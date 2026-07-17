// Package database wraps the SQLite store: users, directories,
// session metadata and a small settings key/value table.
package database

import (
	"database/sql"
	"errors"
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
	role TEXT NOT NULL DEFAULT 'admin', -- reserved for future multi-user
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_login_at TIMESTAMP
);
CREATE TABLE IF NOT EXISTS directories (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	path TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	node TEXT NOT NULL DEFAULT '', -- reserved for future multi-node
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
	ID           int64
	Username     string
	PasswordHash string
	Role         string
}

func (d *DB) UserCount() (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (d *DB) CreateUser(username, passwordHash string) error {
	_, err := d.Exec(`INSERT INTO users(username, password_hash) VALUES(?,?)`, username, passwordHash)
	return err
}

func (d *DB) GetUser(username string) (*User, error) {
	u := &User{}
	err := d.QueryRow(`SELECT id, username, password_hash, role FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (d *DB) TouchLogin(id int64) error {
	_, err := d.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

// ---- directories ----

type Directory struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

func (d *DB) ListDirectories() ([]Directory, error) {
	rows, err := d.Query(`SELECT id, name, path FROM directories ORDER BY name`)
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

func (d *DB) CreateDirectory(name, path string) (int64, error) {
	res, err := d.Exec(`INSERT INTO directories(name, path) VALUES(?,?)`, name, path)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) DeleteDirectory(id int64) error {
	_, err := d.Exec(`DELETE FROM directories WHERE id = ?`, id)
	return err
}

// ---- session metadata ----

type SessionMeta struct {
	ID         string    `json:"id"`
	Node       string    `json:"node"`
	Name       string    `json:"name"`
	Cwd        string    `json:"cwd"`
	Shell      string    `json:"shell"`
	Pid        int       `json:"pid"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
}

func (d *DB) UpsertSession(s SessionMeta) error {
	_, err := d.Exec(`INSERT INTO sessions(id,node,name,cwd,shell,pid,status,created_at,last_active)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET node=excluded.node, name=excluded.name, cwd=excluded.cwd,
			shell=excluded.shell, pid=excluded.pid, status=excluded.status, last_active=excluded.last_active`,
		s.ID, s.Node, s.Name, s.Cwd, s.Shell, s.Pid, s.Status, s.CreatedAt.UTC(), s.LastActive.UTC())
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

func (d *DB) ListSessions() ([]SessionMeta, error) {
	rows, err := d.Query(`SELECT id,node,name,cwd,shell,pid,status,created_at,last_active FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionMeta{}
	for rows.Next() {
		var s SessionMeta
		if err := rows.Scan(&s.ID, &s.Node, &s.Name, &s.Cwd, &s.Shell, &s.Pid, &s.Status, &s.CreatedAt, &s.LastActive); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
