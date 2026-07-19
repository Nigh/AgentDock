// Package hub relays terminal traffic between browser websockets and
// the node (agent-client) websocket, and tracks live session state.
package hub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"agentdock/internal/protocol"
	"agentdock/server/internal/database"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
	// Origin is enforced by the API layer's CSRF/origin check for browsers;
	// the node connection authenticates with a bearer token, not cookies.
	CheckOrigin: func(r *http.Request) bool { return true },
}

type nodeConn struct {
	id       int64 // db node id, assigned on hello
	ownerID  int64 // authenticated owner (from the node token)
	tokenID  int64 // db id of the node token this connection used
	name     string
	conn     *websocket.Conn
	writeMu  sync.Mutex
	sessions map[string]protocol.Session
}

type browserConn struct {
	id        string
	sessionID string
	nodeID    int64 // resolved once at attach; a session never moves nodes
	conn      *websocket.Conn
	writeMu   sync.Mutex
	ready     bool // becomes true once the scrollback buffer arrived
}

type Hub struct {
	db  *database.DB
	log *slog.Logger

	mu             sync.Mutex
	nodes          map[int64]*nodeConn               // db node id -> live connection
	sessionNode    map[string]int64                  // sessionID -> node id (routing)
	browsers       map[string]map[*browserConn]bool  // sessionID -> attached browsers
	pendingCreate  map[string]chan error             // sessionID -> create ack
	pendingListDir map[string]chan *protocol.Message // request ConnID -> dirlist reply
}

func New(db *database.DB, log *slog.Logger) *Hub {
	return &Hub{
		db:             db,
		log:            log,
		nodes:          map[int64]*nodeConn{},
		sessionNode:    map[string]int64{},
		browsers:       map[string]map[*browserConn]bool{},
		pendingCreate:  map[string]chan error{},
		pendingListDir: map[string]chan *protocol.Message{},
	}
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- node side ----

// ServeNode upgrades and runs the websocket for an agent-client owned
// by ownerID (already authenticated via one of its node tokens).
func (h *Hub) ServeNode(w http.ResponseWriter, r *http.Request, ownerID, tokenID int64) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Error("node upgrade failed", "err", err)
		return
	}
	n := &nodeConn{ownerID: ownerID, tokenID: tokenID, conn: conn, sessions: map[string]protocol.Session{}}

	h.log.Info("node connected", "remote", r.RemoteAddr, "owner", ownerID)
	h.readNode(n)

	h.mu.Lock()
	registered := n.id != 0 && h.nodes[n.id] == n
	if registered {
		delete(h.nodes, n.id)
	}
	var orphaned []string
	for sid, nid := range h.sessionNode {
		if nid == n.id && registered {
			orphaned = append(orphaned, sid)
		}
	}
	h.mu.Unlock()
	conn.Close()
	h.log.Info("node disconnected", "name", n.name, "owner", ownerID)
	for _, sid := range orphaned {
		h.notifySessionBrowsers(sid, `{"type":"node_offline"}`)
	}
}

func (h *Hub) readNode(n *nodeConn) {
	n.conn.SetReadLimit(4 << 20)
	n.conn.SetReadDeadline(time.Now().Add(pongWait))
	n.conn.SetPongHandler(func(string) error {
		n.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(pingPeriod)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-t.C:
				n.writeMu.Lock()
				n.conn.SetWriteDeadline(time.Now().Add(writeWait))
				err := n.conn.WriteMessage(websocket.PingMessage, nil)
				n.writeMu.Unlock()
				if err != nil {
					n.conn.Close()
					return
				}
			}
		}
	}()

	for {
		_, data, err := n.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg protocol.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			h.log.Warn("bad node message", "err", err)
			continue
		}
		h.handleNodeMessage(n, &msg)
	}
}

