# AGENTS.md

Guidance for AI coding agents working on this repository.

> **Standing rule: keep this file in sync.** After ANY edit to this
> project (code, config, docs, build, deployment), update AGENTS.md in
> the same change so it fully matches the current state of the project.
> If you add/remove/move a package, endpoint, message type, env var,
> make target or dependency, reflect it here before you finish.

## What this project is

AgentDock: single-user, multi-device remote control for AI coding
agents. Two Go binaries plus a web frontend:

- **agent-server** — public server (Docker). Serves the embedded web
  UI, authenticates the user, accepts the PC node connection, relays
  terminal traffic between browsers and the node.
- **agent-client** — runs on the user's PC. Dials OUT to the server
  (never listens), hosts tmux-like PTY sessions that survive browser
  disconnects. Sessions die with the client process.

## Layout (one Go module, `agentdock`)

```
internal/protocol/            shared ws message envelope (flat Message struct)
server/
  cmd/agent-server/main.go    env config, bootstrap admin, graceful shutdown
  internal/api/               HTTP routes, origin/CSRF check, static SPA
  internal/auth/              bcrypt, JWT HttpOnly cookie, middleware
  internal/database/          SQLite via modernc.org/sqlite (CGO-free);
                              users, directories, sessions, settings
  internal/hub/               node registry + browser<->node relay
  internal/webui/             go:embed of built frontend (dist/)
  e2e/                        end-to-end test: server + client + fake browser
client/
  client.go                   facade (Node) used by cmd and tests
  cmd/agent-client/main.go    `connect` subcommand only
  internal/session/           PTY (creack/pty) manager + 256KB scrollback ring
  internal/ws/                outbound ws, auto-reconnect with backoff
web/                          Svelte 5 (runes) + Vite + Tailwind 4 + xterm.js
                              (WebGL renderer addon, DOM fallback)
  src/App.svelte              hash routing: '' dashboard, '#/session/<id>',
                              '#/browse?path=...' directory browser
  src/lib/                    Login, Dashboard, Terminal, Browse, api.js
  vite.config.js              outDir -> server/internal/webui/dist
```

## Protocol (internal/protocol)

One flat JSON `Message` over a single node websocket, all sessions
multiplexed. Types: node->server `hello sessions output buffer created
exited error dirlist`; server->node `create input resize kill attach
listdir`. `create` may carry `from_session` (spawn in that session's
live cwd, read from /proc/<pid>/cwd); `listdir`/`dirlist` power the
web directory browser (request keyed by `ConnID`).
Browser<->server terminal ws: binary frames = raw terminal bytes, text
frames = JSON control (`resize` in; `exited`, `node_offline`, `error`
out). Attach replay ordering: hub holds a browser's output until its
`buffer` (keyed by `ConnID`) arrives, preventing duplicated bytes.

## HTTP API (all under /api, cookie-authed except login and node ws)

```
POST /api/login | POST /api/logout | GET /api/me
GET  /api/state                     node status + sessions + directories
GET|POST /api/directories, DELETE /api/directories/{id}
GET  /api/browse?path=              subdirs of a path on the PC ("" = home)
POST /api/sessions                  waits (10s) for node ack; optional
                                    from_session inherits its live cwd
DELETE /api/sessions/{id}
GET  /api/sessions/{id}/ws          browser terminal (cookie + Origin check)
GET  /api/node/ws                   node (Bearer AGENTDOCK_NODE_TOKEN)
```

## Env vars (server)

`AGENTDOCK_NODE_TOKEN` (required, >=16 chars), `AGENTDOCK_ADMIN_USER` /
`AGENTDOCK_ADMIN_PASSWORD` (first run only; no default credentials —
server refuses to start without a user), `AGENTDOCK_ADDR` (:8080),
`AGENTDOCK_DB` (./data/agentdock.db; /data/agentdock.db in Docker),
`AGENTDOCK_JWT_SECRET` (auto-generated, persisted in SQLite settings),
`AGENTDOCK_COOKIE_SECURE` (true). Client: `--server/--token/--name`
with env fallbacks `AGENTDOCK_SERVER` / `AGENTDOCK_NODE_TOKEN`.

## Build & test

```
make test        # go vet ./... && go test ./...  (must pass before commit)
make all         # web build (embeds into server) + both binaries -> bin/
make dev-server  # local server :8080, admin/devpassword, insecure cookies
cd web && npm run dev   # Vite :5173, proxies /api with ws
```

go.mod requires go >= 1.25 (toolchain auto-downloads). Frontend build
output is git-ignored except the placeholder
`server/internal/webui/dist/index.html`; never commit built assets.
Docker build: `Dockerfile` (3-stage: node -> go -> alpine, both
binaries in the image), `docker-compose.yml` reads `.env`.

## Tests

- `server/e2e/e2e_test.go` — the main check: real PTY round-trip
  (login, create session, echo, detach, re-attach scrollback replay,
  kill, exited notice), node-token rejection, and directory browsing
  (`/api/browse` + `from_session` cwd inheritance).
- `server/internal/auth/auth_test.go` — bcrypt + JWT.
- `client/internal/session/ring_test.go` — scrollback ring buffer.

Any change to hub/relay/session/protocol logic must keep these green;
extend the e2e test when adding protocol behavior.

## Conventions & constraints

- Lazy-senior style (see workspace ponytail rule): stdlib first, no
  new deps or abstractions without need, `ponytail:` comments mark
  intentional simplifications and their upgrade path.
- Security invariants (do not weaken): PC dials out only; bcrypt
  passwords; no default credentials; constant-time node-token compare;
  HttpOnly SameSite=Lax cookie; Origin check on mutating requests and
  the browser ws.
- Future extension points that exist but must stay UNimplemented for
  now: multi-user (`users.role`), multi-PC (`sessions.node`, hub's
  single `node` field), agent-type metadata.
- Server tree may not import `client/internal/...` — use the
  `agentdock/client` facade (that's why it exists).

## Deployment state (reference)

Production: server runs via docker compose behind an HTTPS reverse
proxy bound to loopback, image shipped via docker save/load (see
README). Client runs on the dev PC as a systemd user service
(`~/.config/systemd/user/agent-client.service`, linger enabled).
Secrets and real hostnames (node token, passwords, domains) live only
in the server's `.env` and the service unit — never commit them, and
never write them into this repo's docs.
