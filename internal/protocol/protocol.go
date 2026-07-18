// Package protocol defines the JSON messages exchanged between
// agent-server and agent-client over a single multiplexed websocket.
package protocol

import "time"

// Message types, node <-> server.
const (
	// node -> server
	TypeHello    = "hello"    // node announces itself, carries NodeName
	TypeSessions = "sessions" // full session list (after hello and on any change)
	TypeOutput   = "output"   // PTY output, Data
	TypeBuffer   = "buffer"   // scrollback replay for one browser conn (ConnID)
	TypeCreated  = "created"  // session created OK
	TypeExited   = "exited"   // session process ended
	TypeError    = "error"    // request failed, Error set
	TypeDirList  = "dirlist"  // reply to listdir: ConnID, Cwd (abs), Dirs (or Error)

	// server -> node
	TypeCreate  = "create"  // create session: SessionID, Name, Cwd, Shell; FromSession = inherit its live cwd
	TypeInput   = "input"   // keyboard input: SessionID, Data
	TypeResize  = "resize"  // SessionID, Cols, Rows
	TypeKill    = "kill"    // SessionID
	TypeAttach  = "attach"  // SessionID, ConnID: node replies with TypeBuffer
	TypeListDir = "listdir" // list subdirectories: ConnID (request id), Cwd ("" = home)
)

// Session is the metadata for one PTY session, mirroring the spec's
// Session{id,name,cwd,shell,pid,created_at,last_active} structure.
type Session struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Cwd        string    `json:"cwd"`
	Shell      string    `json:"shell"`
	Pid        int       `json:"pid"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
}

// Message is the single envelope for every frame. Unused fields are
// omitted on the wire; one flat struct keeps both ends trivial.
type Message struct {
	Type      string    `json:"type"`
	SessionID string    `json:"session_id,omitempty"`
	ConnID    string    `json:"conn_id,omitempty"`
	NodeName  string    `json:"node_name,omitempty"`
	Data      []byte    `json:"data,omitempty"` // base64 in JSON
	Cols      uint16    `json:"cols,omitempty"`
	Rows      uint16    `json:"rows,omitempty"`
	Name      string    `json:"name,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	Shell     string    `json:"shell,omitempty"`
	// FromSession (on create): spawn in that session's live working dir.
	FromSession string   `json:"from_session,omitempty"`
	Dirs        []string `json:"dirs,omitempty"`
	Sessions  []Session `json:"sessions,omitempty"`
	Session   *Session  `json:"session,omitempty"`
	Error     string    `json:"error,omitempty"`
}