func (h *Hub) handleNodeMessage(n *nodeConn, msg *protocol.Message) {
	// Everything except hello requires a registered node.
	if msg.Type != protocol.TypeHello && n.id == 0 {
		h.log.Warn("message from node before hello", "type", msg.Type)
		return
	}
	switch msg.Type {
	case protocol.TypeHello:
		name := msg.NodeName
		if name == "" {
			name = "unnamed"
		}
		id, err := h.db.UpsertNode(n.ownerID, name, n.tokenID)
		if err != nil {
			h.log.Error("upsert node", "err", err)
			n.conn.Close()
			return
		}
		n.name, n.id = name, id
		h.mu.Lock()
		if old := h.nodes[id]; old != nil {
			h.log.Warn("replacing existing connection for node", "node", name)
			old.conn.Close()
		}
		h.nodes[id] = n
		h.mu.Unlock()
		h.log.Info("node hello", "name", name, "id", id, "owner", n.ownerID)

	case protocol.TypeSessions:
		h.mu.Lock()
		n.sessions = map[string]protocol.Session{}
		for sid, nid := range h.sessionNode { // drop stale routes for this node
			if nid == n.id {
				delete(h.sessionNode, sid)
			}
		}
		for _, s := range msg.Sessions {
			n.sessions[s.ID] = s
			h.sessionNode[s.ID] = n.id
		}
		h.mu.Unlock()
		h.syncSessionsToDB(n, msg.Sessions)

	case protocol.TypeOutput:
		h.broadcastOutput(msg.SessionID, msg.Data)

	case protocol.TypeBuffer:
		h.deliverBuffer(msg.SessionID, msg.ConnID, msg.Data)

	case protocol.TypeCreated:
		if msg.Session != nil {
			h.mu.Lock()
			n.sessions[msg.Session.ID] = *msg.Session
			h.sessionNode[msg.Session.ID] = n.id
			h.mu.Unlock()
			h.db.UpsertSession(database.SessionMeta{
				ID: msg.Session.ID, Node: n.name, NodeID: n.id, Name: msg.Session.Name,
				Cwd: msg.Session.Cwd, Shell: msg.Session.Shell, Pid: msg.Session.Pid,
				Status: "running", CreatedAt: msg.Session.CreatedAt, LastActive: msg.Session.LastActive,
			})
		}
		h.resolveCreate(msg.SessionID, nil)

	case protocol.TypeError:
		h.log.Warn("node error", "session", msg.SessionID, "err", msg.Error)
		h.resolveCreate(msg.SessionID, errors.New(msg.Error))

	case protocol.TypeDirList:
		h.mu.Lock()
		ch := h.pendingListDir[msg.ConnID]
		delete(h.pendingListDir, msg.ConnID)
		h.mu.Unlock()
		if ch != nil {
			ch <- msg
		}

	case protocol.TypeExited:
		h.mu.Lock()
		delete(n.sessions, msg.SessionID)
		delete(h.sessionNode, msg.SessionID)
		h.mu.Unlock()
		h.db.MarkSessionExited(msg.SessionID)
		h.notifySessionBrowsers(msg.SessionID, `{"type":"exited"}`)
		h.closeSessionBrowsers(msg.SessionID)

	default:
		h.log.Warn("unknown node message type", "type", msg.Type)
	}
}

// syncSessionsToDB upserts one node's live list and marks its sessions
// the node no longer knows about (e.g. died while offline) as exited.
func (h *Hub) syncSessionsToDB(n *nodeConn, live []protocol.Session) {
	alive := map[string]bool{}
	for _, s := range live {
		alive[s.ID] = true
		h.db.UpsertSession(database.SessionMeta{
			ID: s.ID, Node: n.name, NodeID: n.id, Name: s.Name, Cwd: s.Cwd, Shell: s.Shell,
			Pid: s.Pid, Status: "running", CreatedAt: s.CreatedAt, LastActive: s.LastActive,
		})
	}
	stored, err := h.db.ListSessionsForNode(n.id)
	if err != nil {
		h.log.Error("list sessions", "err", err)
		return
	}
	for _, s := range stored {
		if s.Status == "running" && !alive[s.ID] {
			h.db.MarkSessionExited(s.ID)
		}
	}
}

