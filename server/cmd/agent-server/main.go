// agent-server: public-facing server. Serves the web UI, authenticates
// the user, accepts the agent-client connection and relays terminals.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"agentdock/server/internal/api"
	"agentdock/server/internal/auth"
	"agentdock/server/internal/database"
	"agentdock/server/internal/hub"
	"agentdock/server/internal/webui"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	addr := env("AGENTDOCK_ADDR", ":8080")
	dbPath := env("AGENTDOCK_DB", "./data/agentdock.db")
	secureCookie := env("AGENTDOCK_COOKIE_SECURE", "true") == "true"

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Error("create data dir", "err", err)
		os.Exit(1)
	}
	db, err := database.Open(dbPath)
	if err != nil {
		log.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	// JWT secret: env wins, otherwise generate once and persist in SQLite.
	secret := os.Getenv("AGENTDOCK_JWT_SECRET")
	if secret == "" {
		secret, err = db.GetSetting("jwt_secret")
		if err == nil && secret == "" {
			b := make([]byte, 32)
			rand.Read(b)
			secret = hex.EncodeToString(b)
			err = db.SetSetting("jwt_secret", secret)
		}
		if err != nil {
			log.Error("jwt secret", "err", err)
			os.Exit(1)
		}
	}

	// No bootstrap: the first user to register via the web UI becomes
	// the active admin; everyone after needs admin approval.
	if n, err := db.UserCount(); err == nil && n == 0 {
		log.Info("no users yet: the first account registered in the web UI becomes admin")
	}

	a := auth.New([]byte(secret), 7*24*time.Hour, secureCookie)
	h := hub.New(db, log)
	srv := &api.Server{DB: db, Auth: a, Hub: h, Log: log, WebFS: webui.FS()}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("agent-server listening", "addr", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)
}