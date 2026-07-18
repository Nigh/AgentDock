// End-to-end smoke test: in-process agent-server + agent-client,
// then a fake browser drives a real PTY session over websockets.
// This is the one check that fails if the relay pipeline breaks.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"agentdock/client"
	"agentdock/server/internal/api"
	"agentdock/server/internal/auth"
	"agentdock/server/internal/database"
	"agentdock/server/internal/hub"
)

const (
	testUser  = "alice"
	testPass  = "correct-horse-battery"
	nodeToken = "test-node-token-0123456789"
)

func startStack(t *testing.T) (*httptest.Server, *database.DB) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	hash, _ := auth.HashPassword(testPass)
	if err := db.CreateUser(testUser, hash); err != nil {
		t.Fatal(err)
	}

	a := auth.New([]byte("test-secret"), time.Hour, false)
	h := hub.New(db, log)
	srv := &api.Server{DB: db, Auth: a, Hub: h, NodeToken: nodeToken, Log: log, WebFS: os.DirFS(t.TempDir())}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	// agent-client dialing out to the test server
	node := client.New(ts.URL, nodeToken, "test-pc", log)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); node.Shutdown() })
	go node.Run(ctx)

	return ts, db
}

func login(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": testUser, "password": testPass})
	resp, err := http.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func createSession(t *testing.T, ts *httptest.Server, cookie string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": "e2e", "shell": "/bin/bash"})
	req, _ := http.NewRequest("POST", ts.URL+"/api/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	// node connects asynchronously; retry create until it is online
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == 200 {
			var out struct{ ID string }
			json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			return out.ID
		}
		resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatalf("create session: status %d", resp.StatusCode)
		}
		time.Sleep(100 * time.Millisecond)
		req.Body = http.NoBody
		req, _ = http.NewRequest("POST", ts.URL+"/api/sessions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", cookie)
	}
}

func dialTerminal(t *testing.T, ts *httptest.Server, cookie, sessionID string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/sessions/" + sessionID + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Cookie": {cookie}})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// readUntil collects binary frames until the needle shows up or the deadline hits.
func readUntil(t *testing.T, conn *websocket.Conn, needle string, timeout time.Duration) string {
	t.Helper()
	var got bytes.Buffer
	conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for %q, got so far: %q, err: %v", needle, got.String(), err)
		}
		if mt == websocket.BinaryMessage {
			got.Write(data)
		}
		if strings.Contains(got.String(), needle) {
			return got.String()
		}
	}
}

func TestTerminalRoundTripAndReattach(t *testing.T) {
	ts, _ := startStack(t)

	// unauthenticated requests must be rejected
	resp, _ := http.Get(ts.URL + "/api/state")
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated /api/state: %d", resp.StatusCode)
	}
	resp.Body.Close()

	cookie := login(t, ts)
	sessionID := createSession(t, ts, cookie)

	// attach, run a command, see its output
	conn := dialTerminal(t, ts, cookie, sessionID)
	conn.WriteMessage(websocket.BinaryMessage, []byte("echo agentdock-$((40+2))\n"))
	readUntil(t, conn, "agentdock-42", 5*time.Second)

	// resize control frame must not kill anything
	conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":100,"rows":40}`))

	// detach (browser closes), session must survive
	conn.Close()
	time.Sleep(200 * time.Millisecond)

	// re-attach: scrollback replay must contain the old output
	conn2 := dialTerminal(t, ts, cookie, sessionID)
	defer conn2.Close()
	readUntil(t, conn2, "agentdock-42", 5*time.Second)

	// kill the session via the API
	req, _ := http.NewRequest("DELETE", ts.URL+"/api/sessions/"+sessionID, nil)
	req.Header.Set("Cookie", cookie)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()

	// browser gets told the session exited
	conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := conn2.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for exited notice: %v", err)
		}
		if mt == websocket.TextMessage && strings.Contains(string(data), "exited") {
			break
		}
	}
}

// TestBrowseAndCliHere covers the two directory-navigation features:
// GET /api/browse proxies a directory listing from the node, and a
// create with from_session inherits the source shell's live cwd.
func TestBrowseAndCliHere(t *testing.T) {
	ts, db := startStack(t)
	cookie := login(t, ts)

	// /api/browse lists subdirectories of a known path
	parent := t.TempDir()
	os.Mkdir(filepath.Join(parent, "subproj"), 0o755)
	req, _ := http.NewRequest("GET", ts.URL+"/api/browse?path="+parent, nil)
	req.Header.Set("Cookie", cookie)
	var browse struct {
		Path string
		Dirs []string
	}
	deadline := time.Now().Add(5 * time.Second)
	for { // node connects asynchronously; retry until online
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == 200 {
			json.NewDecoder(resp.Body).Decode(&browse)
			resp.Body.Close()
			break
		}
		resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatalf("browse: status %d", resp.StatusCode)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(browse.Dirs) != 1 || browse.Dirs[0] != "subproj" {
		t.Fatalf("browse dirs = %v, want [subproj]", browse.Dirs)
	}

	// cd inside session 1, then "open CLI here" via from_session
	id1 := createSession(t, ts, cookie)
	conn := dialTerminal(t, ts, cookie, id1)
	defer conn.Close()
	target := filepath.Join(parent, "subproj")
	// marker is computed so the terminal echo of the typed line can't match
	conn.WriteMessage(websocket.BinaryMessage, []byte("cd "+target+" && echo moved-$((40+2))\n"))
	readUntil(t, conn, "moved-42", 5*time.Second)

	body, _ := json.Marshal(map[string]string{"name": "child", "from_session": id1})
	req2, _ := http.NewRequest("POST", ts.URL+"/api/sessions", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Cookie", cookie)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ ID string }
	json.NewDecoder(resp2.Body).Decode(&out)
	resp2.Body.Close()
	if resp2.StatusCode != 200 || out.ID == "" {
		t.Fatalf("create from_session: status %d", resp2.StatusCode)
	}

	want, _ := filepath.EvalSymlinks(target) // /proc cwd is fully resolved
	sessions, err := db.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.ID == out.ID {
			if s.Cwd != want {
				t.Fatalf("child cwd = %q, want %q", s.Cwd, want)
			}
			return
		}
	}
	t.Fatal("child session not found")
}

func TestNodeTokenRequired(t *testing.T) {
	ts, _ := startStack(t)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/node/ws"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer wrong-token"}})
	if err == nil {
		t.Fatal("node websocket accepted a bad token")
	}
	if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %v", resp)
	}
}