func (h *Hub) sendToNode(nodeID int64, msg *protocol.Message) error {
	h.mu.Lock()
	n := h.nodes[nodeID]
	h.mu.Unlock()
	if n == nil {
		return errors.New("PC node is offline")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	n.writeMu.Lock()
	defer n.writeMu.Unlock()
	n.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return n.conn.WriteMessage(websocket.TextMessage, data)
}

// nodeForSession resolves the routing table entry for a session.
func (h *Hub) nodeForSession(sessionID string) (int64, error) {
	h.mu.Lock()
	id, ok := h.sessionNode[sessionID]
	h.mu.Unlock()
	if !ok {
		return 0, errors.New("session not running on any connected node")
	}
	return id, nil
}

// ---- create/kill (called from the HTTP API) ----

func (h *Hub) resolveCreate(sessionID string, err error) {
	h.mu.Lock()
	ch := h.pendingCreate[sessionID]
	delete(h.pendingCreate, sessionID)
	h.mu.Unlock()
	if ch != nil {
		ch <- err
	}
}

// CreateSession asks one node to spawn a PTY and waits for the ack.
// fromSession, when set, makes the node inherit that session's live cwd.
func (h *Hub) CreateSession(nodeID int64, name, cwd, shell, fromSession string) (string, error) {
	id := newID()
	ack := make(chan error, 1)
	h.mu.Lock()
	h.pendingCreate[id] = ack
	h.mu.Unlock()

	err := h.sendToNode(nodeID, &protocol.Message{
		Type: protocol.TypeCreate, SessionID: id, Name: name, Cwd: cwd, Shell: shell,
		FromSession: fromSession,
	})
	if err != nil {
		h.resolveCreate(id, nil) // drain the pending entry
		return "", err
	}
	select {
	case err := <-ack:
		if err != nil {
			return "", err
		}
		return id, nil
	case <-time.After(10 * time.Second):
		h.resolveCreate(id, nil)
		return "", errors.New("timeout waiting for PC node")
	}
}

func (h *Hub) KillSession(id string) error {
	nodeID, err := h.nodeForSession(id)
	if err != nil {
		return err
	}
	return h.sendToNode(nodeID, &protocol.Message{Type: protocol.TypeKill, SessionID: id})
}

// ListDir asks a node for the subdirectories of path ("" = home) and
// waits for the reply. Returns the resolved absolute path and dir names.
func (h *Hub) ListDir(nodeID int64, path string) (string, []string, error) {
	reqID := newID()
	reply := make(chan *protocol.Message, 1)
	h.mu.Lock()
	h.pendingListDir[reqID] = reply
	h.mu.Unlock()
	drop := func() {
		h.mu.Lock()
		delete(h.pendingListDir, reqID)
		h.mu.Unlock()
	}

	if err := h.sendToNode(nodeID, &protocol.Message{Type: protocol.TypeListDir, ConnID: reqID, Cwd: path}); err != nil {
		drop()
		return "", nil, err
	}
	select {
	case msg := <-reply:
		if msg.Error != "" {
			return "", nil, errors.New(msg.Error)
		}
		return msg.Cwd, msg.Dirs, nil
	case <-time.After(10 * time.Second):
		drop()
		return "", nil, errors.New("timeout waiting for PC node")
	}
}

// OnlineNodes returns the set of currently connected node ids.
func (h *Hub) OnlineNodes() map[int64]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[int64]bool, len(h.nodes))
	for id := range h.nodes {
		out[id] = true
	}
	return out
}

