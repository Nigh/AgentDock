# AgentDock

Single-user, multi-device remote control for AI coding agents.

Run Cursor CLI, Codex CLI, Claude Code or any other CLI agent on your
own PC (where your repos, environment and subscriptions live), then
attach to those terminal sessions from any browser — including your
phone — through a small public server. Sessions survive browser
disconnects, tmux-style: close your phone at night, reattach tomorrow.

```
 phone / laptop browser                 your PC (office / home)
 ┌──────────────────┐                  ┌─────────────────────────┐
 │  Web UI (xterm)  │   wss            │  agent-client           │
 └────────┬─────────┘   ◄──────┐       │   ├─ PTY ── bash ── cursor
          │ wss               │       │   ├─ PTY ── bash ── codex
 ┌────────▼─────────┐         │  wss  │   └─ PTY ── bash ── ...
 │   agent-server   │ ◄───────┴───────┤  (dials OUT, no open port)
 │  (public, Docker)│                  └─────────────────────────┘
 └──────────────────┘
```

Key property: the PC **dials out** to the server. No port is ever
opened on your PC.

## Repository layout

```
internal/protocol/   shared websocket message envelope
server/
  cmd/agent-server/  public server binary
  internal/
    api/             HTTP routes (auth, sessions, directories, ws)
    auth/            bcrypt + JWT cookie sessions
    database/        SQLite (users, directories, session metadata)
    hub/             node registry + browser<->node relay
    webui/           embedded built frontend
  e2e/               end-to-end test (server + client + fake browser)
client/
  cmd/agent-client/  PC node binary
  internal/
    session/         tmux-like PTY sessions + scrollback ring buffer
    ws/              outbound websocket with auto-reconnect
web/                 Svelte 5 + Vite + Tailwind + xterm.js frontend
```

## Quick start

### 1. Server (public machine)

```bash
cp .env.example .env
# edit .env:
#   AGENTDOCK_NODE_TOKEN=$(openssl rand -hex 24)
#   AGENTDOCK_ADMIN_USER=you
#   AGENTDOCK_ADMIN_PASSWORD=a-strong-password
docker compose up -d
```

The server listens on `:8080`. Put it behind an HTTPS reverse proxy
(Caddy, nginx, Traefik) — websockets must be proxied too. Example Caddy:

```
example.com {
    reverse_proxy localhost:8080
}
```

For plain-http testing only, set `AGENTDOCK_COOKIE_SECURE=false`.

### 2. Client (your PC)

Build (or copy the binary out of the Docker image:
`docker cp agentdock-server:/usr/local/bin/agent-client .`):

```bash
make client        # -> bin/agent-client
# or cross compile: make client-all
```

Connect:

```bash
agent-client connect \
  --server https://example.com \
  --token <AGENTDOCK_NODE_TOKEN> \
  --name office-pc
```

Keep it running with systemd, tmux, or nohup. It reconnects
automatically with backoff; sessions survive network blips.

### 3. Browser

Open `https://example.com`, log in, and:

1. See `office-pc` online on the dashboard.
2. Click **New Session** (or tap a saved directory for a one-tap session).
3. In the terminal: `cd ~/project && cursor` (or `codex`, `claude`, ...).
4. Close the browser whenever. The session keeps running on your PC.
5. Reopen later — the terminal is restored with its recent scrollback.

## Configuration (server)

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `AGENTDOCK_NODE_TOKEN` | yes | – | Shared secret for the PC node (min 16 chars). Generate: `openssl rand -hex 24` |
| `AGENTDOCK_ADMIN_USER` | first run | – | Bootstrap username (ignored once a user exists) |
| `AGENTDOCK_ADMIN_PASSWORD` | first run | – | Bootstrap password (min 8 chars, stored bcrypt-hashed) |
| `AGENTDOCK_ADDR` | no | `:8080` | Listen address |
| `AGENTDOCK_DB` | no | `./data/agentdock.db` (`/data/agentdock.db` in Docker) | SQLite path |
| `AGENTDOCK_JWT_SECRET` | no | auto-generated, persisted in SQLite | JWT signing key |
| `AGENTDOCK_COOKIE_SECURE` | no | `true` | Set `false` only for plain-http testing |

Client flags (env fallbacks in parentheses): `--server`
(`AGENTDOCK_SERVER`), `--token` (`AGENTDOCK_NODE_TOKEN`), `--name`
(hostname by default).

## Security model

- Passwords are bcrypt-hashed; no default credentials exist — the
  server refuses to start without a bootstrapped user.
- Browser sessions use a signed JWT in an `HttpOnly` `SameSite=Lax`
  cookie (7-day expiry); the terminal websocket is authenticated by the
  same cookie plus an Origin check.
- The PC node authenticates with a bearer token compared in constant
  time; wrong tokens get a 401 before any upgrade.
- State-changing requests pass a same-origin check (CSRF guard).
- The PC never listens; it only dials out over `wss`.

## Development

```bash
make test                      # go vet + all tests (incl. e2e PTY round-trip)
make dev-server                # server on :8080 (admin/devpassword)
cd web && npm run dev          # Vite dev server on :5173, proxies /api
go run ./client/cmd/agent-client connect \
  --server http://localhost:8080 --token dev-token-not-for-prod
```

`make all` builds everything into `bin/` with the frontend embedded in
`agent-server`.

## Design notes & future-proofing

- **Sessions live in the `agent-client` process.** Like a tmux server,
  if the client (or the PC) restarts, sessions end; the dashboard then
  shows them as `exited`. Scrollback replay is a 256 KB in-memory ring
  per session.
- One flat JSON message envelope (`internal/protocol`) multiplexes all
  sessions over a single node websocket.
- The schema and hub already carry `node` and `role` fields; multi-PC,
  multi-user, and agent-type metadata are deliberate extension points,
  not implemented features.
