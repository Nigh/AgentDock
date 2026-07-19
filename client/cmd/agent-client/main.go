// agent-client: runs on the user's PC, dials out to agent-server and
// hosts tmux-like PTY sessions. Never listens on any port.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"agentdock/client/internal/session"
	"agentdock/client/internal/ws"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "connect" {
		fmt.Fprintf(os.Stderr, `agent-client - AgentDock PC node

Usage:
  agent-client connect --server https://example.com --token adk_XXXX [--name office-pc]

The token is your personal node token, generated on the web UI
dashboard; the node automatically belongs to your account. Sessions
are created and managed from the web UI. They live as long as this
process runs; browsers can attach and detach freely.
`)
		os.Exit(2)
	}

	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	server := fs.String("server", os.Getenv("AGENTDOCK_SERVER"), "server base URL, e.g. https://example.com")
	token := fs.String("token", os.Getenv("AGENTDOCK_NODE_TOKEN"), "personal node token generated on the web UI dashboard")
	name := fs.String("name", defaultNodeName(), "node display name")
	fs.Parse(os.Args[2:])

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if *server == "" || *token == "" {
		log.Error("--server and --token are required (or AGENTDOCK_SERVER / AGENTDOCK_NODE_TOKEN)")
		os.Exit(1)
	}

	mgr := session.NewManager(log)
	client := ws.New(*server, *token, *name, mgr, log)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	err := client.Run(ctx)
	if ctx.Err() != nil {
		log.Info("shutting down, killing sessions")
		mgr.Shutdown()
		return
	}
	if err != nil {
		log.Error("connection failed", "err", err)
		os.Exit(1)
	}
}

func defaultNodeName() string {
	if n := os.Getenv("AGENTDOCK_NODE_NAME"); n != "" {
		return n
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "pc"
}
