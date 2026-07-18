// Package api wires up HTTP routes: auth, registration, users, nodes,
// directories, sessions, dashboard state and the two websocket endpoints.
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentdock/server/internal/auth"
	"agentdock/server/internal/database"
	"agentdock/server/internal/hub"
)

type Server struct {
	DB    *database.DB
	Auth  *auth.Auth
	Hub   *hub.Hub
	Log   *slog.Logger
	WebFS fs.FS
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/register", s.handleRegister)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.authed(s.handleMe))
	mux.HandleFunc("POST /api/me/node-token", s.authed(s.handleNodeToken))
	mux.HandleFunc("GET /api/state", s.authed(s.handleState))
	mux.HandleFunc("GET /api/users", s.authed(s.handleListUsers))
	mux.HandleFunc("POST /api/users/{id}/approve", s.authed(s.handleApproveUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.authed(s.handleDeleteUser))
	mux.HandleFunc("POST /api/nodes/{id}/share", s.authed(s.handleShareNode))
	mux.HandleFunc("DELETE /api/nodes/{id}/share/{uid}", s.authed(s.handleRevokeShare))
	mux.HandleFunc("GET /api/directories", s.authed(s.handleListDirectories))
	mux.HandleFunc("POST /api/directories", s.authed(s.handleCreateDirectory))
	mux.HandleFunc("DELETE /api/directories/{id}", s.authed(s.handleDeleteDirectory))
	mux.HandleFunc("GET /api/browse", s.authed(s.handleBrowse))
	mux.HandleFunc("POST /api/sessions", s.authed(s.handleCreateSession))
	mux.HandleFunc("DELETE /api/sessions/{id}", s.authed(s.handleKillSession))
	mux.HandleFunc("GET /api/sessions/{id}/ws", s.authed(s.handleBrowserWS))
	mux.HandleFunc("GET /api/node/ws", s.handleNodeWS)
	mux.HandleFunc("/", s.handleStatic)

	return s.originCheck(mux)
}

// originCheck is a cheap CSRF guard: cookie-authenticated state-changing
// requests must come from our own origin (or have no Origin, e.g. curl
// without cookies won't pass auth anyway).
func (s *Server) originCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					http.Error(w, `{"error":"bad origin"}`, http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authed resolves the cookie to a live, active user on every request so
// approval revocations and deletions take effect immediately.
// ponytail: one SQLite point-read per request; cache if it ever hurts.
func (s *Server) authed(fn func(http.ResponseWriter, *http.Request, *database.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := s.Auth.UserFromRequest(r)
		if username == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		u, err := s.DB.GetUser(username)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if u == nil || u.Status != "active" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		fn(w, r, u)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ---- auth & registration ----

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || len(req.Username) > 64 {
		writeErr(w, http.StatusBadRequest, "username is required (max 64 chars)")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// First registered user becomes the active admin; everyone after
	// starts pending until an admin approves them.
	n, err := s.DB.UserCount()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	role, status := "user", "pending"
	if n == 0 {
		role, status = "admin", "active"
	}
	if _, err := s.DB.CreateUser(req.Username, hash, role, status); err != nil {
		writeErr(w, http.StatusConflict, "username already taken")
		return
	}
	s.Log.Info("user registered", "username", req.Username, "role", role, "status", status)
	writeJSON(w, map[string]string{"username": req.Username, "status": status})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	user, err := s.DB.GetUser(req.Username)
	if err != nil {
		s.Log.Error("get user", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Burn a bcrypt round even for unknown users to keep timing uniform.
	hash := "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	if user != nil {
		hash = user.PasswordHash
	}
	if !auth.CheckPassword(hash, req.Password) || user == nil {
		time.Sleep(300 * time.Millisecond) // slow down brute force
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if user.Status != "active" {
		writeErr(w, http.StatusForbidden, "account is pending admin approval")
		return
	}
	token, err := s.Auth.IssueToken(user.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.DB.TouchLogin(user.ID)
	s.Auth.SetCookie(w, token)
	s.Log.Info("login ok", "user", user.Username, "remote", r.RemoteAddr)
	writeJSON(w, map[string]any{"username": user.Username, "uid": user.ID, "role": user.Role})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.Auth.ClearCookie(w)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, u *database.User) {
	writeJSON(w, map[string]any{"username": u.Username, "uid": u.ID, "role": u.Role})
}

// handleNodeToken (re)generates the caller's personal node token. The
// plaintext is returned exactly once; only its sha256 is stored, and any
// previously issued token stops working.
func (s *Server) handleNodeToken(w http.ResponseWriter, r *http.Request, u *database.User) {
	b := make([]byte, 24)
	rand.Read(b)
	token := "adk_" + hex.EncodeToString(b)
	if err := s.DB.SetNodeTokenHash(u.ID, hashToken(token)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Log.Info("node token generated", "user", u.Username)
	writeJSON(w, map[string]string{"token": token})
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ---- dashboard state ----

func (s *Server) handleState(w http.ResponseWriter, r *http.Request, u *database.User) {
	nodes, err := s.DB.ListNodesForUser(u.ID, u.IsAdmin())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	online := s.Hub.OnlineNodes()
	type nodeView struct {
		database.Node
		Owner     string          `json:"owner"`
		Connected bool            `json:"connected"`
		Shares    []database.User `json:"shares"`
	}
	nodeViews := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		nv := nodeView{Node: n, Connected: online[n.ID]}
		if owner, err := s.DB.GetUserByID(n.OwnerID); err == nil && owner != nil {
			nv.Owner = owner.Username
		}
		if shares, err := s.DB.ListNodeShares(n.ID); err == nil {
			nv.Shares = shares
		}
		nodeViews = append(nodeViews, nv)
	}
	sessions, err := s.DB.ListSessionsForUser(u.ID, u.IsAdmin())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	dirs, err := s.DB.ListDirectories(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, map[string]any{
		"me":          map[string]any{"username": u.Username, "uid": u.ID, "role": u.Role},
		"nodes":       nodeViews,
		"sessions":    sessions,
		"directories": dirs,
	})
}

// ---- users (admin management + share picker) ----

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, u *database.User) {
	users, err := s.DB.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if u.IsAdmin() {
		writeJSON(w, users)
		return
	}
	// Non-admins only get the minimal directory needed to share nodes.
	type slim struct {
		UID      int64  `json:"uid"`
		Username string `json:"username"`
	}
	out := []slim{}
	for _, x := range users {
		if x.Status == "active" {
			out = append(out, slim{x.ID, x.Username})
		}
	}
	writeJSON(w, out)
}

func (s *Server) adminOnly(w http.ResponseWriter, u *database.User) bool {
	if !u.IsAdmin() {
		writeErr(w, http.StatusForbidden, "admin only")
		return false
	}
	return true
}

func (s *Server) handleApproveUser(w http.ResponseWriter, r *http.Request, u *database.User) {
	if !s.adminOnly(w, u) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.DB.ApproveUser(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Log.Info("user approved", "uid", id, "by", u.Username)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, u *database.User) {
	if !s.adminOnly(w, u) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if id == u.ID {
		writeErr(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}
	if err := s.DB.DeleteUser(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Log.Info("user deleted", "uid", id, "by", u.Username)
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- node sharing ----

// canAccessNode is the single access check for everything node-scoped.
func (s *Server) canAccessNode(w http.ResponseWriter, u *database.User, nodeID int64) bool {
	ok, err := s.DB.UserCanAccessNode(u.ID, nodeID, u.IsAdmin())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "no access to this node")
		return false
	}
	return true
}

func (s *Server) handleShareNode(w http.ResponseWriter, r *http.Request, u *database.User) {
	nodeID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad node id")
		return
	}
	var req struct{ UID int64 }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	// Anyone with access may share onward (per design).
	if !s.canAccessNode(w, u, nodeID) {
		return
	}
	target, err := s.DB.GetUserByID(req.UID)
	if err != nil || target == nil || target.Status != "active" {
		writeErr(w, http.StatusBadRequest, "no such active user")
		return
	}
	node, err := s.DB.GetNode(nodeID)
	if err != nil || node == nil {
		writeErr(w, http.StatusNotFound, "no such node")
		return
	}
	if node.OwnerID == target.ID {
		writeErr(w, http.StatusBadRequest, "user already owns this node")
		return
	}
	if err := s.DB.GrantNodeAccess(nodeID, target.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Log.Info("node shared", "node", nodeID, "to", target.Username, "by", u.Username)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request, u *database.User) {
	nodeID, err1 := strconv.ParseInt(r.PathValue("id"), 10, 64)
	uid, err2 := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	node, err := s.DB.GetNode(nodeID)
	if err != nil || node == nil {
		writeErr(w, http.StatusNotFound, "no such node")
		return
	}
	// Only the owner or an admin may revoke (a grantee may drop itself).
	if node.OwnerID != u.ID && !u.IsAdmin() && uid != u.ID {
		writeErr(w, http.StatusForbidden, "only the owner or an admin can revoke")
		return
	}
	if err := s.DB.RevokeNodeAccess(nodeID, uid); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- directories ----

func (s *Server) handleListDirectories(w http.ResponseWriter, r *http.Request, u *database.User) {
	dirs, err := s.DB.ListDirectories(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, dirs)
}

func (s *Server) handleCreateDirectory(w http.ResponseWriter, r *http.Request, u *database.User) {
	var req struct{ Name, Path string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	req.Name, req.Path = strings.TrimSpace(req.Name), strings.TrimSpace(req.Path)
	if req.Name == "" || req.Path == "" {
		writeErr(w, http.StatusBadRequest, "name and path are required")
		return
	}
	id, err := s.DB.CreateDirectory(u.ID, req.Name, req.Path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, database.Directory{ID: id, Name: req.Name, Path: req.Path})
}

func (s *Server) handleDeleteDirectory(w http.ResponseWriter, r *http.Request, u *database.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.DB.DeleteDirectory(id, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- filesystem browsing (proxied to a PC node) ----

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request, u *database.User) {
	nodeID, err := strconv.ParseInt(r.URL.Query().Get("node_id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "node_id is required")
		return
	}
	if !s.canAccessNode(w, u, nodeID) {
		return
	}
	path, dirs, err := s.Hub.ListDir(nodeID, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"path": path, "dirs": dirs})
}

// ---- sessions ----

// sessionForUser loads a session and enforces node access in one place.
func (s *Server) sessionForUser(w http.ResponseWriter, u *database.User, sessionID string) *database.SessionMeta {
	sess, err := s.DB.GetSession(sessionID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return nil
	}
	if sess == nil {
		writeErr(w, http.StatusNotFound, "no such session")
		return nil
	}
	if !s.canAccessNode(w, u, sess.NodeID) {
		return nil
	}
	return sess
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request, u *database.User) {
	var req struct {
		Name, Cwd, Shell string
		NodeID           int64  `json:"node_id"`
		FromSession      string `json:"from_session"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.FromSession != "" {
		// The target node is implied by the source session.
		src := s.sessionForUser(w, u, req.FromSession)
		if src == nil {
			return
		}
		req.NodeID = src.NodeID
	} else {
		if req.NodeID == 0 {
			writeErr(w, http.StatusBadRequest, "node_id is required")
			return
		}
		if !s.canAccessNode(w, u, req.NodeID) {
			return
		}
	}
	id, err := s.Hub.CreateSession(req.NodeID, req.Name, req.Cwd, req.Shell, req.FromSession)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Log.Info("session created", "id", id, "name", req.Name, "node", req.NodeID, "by", u.Username)
	writeJSON(w, map[string]string{"id": id})
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request, u *database.User) {
	sess := s.sessionForUser(w, u, r.PathValue("id"))
	if sess == nil {
		return
	}
	if err := s.Hub.KillSession(sess.ID); err != nil {
		// Node offline: still let the user clean up dead metadata.
		s.DB.DeleteSession(sess.ID)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- websockets ----

func (s *Server) handleBrowserWS(w http.ResponseWriter, r *http.Request, u *database.User) {
	// Browsers can't set headers on WebSocket; the session cookie already
	// authenticated this request via s.authed. Enforce origin here since
	// WS bypasses the non-GET origin check.
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
			writeErr(w, http.StatusForbidden, "bad origin")
			return
		}
	}
	sess := s.sessionForUser(w, u, r.PathValue("id"))
	if sess == nil {
		return
	}
	s.Hub.ServeBrowser(w, r, sess.ID)
}

// handleNodeWS authenticates an agent-client by its owner's personal
// node token (Bearer) and hands the connection to the hub.
func (s *Server) handleNodeWS(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		writeErr(w, http.StatusUnauthorized, "invalid node token")
		return
	}
	owner, err := s.DB.GetUserByTokenHash(hashToken(token))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if owner == nil || owner.Status != "active" {
		writeErr(w, http.StatusUnauthorized, "invalid node token")
		return
	}
	s.Hub.ServeNode(w, r, owner.ID)
}

// ---- static SPA ----

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(s.WebFS, path); err != nil {
		path = "index.html" // SPA fallback
	}
	http.ServeFileFS(w, r, s.WebFS, path)
}
