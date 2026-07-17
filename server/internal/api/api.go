// Package api wires up HTTP routes: auth, directories, sessions,
// dashboard state and the two websocket endpoints.
package api

import (
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
	DB        *database.DB
	Auth      *auth.Auth
	Hub       *hub.Hub
	NodeToken string
	Log       *slog.Logger
	WebFS     fs.FS
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.authed(s.handleMe))
	mux.HandleFunc("GET /api/state", s.authed(s.handleState))
	mux.HandleFunc("GET /api/directories", s.authed(s.handleListDirectories))
	mux.HandleFunc("POST /api/directories", s.authed(s.handleCreateDirectory))
	mux.HandleFunc("DELETE /api/directories/{id}", s.authed(s.handleDeleteDirectory))
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

func (s *Server) authed(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Auth.UserFromRequest(r) == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		fn(w, r)
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

// ---- auth ----

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
	token, err := s.Auth.IssueToken(user.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.DB.TouchLogin(user.ID)
	s.Auth.SetCookie(w, token)
	s.Log.Info("login ok", "user", user.Username, "remote", r.RemoteAddr)
	writeJSON(w, map[string]string{"username": user.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.Auth.ClearCookie(w)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"username": s.Auth.UserFromRequest(r)})
}

// ---- dashboard state ----

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	nodeName, connected, _ := s.Hub.NodeStatus()
	sessions, err := s.DB.ListSessions()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	dirs, err := s.DB.ListDirectories()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, map[string]any{
		"node":        map[string]any{"name": nodeName, "connected": connected},
		"sessions":    sessions,
		"directories": dirs,
	})
}

// ---- directories ----

func (s *Server) handleListDirectories(w http.ResponseWriter, r *http.Request) {
	dirs, err := s.DB.ListDirectories()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, dirs)
}

func (s *Server) handleCreateDirectory(w http.ResponseWriter, r *http.Request) {
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
	id, err := s.DB.CreateDirectory(req.Name, req.Path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, database.Directory{ID: id, Name: req.Name, Path: req.Path})
}

func (s *Server) handleDeleteDirectory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.DB.DeleteDirectory(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- sessions ----

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Cwd, Shell string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	id, err := s.Hub.CreateSession(req.Name, req.Cwd, req.Shell)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Log.Info("session created", "id", id, "name", req.Name)
	writeJSON(w, map[string]string{"id": id})
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Hub.KillSession(id); err != nil {
		// Node offline: still let the user clean up dead metadata.
		s.DB.DeleteSession(id)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- websockets ----

func (s *Server) handleBrowserWS(w http.ResponseWriter, r *http.Request) {
	// Browsers can't set headers on WebSocket; the session cookie already
	// authenticated this request via s.authed. Enforce origin here since
	// WS bypasses the non-GET origin check.
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
			writeErr(w, http.StatusForbidden, "bad origin")
			return
		}
	}
	s.Hub.ServeBrowser(w, r, r.PathValue("id"))
}

func (s *Server) handleNodeWS(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || !auth.ConstantTimeEquals(token, s.NodeToken) {
		writeErr(w, http.StatusUnauthorized, "invalid node token")
		return
	}
	s.Hub.ServeNode(w, r)
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
