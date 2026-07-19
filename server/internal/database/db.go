// Package database wraps the SQLite store: users, nodes, node access
// grants, directories, session metadata and a small settings table.
package database

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct{ *sql.DB }

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// modernc sqlite is not safe for concurrent writers on one conn pool > 1
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'user',      -- 'admin' | 'user'
	status TEXT NOT NULL DEFAULT 'pending', -- 'pending' | 'active'
	node_token_hash TEXT,                   -- legacy single token, migrated to node_tokens
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_login_at TIMESTAMP
);
CREATE TABLE IF NOT EXISTS node_tokens (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	name TEXT NOT NULL DEFAULT '',          -- user-chosen alias
	token_hash TEXT NOT NULL UNIQUE,        -- sha256 of the plaintext token
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS nodes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	token_id INTEGER NOT NULL DEFAULT 0,    -- node token it last connected with
	last_seen TIMESTAMP,
	UNIQUE(owner_id, name)
);
CREATE TABLE IF NOT EXISTS node_access (
	node_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	UNIQUE(node_id, user_id)
);
CREATE TABLE IF NOT EXISTS directories (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL DEFAULT 0,
	name TEXT NOT NULL,
	path TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	node TEXT NOT NULL DEFAULT '',          -- node display name
	node_id INTEGER NOT NULL DEFAULT 0,
	name TEXT NOT NULL,
	cwd TEXT NOT NULL,
	shell TEXT NOT NULL,
	pid INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'running', -- running | exited
	created_at TIMESTAMP NOT NULL,
	last_active TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`)
	if err != nil {
		return err
	}
	// Older DBs: nodes table predates token_id. Ignore "duplicate column".
	if _, err := db.Exec(`ALTER TABLE nodes ADD COLUMN token_id INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	// One-shot migration of the legacy single per-user token into
	// node_tokens; the legacy column is cleared so this never re-runs.
	if _, err := db.Exec(`INSERT INTO node_tokens(user_id, name, token_hash)
		SELECT id, 'default', node_token_hash FROM users
		WHERE node_token_hash IS NOT NULL AND node_token_hash != ''
		ON CONFLICT(token_hash) DO NOTHING`); err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE users SET node_token_hash = NULL`)
	return err
}

// ---- settings ----

func (d *DB) GetSetting(key string) (string, error) {
	var v string
	err := d.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetSetting(key, value string) error {
	_, err := d.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---- users ----

type User struct {
	ID           int64     `json:"uid"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

func (d *DB) UserCount() (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (d *DB) CreateUser(username, passwordHash, role, status string) (int64, error) {
	res, err := d.Exec(`INSERT INTO users(username, password_hash, role, status) VALUES(?,?,?,?)`,
		username, passwordHash, role, status)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const userCols = `id, username, password_hash, role, status, created_at`

func (d *DB) scanUser(row *sql.Row) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (d *DB) GetUser(username string) (*User, error) {
	return d.scanUser(d.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, username))
}

func (d *DB) GetUserByID(id int64) (*User, error) {
	return d.scanUser(d.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// ---- node tokens (multiple per user) ----

type NodeToken struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"-"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func (d *DB) CreateNodeToken(userID int64, name, hash string) (int64, error) {
	res, err := d.Exec(`INSERT INTO node_tokens(user_id, name, token_hash) VALUES(?,?,?)`, userID, name, hash)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) CountNodeTokens(userID int64) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM node_tokens WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

func (d *DB) ListNodeTokens(userID int64) ([]NodeToken, error) {
	rows, err := d.Query(`SELECT id, user_id, name, created_at FROM node_tokens WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeToken{}
	for rows.Next() {
		var t NodeToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetNodeTokenByHash resolves a presented token to its row (nil if unknown).
func (d *DB) GetNodeTokenByHash(hash string) (*NodeToken, error) {
	t := &NodeToken{}
	err := d.QueryRow(`SELECT id, user_id, name, created_at FROM node_tokens WHERE token_hash = ?`, hash).
		Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (d *DB) DeleteNodeToken(id, userID int64) error {
	_, err := d.Exec(`DELETE FROM node_tokens WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// NodeTokenName returns the alias of a token, "" if it no longer exists.
func (d *DB) NodeTokenName(id int64) (string, error) {
	var name string
	err := d.QueryRow(`SELECT name FROM node_tokens WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

func (d *DB) ListUsers() ([]User, error) {
	rows, err := d.Query(`SELECT ` + userCols + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Status, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) ApproveUser(id int64) error {
	_, err := d.Exec(`UPDATE users SET status='active' WHERE id = ?`, id)
	return err
}

// DeleteUser removes the user plus their nodes and every access grant
// involving them (as grantee, or on their nodes).
func (d *DB) DeleteUser(id int64) error {
	stmts := []string{
		`DELETE FROM node_access WHERE user_id = ?`,
		`DELETE FROM node_access WHERE node_id IN (SELECT id FROM nodes WHERE owner_id = ?)`,
		`DELETE FROM sessions WHERE node_id IN (SELECT id FROM nodes WHERE owner_id = ?)`,
		`DELETE FROM directories WHERE user_id = ?`,
		`DELETE FROM node_tokens WHERE user_id = ?`,
		`DELETE FROM nodes WHERE owner_id = ?`,
		`DELETE FROM users WHERE id = ?`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s, id); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) TouchLogin(id int64) error {
	_, err := d.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

// ---- nodes & access ----

type Node struct {
	ID      int64  `json:"id"`
	OwnerID int64  `json:"owner_uid"`
	Name    string `json:"name"`
	TokenID int64  `json:"token_id"`
}

// UpsertNode registers (or refreshes) a node identified by owner+name,
// remembers which token it connected with, and returns its stable id.
func (d *DB) UpsertNode(ownerID int64, name string, tokenID int64) (int64, error) {
	_, err := d.Exec(`INSERT INTO nodes(owner_id, name, token_id, last_seen) VALUES(?,?,?,?)
		ON CONFLICT(owner_id, name) DO UPDATE SET token_id=excluded.token_id, last_seen=excluded.last_seen`,
		ownerID, name, tokenID, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	var id int64
	err = d.QueryRow(`SELECT id FROM nodes WHERE owner_id = ? AND name = ?`, ownerID, name).Scan(&id)
	return id, err
}

func (d *DB) GetNode(id int64) (*Node, error) {
	n := &Node{}
	err := d.QueryRow(`SELECT id, owner_id, name, token_id FROM nodes WHERE id = ?`, id).
		Scan(&n.ID, &n.OwnerID, &n.Name, &n.TokenID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return n, err
}

// ListNodesForUser: everything for admins, owned + shared for others.
func (d *DB) ListNodesForUser(userID int64, isAdmin bool) ([]Node, error) {
	q := `SELECT id, owner_id, name, token_id FROM nodes
		WHERE ? OR owner_id = ? OR id IN (SELECT node_id FROM node_access WHERE user_id = ?)
		ORDER BY name`
	rows, err := d.Query(q, isAdmin, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.OwnerID, &n.Name, &n.TokenID); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (d *DB) UserCanAccessNode(userID, nodeID int64, isAdmin bool) (bool, error) {
	if isAdmin {
		return true, nil
	}
	var ok bool
	err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM nodes WHERE id = ? AND owner_id = ?)
		OR EXISTS(SELECT 1 FROM node_access WHERE node_id = ? AND user_id = ?)`,
		nodeID, userID, nodeID, userID).Scan(&ok)
	return ok, err
}

func (d *DB) GrantNodeAccess(nodeID, userID int64) error {
	_, err := d.Exec(`INSERT INTO node_access(node_id, user_id) VALUES(?,?) ON CONFLICT DO NOTHING`, nodeID, userID)
	return err
}

func (d *DB) RevokeNodeAccess(nodeID, userID int64) error {
	_, err := d.Exec(`DELETE FROM node_access WHERE node_id = ? AND user_id = ?`, nodeID, userID)
	return err
}

// ListNodeShares returns the users a node has been shared with.
func (d *DB) ListNodeShares(nodeID int64) ([]User, error) {
	rows, err := d.Query(`SELECT u.id, u.username FROM node_access a
		JOIN users u ON u.id = a.user_id WHERE a.node_id = ? ORDER BY u.id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---- directories ----

type Directory struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

func (d *DB) ListDirectories(userID int64) ([]Directory, error) {
	rows, err := d.Query(`SELECT id, name, path FROM directories WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Directory{}
	for rows.Next() {
		var dir Directory
		if err := rows.Scan(&dir.ID, &dir.Name, &dir.Path); err != nil {
			return nil, err
		}
		out = append(out, dir)
	}
	return out, rows.Err()
}

func (d *DB) CreateDirectory(userID int64, name, path string) (int64, error) {
	res, err := d.Exec(`INSERT INTO directories(user_id, name, path) VALUES(?,?,?)`, userID, name, path)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) DeleteDirectory(id, userID int64) error {
	_, err := d.Exec(`DELETE FROM directories WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// ---- session metadata ----

type SessionMeta struct {
	ID         string    `json:"id"`
	Node       string    `json:"node"`
	NodeID     int64     `json:"node_id"`
	Name       string    `json:"name"`
	Cwd        string    `json:"cwd"`
	Shell      string    `json:"shell"`
	Pid        int       `json:"pid"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
}

func (d *DB) UpsertSession(s SessionMeta) error {
	_, err := d.Exec(`INSERT INTO sessions(id,node,node_id,name,cwd,shell,pid,status,created_at,last_active)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET node=excluded.node, node_id=excluded.node_id, name=excluded.name,
			cwd=excluded.cwd, shell=excluded.shell, pid=excluded.pid, status=excluded.status, last_active=excluded.last_active`,
		s.ID, s.Node, s.NodeID, s.Name, s.Cwd, s.Shell, s.Pid, s.Status, s.CreatedAt.UTC(), s.LastActive.UTC())
	return err
}

func (d *DB) MarkSessionExited(id string) error {
	_, err := d.Exec(`UPDATE sessions SET status='exited', last_active=? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

func (d *DB) DeleteSession(id string) error {
	_, err := d.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

const sessionCols = `id,node,node_id,name,cwd,shell,pid,status,created_at,last_active`

func (d *DB) scanSessions(rows *sql.Rows) ([]SessionMeta, error) {
	defer rows.Close()
	out := []SessionMeta{}
	for rows.Next() {
		var s SessionMeta
		if err := rows.Scan(&s.ID, &s.Node, &s.NodeID, &s.Name, &s.Cwd, &s.Shell, &s.Pid, &s.Status, &s.CreatedAt, &s.LastActive); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) GetSession(id string) (*SessionMeta, error) {
	rows, err := d.Query(`SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	all, err := d.scanSessions(rows)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	return &all[0], nil
}

// ListSessionsForNode returns every session recorded for one node.
func (d *DB) ListSessionsForNode(nodeID int64) ([]SessionMeta, error) {
	rows, err := d.Query(`SELECT `+sessionCols+` FROM sessions WHERE node_id = ? ORDER BY created_at DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	return d.scanSessions(rows)
}

// ListSessionsForUser returns sessions on every node the user can access.
func (d *DB) ListSessionsForUser(userID int64, isAdmin bool) ([]SessionMeta, error) {
	rows, err := d.Query(`SELECT `+sessionCols+` FROM sessions
		WHERE ? OR node_id IN (
			SELECT id FROM nodes WHERE owner_id = ?
			UNION SELECT node_id FROM node_access WHERE user_id = ?)
		ORDER BY created_at DESC`, isAdmin, userID, userID)
	if err != nil {
		return nil, err
	}
	return d.scanSessions(rows)
}
