// End-to-end smoke test: in-process agent-server + agent-client,
// then a fake browser drives a real PTY session over websockets.
// Covers registration/approval, personal node tokens, node sharing
// and the relay pipeline. This is the one check that fails if any
// of that breaks.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	adminUser = "alice"
	adminPass = "correct-horse-battery"
)

func startServer(t *testing.T) (*httptest.Server, *database.DB) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	a := auth.New([]byte("test-secret"), time.Hour, false)
	h := hub.New(db, log)
	srv := &api.Server{DB: db, Auth: a, Hub: h, Log: log, WebFS: os.DirFS(t.TempDir())}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, db
}

func startClient(t *testing.T, ts *httptest.Server, token, name string) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	node := client.New(ts.URL, token, name, log)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); node.Shutdown() })
	go node.Run(ctx)
}

// doJSON fires an authenticated JSON request and decodes the response.
func doJSON(t *testing.T, ts *httptest.Server, cookie, method, path string, body any, out any) int {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func register(t *testing.T, ts *httptest.Server, user, pass string) string {
	t.Helper()
	var out struct{ Status string }
	if code := doJSON(t, ts, "", "POST", "/api/register", map[string]string{"username": user, "password": pass}, &out); code != 200 {
		t.Fatalf("register %s: status %d", user, code)
	}
	return out.Status
}

func login(t *testing.T, ts *httptest.Server, user, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := http.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login %s: status %d", user, resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func nodeToken(t *testing.T, ts *httptest.Server, cookie, alias string) (int64, string) {
	t.Helper()
	var out struct {
		ID    int64
		Token string
	}
	if code := doJSON(t, ts, cookie, "POST", "/api/me/node-tokens", map[string]string{"name": alias}, &out); code != 200 || out.Token == "" {
		t.Fatalf("node token: status %d token %q", code, out.Token)
	}
	return out.ID, out.Token
}

type stateView struct {
	Nodes []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		OwnerUID  int64  `json:"owner_uid"`
		TokenID   int64  `json:"token_id"`
		TokenName string `json:"token_name"`
		Connected bool   `json:"connected"`
	} `json:"nodes"`
	Sessions []database.SessionMeta `json:"sessions"`
	Tokens   []database.NodeToken   `json:"tokens"`
}

