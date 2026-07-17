// Package client exposes the agent-client node as a library: a session
// manager plus the outbound websocket. Used by cmd/agent-client and tests.
package client

import (
	"context"
	"log/slog"

	"agentdock/client/internal/session"
	"agentdock/client/internal/ws"
)

type Node struct {
	mgr *session.Manager
	ws  *ws.Client
}

func New(serverURL, token, nodeName string, log *slog.Logger) *Node {
	mgr := session.NewManager(log)
	return &Node{mgr: mgr, ws: ws.New(serverURL, token, nodeName, mgr, log)}
}

// Run connects (and reconnects) to the server until ctx is cancelled.
func (n *Node) Run(ctx context.Context) error { return n.ws.Run(ctx) }

// Shutdown kills all local sessions.
func (n *Node) Shutdown() { n.mgr.Shutdown() }