// DisconnectToken closes every node connection that authenticated with
// the given token (called when the token is deleted).
func (h *Hub) DisconnectToken(tokenID int64) {
	h.mu.Lock()
	var conns []*websocket.Conn
	for _, n := range h.nodes {
		if n.tokenID == tokenID {
			conns = append(conns, n.conn)
		}
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

// ---- browser side ----

// ServeBrowser attaches an authenticated browser websocket to a session.
// Binary frames carry terminal bytes; text frames carry small JSON
// control messages ({"type":"resize",...} in, {"type":"exited"} out).
func (h *Hub) ServeBrowser(w http.ResponseWriter, r *http.Request, sessionID string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	nodeID, nodeErr := h.nodeForSession(sessionID)
	b := &browserConn{id: newID(), sessionID: sessionID, nodeID: nodeID, conn: conn}

	h.mu.Lock()
	if h.browsers[sessionID] == nil {
		h.browsers[sessionID] = map[*browserConn]bool{}
	}
	h.browsers[sessionID][b] = true
	h.mu.Unlock()

	// Ask the node for the scrollback buffer; output is held back until it
	// arrives so the replay and the live stream do not interleave.
	if nodeErr == nil {
		nodeErr = h.sendToNode(nodeID, &protocol.Message{Type: protocol.TypeAttach, SessionID: sessionID, ConnID: b.id})
	}
	if nodeErr != nil {
		b.writeText(fmt.Sprintf(`{"type":"error","error":%q}`, nodeErr.Error()))
	}

	h.readBrowser(b)

	h.mu.Lock()
	delete(h.browsers[sessionID], b)
	if len(h.browsers[sessionID]) == 0 {
		delete(h.browsers, sessionID)
	}
	h.mu.Unlock()
	conn.Close()
}

func (h *Hub) readBrowser(b *browserConn) {
	b.conn.SetReadLimit(1 << 20)
	b.conn.SetReadDeadline(time.Now().Add(pongWait))
	b.conn.SetPongHandler(func(string) error {
		b.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(pingPeriod)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-t.C:
				b.writeMu.Lock()
				b.conn.SetWriteDeadline(time.Now().Add(writeWait))
				err := b.conn.WriteMessage(websocket.PingMessage, nil)
				b.writeMu.Unlock()
				if err != nil {
					b.conn.Close()
					return
				}
			}
		}
	}()

	for {
		mt, data, err := b.conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			h.sendToNode(b.nodeID, &protocol.Message{Type: protocol.TypeInput, SessionID: b.sessionID, Data: data})
		case websocket.TextMessage:
			var ctl struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" {
				h.sendToNode(b.nodeID, &protocol.Message{Type: protocol.TypeResize, SessionID: b.sessionID, Cols: ctl.Cols, Rows: ctl.Rows})
			}
		}
	}
}

func (b *browserConn) writeBinary(data []byte) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	b.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return b.conn.WriteMessage(websocket.BinaryMessage, data)
}

func (b *browserConn) writeText(s string) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	b.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return b.conn.WriteMessage(websocket.TextMessage, []byte(s))
}

func (h *Hub) broadcastOutput(sessionID string, data []byte) {
	h.mu.Lock()
	targets := make([]*browserConn, 0, len(h.browsers[sessionID]))
	for b := range h.browsers[sessionID] {
		if b.ready { // pre-buffer output is already inside the replay buffer
			targets = append(targets, b)
		}
	}
	h.mu.Unlock()
	for _, b := range targets {
		if b.writeBinary(data) != nil {
			b.conn.Close()
		}
	}
}

func (h *Hub) deliverBuffer(sessionID, connID string, data []byte) {
	h.mu.Lock()
	var target *browserConn
	for b := range h.browsers[sessionID] {
		if b.id == connID {
			target = b
			b.ready = true
			break
		}
	}
	h.mu.Unlock()
	if target != nil {
		if len(data) > 0 {
			target.writeBinary(data)
		}
	}
}

func (h *Hub) notifySessionBrowsers(sessionID, jsonMsg string) {
	h.mu.Lock()
	targets := make([]*browserConn, 0, len(h.browsers[sessionID]))
	for b := range h.browsers[sessionID] {
		targets = append(targets, b)
	}
	h.mu.Unlock()
	for _, b := range targets {
		b.writeText(jsonMsg)
	}
}

func (h *Hub) closeSessionBrowsers(sessionID string) {
	h.mu.Lock()
	targets := make([]*browserConn, 0, len(h.browsers[sessionID]))
	for b := range h.browsers[sessionID] {
		targets = append(targets, b)
	}
	h.mu.Unlock()
	for _, b := range targets {
		b.conn.Close()
	}
}

