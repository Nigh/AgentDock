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
	name     string
	conn     *websocket.Conn
	writeMu  sync.Mutex
	sessions map[string]protocol.Session
}

type browserConn struct {
	id        string
	sessionID string
	conn      *websocket.Conn
	writeMu   sync.Mutex
	ready     bool // becomes true once the scrollback buffer arrived
}

type Hub struct {
	db  *database.DB
	log *slog.Logger

	mu sync.Mutex
	// ponytail: single node for the MVP; becomes map[nodeID]*nodeConn for multi-PC
	node           *nodeConn
	browsers       map[string]map[*browserConn]bool  // sessionID -> attached browsers
	pendingCreate  map[string]chan error             // sessionID -> create ack
	pendingListDir map[string]chan *protocol.Message // request ConnID -> dirlist reply
}

func New(db *database.DB, log *slog.Logger) *Hub {
	return &Hub{
		db:             db,
		log:            log,
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

// ServeNode upgrades and runs the websocket for an agent-client.
func (h *Hub) ServeNode(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Error("node upgrade failed", "err", err)
		return
	}
	n := &nodeConn{conn: conn, sessions: map[string]protocol.Session{}}

	h.mu.Lock()
	if h.node != nil {
		h.log.Warn("replacing existing node connection")
		h.node.conn.Close()
	}
	h.node = n
	h.mu.Unlock()

	h.log.Info("node connected", "remote", r.RemoteAddr)
	h.readNode(n)

	h.mu.Lock()
	if h.node == n {
		h.node = nil
	}
	h.mu.Unlock()
	conn.Close()
	h.log.Info("node disconnected")
	h.notifyAllBrowsers(`{"type":"node_offline"}`)
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
	switch msg.Type {
	case protocol.TypeHello:
		n.name = msg.NodeName
		h.log.Info("node hello", "name", n.name)

	case protocol.TypeSessions:
		h.mu.Lock()
		n.sessions = map[string]protocol.Session{}
		for _, s := range msg.Sessions {
			n.sessions[s.ID] = s
		}
		h.mu.Unlock()
		h.syncSessionsToDB(n.name, msg.Sessions)

	case protocol.TypeOutput:
		h.broadcastOutput(msg.SessionID, msg.Data)

	case protocol.TypeBuffer:
		h.deliverBuffer(msg.SessionID, msg.ConnID, msg.Data)

	case protocol.TypeCreated:
		if msg.Session != nil {
			h.mu.Lock()
			n.sessions[msg.Session.ID] = *msg.Session
			h.mu.Unlock()
			h.db.UpsertSession(database.SessionMeta{
				ID: msg.Session.ID, Node: n.name, Name: msg.Session.Name,
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
		h.mu.Unlock()
		h.db.MarkSessionExited(msg.SessionID)
		h.notifySessionBrowsers(msg.SessionID, `{"type":"exited"}`)
		h.closeSessionBrowsers(msg.SessionID)

	default:
		h.log.Warn("unknown node message type", "type", msg.Type)
	}
}

// syncSessionsToDB upserts the live list and marks anything the node no
// longer knows about (e.g. died while offline) as exited.
func (h *Hub) syncSessionsToDB(node string, live []protocol.Session) {
	alive := map[string]bool{}
	for _, s := range live {
		alive[s.ID] = true
		h.db.UpsertSession(database.SessionMeta{
			ID: s.ID, Node: node, Name: s.Name, Cwd: s.Cwd, Shell: s.Shell,
			Pid: s.Pid, Status: "running", CreatedAt: s.CreatedAt, LastActive: s.LastActive,
		})
	}
	stored, err := h.db.ListSessions()
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

func (h *Hub) sendToNode(msg *protocol.Message) error {
	h.mu.Lock()
	n := h.node
	h.mu.Unlock()
	if n == nil {
		return errors.New("no PC node connected")
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

// CreateSession asks the node to spawn a PTY and waits for the ack.
// fromSession, when set, makes the node inherit that session's live cwd.
func (h *Hub) CreateSession(name, cwd, shell, fromSession string) (string, error) {
	id := newID()
	ack := make(chan error, 1)
	h.mu.Lock()
	h.pendingCreate[id] = ack
	h.mu.Unlock()

	err := h.sendToNode(&protocol.Message{
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
	return h.sendToNode(&protocol.Message{Type: protocol.TypeKill, SessionID: id})
}

// ListDir asks the node for the subdirectories of path ("" = home) and
// waits for the reply. Returns the resolved absolute path and dir names.
func (h *Hub) ListDir(path string) (string, []string, error) {
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

	if err := h.sendToNode(&protocol.Message{Type: protocol.TypeListDir, ConnID: reqID, Cwd: path}); err != nil {
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

// NodeStatus returns (name, connected) plus the live session list.
func (h *Hub) NodeStatus() (string, bool, []protocol.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.node == nil {
		return "", false, nil
	}
	out := make([]protocol.Session, 0, len(h.node.sessions))
	for _, s := range h.node.sessions {
		out = append(out, s)
	}
	return h.node.name, true, out
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
	b := &browserConn{id: newID(), sessionID: sessionID, conn: conn}

	h.mu.Lock()
	if h.browsers[sessionID] == nil {
		h.browsers[sessionID] = map[*browserConn]bool{}
	}
	h.browsers[sessionID][b] = true
	h.mu.Unlock()

	// Ask the node for the scrollback buffer; output is held back until it
	// arrives so the replay and the live stream do not interleave.
	if err := h.sendToNode(&protocol.Message{Type: protocol.TypeAttach, SessionID: sessionID, ConnID: b.id}); err != nil {
		b.writeText(fmt.Sprintf(`{"type":"error","error":%q}`, err.Error()))
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
			h.sendToNode(&protocol.Message{Type: protocol.TypeInput, SessionID: b.sessionID, Data: data})
		case websocket.TextMessage:
			var ctl struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" {
				h.sendToNode(&protocol.Message{Type: protocol.TypeResize, SessionID: b.sessionID, Cols: ctl.Cols, Rows: ctl.Rows})
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

func (h *Hub) notifyAllBrowsers(jsonMsg string) {
	h.mu.Lock()
	targets := []*browserConn{}
	for _, set := range h.browsers {
		for b := range set {
			targets = append(targets, b)
		}
	}
	h.mu.Unlock()
	for _, b := range targets {
		b.writeText(jsonMsg)
	}
}
