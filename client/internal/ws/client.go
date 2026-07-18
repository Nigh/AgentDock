// Package ws maintains the outbound websocket to agent-server and
// dispatches protocol messages to the session manager. The PC dials
// out; no inbound port is ever opened.
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"agentdock/client/internal/session"
	"agentdock/internal/protocol"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second
)

type Client struct {
	serverURL string
	token     string
	nodeName  string
	log       *slog.Logger
	mgr       *session.Manager

	mu   sync.Mutex
	conn *websocket.Conn
}

func New(serverURL, token, nodeName string, mgr *session.Manager, log *slog.Logger) *Client {
	c := &Client{serverURL: serverURL, token: token, nodeName: nodeName, mgr: mgr, log: log}
	mgr.OnOutput = func(id string, data []byte) {
		c.send(&protocol.Message{Type: protocol.TypeOutput, SessionID: id, Data: data})
	}
	mgr.OnExit = func(id string) {
		c.send(&protocol.Message{Type: protocol.TypeExited, SessionID: id})
		c.sendSessionList()
	}
	return c
}

func (c *Client) wsURL() (string, error) {
	u, err := url.Parse(c.serverURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/node/ws"
	return u.String(), nil
}

// Run connects and reconnects with backoff until ctx is cancelled.
// Sessions keep running across reconnects.
func (c *Client) Run(ctx context.Context) error {
	wsURL, err := c.wsURL()
	if err != nil {
		return err
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := c.runOnce(ctx, wsURL)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.log.Warn("disconnected from server", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) runOnce(ctx context.Context, wsURL string) error {
	header := http.Header{"Authorization": {"Bearer " + c.token}}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			c.log.Error("server rejected node token; check --token")
		}
		return err
	}
	defer conn.Close()

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	c.log.Info("connected to server", "url", wsURL)
	c.send(&protocol.Message{Type: protocol.TypeHello, NodeName: c.nodeName})
	c.sendSessionList()

	conn.SetReadLimit(4 << 20)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(pingPeriod)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				conn.Close()
				return
			case <-t.C:
				c.mu.Lock()
				conn.SetWriteDeadline(time.Now().Add(writeWait))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				c.mu.Unlock()
				if err != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var msg protocol.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			c.log.Warn("bad message from server", "err", err)
			continue
		}
		c.handle(&msg)
	}
}

func (c *Client) handle(msg *protocol.Message) {
	switch msg.Type {
	case protocol.TypeCreate:
		cwd := msg.Cwd
		if msg.FromSession != "" {
			live, err := c.mgr.LiveCwd(msg.FromSession)
			if err != nil {
				c.send(&protocol.Message{Type: protocol.TypeError, SessionID: msg.SessionID, Error: err.Error()})
				return
			}
			cwd = live
		}
		s, err := c.mgr.Create(msg.SessionID, msg.Name, cwd, msg.Shell)
		if err != nil {
			c.send(&protocol.Message{Type: protocol.TypeError, SessionID: msg.SessionID, Error: err.Error()})
			return
		}
		c.send(&protocol.Message{Type: protocol.TypeCreated, SessionID: s.ID, Session: &s.Session})
		c.sendSessionList()

	case protocol.TypeInput:
		if err := c.mgr.Write(msg.SessionID, msg.Data); err != nil {
			c.log.Warn("input", "err", err)
		}

	case protocol.TypeResize:
		c.mgr.Resize(msg.SessionID, msg.Cols, msg.Rows)

	case protocol.TypeAttach:
		buf, err := c.mgr.Buffer(msg.SessionID)
		if err != nil {
			c.send(&protocol.Message{Type: protocol.TypeError, SessionID: msg.SessionID, ConnID: msg.ConnID, Error: err.Error()})
			return
		}
		c.send(&protocol.Message{Type: protocol.TypeBuffer, SessionID: msg.SessionID, ConnID: msg.ConnID, Data: buf})

	case protocol.TypeKill:
		if err := c.mgr.Kill(msg.SessionID); err != nil {
			c.log.Warn("kill", "err", err)
		}

	case protocol.TypeListDir:
		c.handleListDir(msg)

	default:
		c.log.Warn("unknown message from server", "type", msg.Type)
	}
}

// handleListDir replies with the absolute path and its subdirectories,
// so the browser can navigate the PC's filesystem graphically.
func (c *Client) handleListDir(msg *protocol.Message) {
	dir := msg.Cwd
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
			abs = resolved
		}
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		c.send(&protocol.Message{Type: protocol.TypeDirList, ConnID: msg.ConnID, Error: err.Error()})
		return
	}
	dirs := []string{}
	for _, e := range entries { // ReadDir sorts by name
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		} else if e.Type()&os.ModeSymlink != 0 {
			if st, serr := os.Stat(filepath.Join(abs, e.Name())); serr == nil && st.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
	}
	c.send(&protocol.Message{Type: protocol.TypeDirList, ConnID: msg.ConnID, Cwd: abs, Dirs: dirs})
}

func (c *Client) sendSessionList() {
	c.send(&protocol.Message{Type: protocol.TypeSessions, Sessions: c.mgr.List()})
}

func (c *Client) send(msg *protocol.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return // offline; sessions keep running, server resyncs on reconnect
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		c.conn.Close()
	}
}
