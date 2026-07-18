# AgentDock

Multi-user, multi-device remote control for AI coding agents.

Run Cursor CLI, Codex CLI, Claude Code or any other CLI agent on your
own PC (where your repos, environment and subscriptions live), then
attach to those terminal sessions from any browser — including your
phone — through a small public server. Sessions survive browser
disconnects, tmux-style: close your phone at night, reattach tomorrow.
Invite friends: each user connects their own PCs and can share them
with other users.

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
cp .env.example .env   # defaults are fine behind an HTTPS proxy
docker compose up -d
```

No credentials to configure: open the web UI and **register — the
first account becomes the admin**. Anyone registering after that is
`pending` until the admin approves them on the Admin page.

The server listens on `:8080`. Put it behind an HTTPS reverse proxy
(Caddy, nginx, Traefik) — websockets must be proxied too. Example Caddy:

```
example.com {
    reverse_proxy localhost:8080
}
```

For plain-http testing only, set `AGENTDOCK_COOKIE_SECURE=false`.

<details>
<summary>Deploying to a host without a registry (docker save/load)</summary>

If the server host can't pull from a registry, build locally and ship
the image over SSH:

```bash
docker build -t agentdock:latest .
docker save agentdock:latest | gzip | ssh myserver 'gunzip | docker load'
ssh myserver 'mkdir -p ~/app/agentdock'
scp docker-compose.yml .env myserver:~/app/agentdock/
ssh myserver 'cd ~/app/agentdock && docker compose up -d'
```

Change `build: .` to `image: agentdock:latest` in the remote
`docker-compose.yml`, bind the port to loopback only
(`"127.0.0.1:8080:8080"`) and point your reverse proxy at it —
the container should never be reachable directly from the internet.

</details>

Verify the deployment end to end:

```bash
curl -s https://example.com/api/state          # 401 = auth is on
```

### 2. Client (your PC)

Sign in to the web UI and use the **Connect a PC** card on the
dashboard to generate your personal node token (`adk_...`). The token
is shown once; the server only stores a hash. A client connecting with
it automatically belongs to your account.

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
  --token adk_XXXXXXXX... \
  --name office-pc
```

It reconnects automatically with backoff; sessions survive network
blips. For a permanent setup, run it as a systemd user service so it
starts on boot and survives logouts:

```ini
# ~/.config/systemd/user/agent-client.service
[Unit]
Description=AgentDock client (PC node)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%h/.local/bin/agent-client connect --server https://example.com --token adk_XXXX --name office-pc
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

```bash
cp bin/agent-client ~/.local/bin/
systemctl --user daemon-reload
systemctl --user enable --now agent-client
loginctl enable-linger $USER          # keep it running after logout
journalctl --user -u agent-client -f  # watch logs
```

Don't run the client in Docker: sessions would see the container's
filesystem and environment instead of your real repos, shells and
agent-CLI credentials, which defeats the purpose. The binary is
static and dependency-free; systemd is all it needs.

### 3. Browser

Open `https://example.com`, sign in, and:

1. See `office-pc` online on the dashboard.
2. Click **New Session** on its card, **Browse…** to pick a directory
   graphically, or tap a saved directory for a one-tap session.
3. In the terminal: `cd ~/project && cursor` (or `codex`, `claude`, ...).
4. Close the browser whenever. The session keeps running on your PC.
5. Reopen later — the terminal is restored with its recent scrollback.

## Users, PCs and sharing

- The first registered account is the **admin**; later registrations
  need approval on the Admin page before they can sign in.
- A PC (node) belongs to the user whose token it connects with, and is
  visible to its owner and to admins.
- Every user's uid is shown next to their name (`alice #1`). Anyone
  who can access a node can **share** it with another user by uid from
  the node card — access is all-or-nothing (see, open and kill every
  session on that node, and share it onward). Only the node's owner or
  an admin can revoke a share.
- Admins can also grant any node to any user from the Admin page.

## Configuration (server)

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `AGENTDOCK_ADDR` | no | `:8080` | Listen address |
| `AGENTDOCK_DB` | no | `./data/agentdock.db` (`/data/agentdock.db` in Docker) | SQLite path |
| `AGENTDOCK_JWT_SECRET` | no | auto-generated, persisted in SQLite | JWT signing key |
| `AGENTDOCK_COOKIE_SECURE` | no | `true` | Set `false` only for plain-http testing |

Client flags (env fallbacks in parentheses): `--server`
(`AGENTDOCK_SERVER`), `--token` (`AGENTDOCK_NODE_TOKEN`), `--name`
(hostname by default).

## Security model

- Passwords are bcrypt-hashed; no default or env-injected credentials
  exist — the first web registration creates the admin.
- New accounts are inactive until an admin approves them; approval is
  re-checked on every request, so revocations apply immediately.
- Browser sessions use a signed JWT in an `HttpOnly` `SameSite=Lax`
  cookie (7-day expiry); the terminal websocket is authenticated by the
  same cookie plus an Origin check, and every session/browse/terminal
  request re-checks node access.
- Each PC node authenticates with its owner's personal bearer token
  (`adk_...`, shown once, stored as a SHA-256 hash, revocable by
  regenerating); wrong tokens get a 401 before any upgrade.
- State-changing requests pass a same-origin check (CSRF guard).
- The PC never listens; it only dials out over `wss`.

## Development

```bash
make test                      # go vet + all tests (incl. e2e PTY round-trip)
make dev-server                # server on :8080; register the admin in the browser
cd web && npm run dev          # Vite dev server on :5173, proxies /api
go run ./client/cmd/agent-client connect \
  --server http://localhost:8080 --token <token from the dashboard>
```

`make all` builds everything into `bin/` with the frontend embedded in
`agent-server`.

## Design notes & future-proofing

- **Sessions live in the `agent-client` process.** Like a tmux server,
  if the client (or the PC) restarts, sessions end; the dashboard then
  shows them as `exited`. Scrollback replay is a 256 KB in-memory ring
  per session.
- One flat JSON message envelope (`internal/protocol`) multiplexes all
  of a node's sessions over its single websocket; the hub routes each
  session to the right node.
- Node access is deliberately all-or-nothing per node; per-session
  permissions and agent-type metadata remain extension points.