// waitNodeOnline polls /api/state until the named node is connected.
func waitNodeOnline(t *testing.T, ts *httptest.Server, cookie, name string) int64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var st stateView
		doJSON(t, ts, cookie, "GET", "/api/state", nil, &st)
		for _, n := range st.Nodes {
			if n.Name == name && n.Connected {
				return n.ID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("node %s never came online: %+v", name, st.Nodes)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func createSession(t *testing.T, ts *httptest.Server, cookie string, nodeID int64, name string) string {
	t.Helper()
	var out struct{ ID string }
	code := doJSON(t, ts, cookie, "POST", "/api/sessions",
		map[string]any{"name": name, "shell": "/bin/bash", "node_id": nodeID}, &out)
	if code != 200 || out.ID == "" {
		t.Fatalf("create session: status %d", code)
	}
	return out.ID
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

// adminStack: register the first (admin) user, connect their client.
func adminStack(t *testing.T) (*httptest.Server, *database.DB, string, int64) {
	t.Helper()
	ts, db := startServer(t)
	if st := register(t, ts, adminUser, adminPass); st != "active" {
		t.Fatalf("first user status = %s, want active", st)
	}
	cookie := login(t, ts, adminUser, adminPass)
	_, tok := nodeToken(t, ts, cookie, "default")
	startClient(t, ts, tok, "test-pc")
	nodeID := waitNodeOnline(t, ts, cookie, "test-pc")
	return ts, db, cookie, nodeID
}

// TestMultipleNodeTokens: a second token drives a second PC, the state
// view links nodes to tokens, deleting a token kicks its PC, and the
// 16-token cap holds.
func TestMultipleNodeTokens(t *testing.T) {
	ts, _, cookie, _ := adminStack(t)

	tokID, tok2 := nodeToken(t, ts, cookie, "laptop")
	startClient(t, ts, tok2, "second-pc")
	waitNodeOnline(t, ts, cookie, "second-pc")

	var st stateView
	doJSON(t, ts, cookie, "GET", "/api/state", nil, &st)
	if len(st.Tokens) != 2 {
		t.Fatalf("tokens = %d, want 2", len(st.Tokens))
	}
	for _, n := range st.Nodes {
		if n.Name == "second-pc" && (n.TokenID != tokID || n.TokenName != "laptop") {
			t.Fatalf("second-pc token = %d %q, want %d laptop", n.TokenID, n.TokenName, tokID)
		}
	}

	// deleting the token disconnects its PC; the other one stays online
	if code := doJSON(t, ts, cookie, "DELETE", fmt.Sprintf("/api/me/node-tokens/%d", tokID), nil, nil); code != 200 {
		t.Fatalf("delete token: status %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		doJSON(t, ts, cookie, "GET", "/api/state", nil, &st)
		second, first := true, false
		for _, n := range st.Nodes {
			if n.Name == "second-pc" {
				second = n.Connected
			}
			if n.Name == "test-pc" {
				first = n.Connected
			}
		}
		if !second && first {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after token delete: nodes = %+v", st.Nodes)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// the deleted token can no longer authenticate
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/node/ws"
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer " + tok2}}); err == nil || resp.StatusCode != 401 {
		t.Fatalf("deleted token still accepted: %v", resp)
	}

	// cap: fill up to 16, the 17th is rejected
	for i := 2; i <= 16; i++ {
		nodeToken(t, ts, cookie, fmt.Sprintf("t%d", i))
	}
	if code := doJSON(t, ts, cookie, "POST", "/api/me/node-tokens", map[string]string{"name": "overflow"}, nil); code != 400 {
		t.Fatalf("17th token: status %d, want 400", code)
	}
}

func TestTerminalRoundTripAndReattach(t *testing.T) {
	ts, _, cookie, nodeID := adminStack(t)

	// unauthenticated requests must be rejected
	resp, _ := http.Get(ts.URL + "/api/state")
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated /api/state: %d", resp.StatusCode)
	}
	resp.Body.Close()

	sessionID := createSession(t, ts, cookie, nodeID, "e2e")

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
	if code := doJSON(t, ts, cookie, "DELETE", "/api/sessions/"+sessionID, nil, nil); code != 200 {
		t.Fatalf("kill session: status %d", code)
	}

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
	ts, db, cookie, nodeID := adminStack(t)

	// /api/browse lists subdirectories of a known path
	parent := t.TempDir()
	os.Mkdir(filepath.Join(parent, "subproj"), 0o755)
	var browse struct {
		Path string
		Dirs []string
	}
	code := doJSON(t, ts, cookie, "GET", fmt.Sprintf("/api/browse?node_id=%d&path=%s", nodeID, parent), nil, &browse)
	if code != 200 {
		t.Fatalf("browse: status %d", code)
	}
	if len(browse.Dirs) != 1 || browse.Dirs[0] != "subproj" {
		t.Fatalf("browse dirs = %v, want [subproj]", browse.Dirs)
	}

	// cd inside session 1, then "open CLI here" via from_session
	id1 := createSession(t, ts, cookie, nodeID, "src")
	conn := dialTerminal(t, ts, cookie, id1)
	defer conn.Close()
	target := filepath.Join(parent, "subproj")
	// marker is computed so the terminal echo of the typed line can't match
	conn.WriteMessage(websocket.BinaryMessage, []byte("cd "+target+" && echo moved-$((40+2))\n"))
	readUntil(t, conn, "moved-42", 5*time.Second)

	var out struct{ ID string }
	code = doJSON(t, ts, cookie, "POST", "/api/sessions",
		map[string]any{"name": "child", "from_session": id1}, &out)
	if code != 200 || out.ID == "" {
		t.Fatalf("create from_session: status %d", code)
	}

	want, _ := filepath.EvalSymlinks(target) // /proc cwd is fully resolved
	sess, err := db.GetSession(out.ID)
	if err != nil || sess == nil {
		t.Fatalf("child session not found: %v", err)
	}
	if sess.Cwd != want {
		t.Fatalf("child cwd = %q, want %q", sess.Cwd, want)
	}
}

// TestMultiUserApprovalAndSharing walks the whole multi-user flow:
// pending registration, admin approval, node invisibility, 403s for
// non-shared users, then sharing (and onward sharing) making it work.
func TestMultiUserApprovalAndSharing(t *testing.T) {
	ts, _, adminCookie, nodeID := adminStack(t)

	// bob registers -> pending, login rejected with 403
	if st := register(t, ts, "bob", "hunter2hunter2"); st != "pending" {
		t.Fatalf("bob status = %s, want pending", st)
	}
	body, _ := json.Marshal(map[string]string{"username": "bob", "password": "hunter2hunter2"})
	resp, _ := http.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(body))
	if resp.StatusCode != 403 {
		t.Fatalf("pending login: status %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// admin approves bob (uid from the admin user list)
	var users []database.User
	doJSON(t, ts, adminCookie, "GET", "/api/users", nil, &users)
	var bobUID int64
	for _, u := range users {
		if u.Username == "bob" {
			bobUID = u.ID
		}
	}
	if bobUID == 0 {
		t.Fatal("bob not in user list")
	}
	if code := doJSON(t, ts, adminCookie, "POST", fmt.Sprintf("/api/users/%d/approve", bobUID), nil, nil); code != 200 {
		t.Fatalf("approve: status %d", code)
	}
	bobCookie := login(t, ts, "bob", "hunter2hunter2")

	// alice's node is invisible to bob, and everything node-scoped is 403
	var st stateView
	doJSON(t, ts, bobCookie, "GET", "/api/state", nil, &st)
	if len(st.Nodes) != 0 {
		t.Fatalf("bob sees %d nodes before sharing, want 0", len(st.Nodes))
	}
	if code := doJSON(t, ts, bobCookie, "POST", "/api/sessions",
		map[string]any{"name": "x", "node_id": nodeID}, nil); code != 403 {
		t.Fatalf("unshared create: status %d, want 403", code)
	}
	if code := doJSON(t, ts, bobCookie, "GET", fmt.Sprintf("/api/browse?node_id=%d", nodeID), nil, nil); code != 403 {
		t.Fatalf("unshared browse: status %d, want 403", code)
	}

	// admin's session is also unreachable for bob
	sid := createSession(t, ts, adminCookie, nodeID, "secret")
	if code := doJSON(t, ts, bobCookie, "DELETE", "/api/sessions/"+sid, nil, nil); code != 403 {
		t.Fatalf("unshared kill: status %d, want 403", code)
	}

	// alice shares the node with bob -> bob can see and use it
	if code := doJSON(t, ts, adminCookie, "POST", fmt.Sprintf("/api/nodes/%d/share", nodeID),
		map[string]any{"uid": bobUID}, nil); code != 200 {
		t.Fatalf("share: status %d", code)
	}
	doJSON(t, ts, bobCookie, "GET", "/api/state", nil, &st)
	if len(st.Nodes) != 1 || !st.Nodes[0].Connected {
		t.Fatalf("bob nodes after share = %+v", st.Nodes)
	}
	bobSession := createSession(t, ts, bobCookie, nodeID, "bobs")
	conn := dialTerminal(t, ts, bobCookie, bobSession)
	defer conn.Close()
	conn.WriteMessage(websocket.BinaryMessage, []byte("echo shared-$((40+2))\n"))
	readUntil(t, conn, "shared-42", 5*time.Second)

	// onward sharing: bob (a grantee, not the owner) shares with carol
	register(t, ts, "carol", "carol-pass-123")
	doJSON(t, ts, adminCookie, "GET", "/api/users", nil, &users)
	var carolUID int64
	for _, u := range users {
		if u.Username == "carol" {
			carolUID = u.ID
		}
	}
	doJSON(t, ts, adminCookie, "POST", fmt.Sprintf("/api/users/%d/approve", carolUID), nil, nil)
	if code := doJSON(t, ts, bobCookie, "POST", fmt.Sprintf("/api/nodes/%d/share", nodeID),
		map[string]any{"uid": carolUID}, nil); code != 200 {
		t.Fatalf("onward share: status %d", code)
	}
	carolCookie := login(t, ts, "carol", "carol-pass-123")
	doJSON(t, ts, carolCookie, "GET", "/api/state", nil, &st)
	if len(st.Nodes) != 1 {
		t.Fatalf("carol nodes after onward share = %+v", st.Nodes)
	}

	// revoke: only owner/admin (bob revoking carol is rejected... he is
	// not the owner and not removing himself)
	if code := doJSON(t, ts, bobCookie, "DELETE", fmt.Sprintf("/api/nodes/%d/share/%d", nodeID, carolUID), nil, nil); code != 403 {
		t.Fatalf("grantee revoking other: status %d, want 403", code)
	}
	if code := doJSON(t, ts, adminCookie, "DELETE", fmt.Sprintf("/api/nodes/%d/share/%d", nodeID, carolUID), nil, nil); code != 200 {
		t.Fatalf("owner revoke: status %d", code)
	}
	doJSON(t, ts, carolCookie, "GET", "/api/state", nil, &st)
	if len(st.Nodes) != 0 {
		t.Fatalf("carol nodes after revoke = %+v", st.Nodes)
	}
}

func TestNodeTokenRequired(t *testing.T) {
	ts, _ := startServer(t)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/node/ws"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer wrong-token"}})
	if err == nil {
		t.Fatal("node websocket accepted a bad token")
	}
	if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %v", resp)
	}
}
