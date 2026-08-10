# AGENTS.md

Guidance for AI coding agents working on this repository.

> **Standing rule: keep this file in sync.** After ANY edit to this
> project (code, config, docs, build, deployment), update AGENTS.md in
> the same change so it fully matches the current state of the project.
> If you add/remove/move a package, endpoint, message type, env var,
> make target or dependency, reflect it here before you finish.

## What this project is

AgentDock: multi-user, multi-device remote control for AI coding
agents. Two Go binaries plus a web frontend:

- **agent-server** — public server (Docker). Serves the embedded web
  UI, authenticates users (first web registration = admin; later
  registrations pending until approved), accepts any number of PC node
  connections, relays terminal traffic between browsers and nodes.
- **agent-client** — runs on a user's PC. Dials OUT to the server
  (never listens) authenticating with one of its owner's node tokens
  (each user holds up to 16, with optional aliases), hosts tmux-like
  PTY sessions that survive browser disconnects. Sessions die with
  the client process.

Access model: a node belongs to the user whose token it connected
with; visible to owner + admins. Any user with access can share the
node (all-or-nothing: all sessions, and onward sharing) with another
user by uid; only owner/admin can revoke.

## Layout (one Go module, `agentdock`)

```
internal/protocol/            shared ws message envelope (flat Message struct)
server/
  cmd/agent-server/main.go    env config, graceful shutdown (no user bootstrap)
  internal/api/               HTTP routes, origin/CSRF check, per-request
                              user load + node ACL checks, static SPA
  internal/auth/              bcrypt, JWT HttpOnly cookie
  internal/database/          SQLite via modernc.org/sqlite (CGO-free);
                              users (role/status), node_tokens (<=16/user,
                              alias, sha256; legacy users.node_token_hash
                              migrates on open), nodes (token_id),
                              node_access, directories (per user),
                              sessions (node_id), settings
  internal/hub/               multi-node registry, session->node routing,
                              browser<->node relay
  internal/webui/             go:embed of built frontend (dist/)
  e2e/                        end-to-end test: server + client + fake browser
client/
  client.go                   facade (Node) used by cmd and tests
  cmd/agent-client/main.go    `connect` subcommand only
  internal/session/           PTY (creack/pty) manager + 256KB scrollback ring
  internal/ws/                outbound ws, auto-reconnect with backoff
scripts/
  install-client.sh           Linux client install+upgrade: git pull, make
                              client, config asked once into
                              ~/.config/agentdock/client.env (0600), systemd
                              user unit via EnvironmentFile, restart
web/                          Svelte 5 (runes) + Vite + Tailwind 4 + xterm.js
                              (WebGL renderer addon, DOM fallback)
  src/app.css                 xianii color theme (dark), tokens vendored
                              from github.com/Nigh/xianii-theme as plain
                              Tailwind @theme vars (no daisyUI dep)
  src/App.svelte              hash routing: '' dashboard, '#/session/<id>',
                              '#/browse?node=<id>&path=...', '#/admin';
                              mounts the global ConfirmDialog
  src/lib/                    Login (register toggle), Dashboard (node
                              cards with token badge, node-tokens card:
                              alias + per-token PC list + delete, share;
                              exited sessions: Reopen = create same
                              name/cwd/shell on that node when online,
                              then delete the exited row),
                              Admin, Terminal, Browse, api.js, confirm.svelte.js +
                              ConfirmDialog.svelte (promise-based modal on
                              native <dialog>, replaces window.confirm)
  vite.config.js              outDir -> server/internal/webui/dist;
                              build target es2022 (xterm 6.0's pre-minified
                              `||=` breaks if re-minified for es2020: vim/
                              DECRQM freeze, xterm.js#5800)
screenshots/                  README images: dashboard + herdr session
```

## Protocol (internal/protocol)

One flat JSON `Message` per node websocket, that node's sessions
multiplexed. Types: node->server `hello sessions output buffer created
exited error dirlist`; server->node `create input resize kill attach
listdir`. `create` may carry `from_session` (spawn in that session's
live cwd, read from /proc/<pid>/cwd); `listdir`/`dirlist` power the
web directory browser (request keyed by `ConnID`). The hub upserts the
node row on `hello` (owner from the token + name) and keeps a
sessionID->nodeID routing table for everything else.
Browser<->server terminal ws: binary frames = raw terminal bytes, text
frames = JSON control (`resize` in; `exited`, `node_offline`, `error`
out). Attach replay ordering: hub holds a browser's output until its
`buffer` (keyed by `ConnID`) arrives, preventing duplicated bytes.

## HTTP API (all under /api, cookie-authed except register/login/node ws)

```
POST /api/register                  first user = active admin, rest pending
POST /api/login | POST /api/logout | GET /api/me
GET  /api/me/node-tokens            list own node tokens (id, alias, created)
POST /api/me/node-tokens {name?}    mint token (max 16/user), plaintext once
DELETE /api/me/node-tokens/{id}     revoke + disconnect PCs using it
GET  /api/state                     me + accessible nodes (+shares +
                                    token_id/token_name) + their sessions
                                    + own directories + own tokens
GET  /api/users                     admin: full list; others: active uid+name
POST /api/users/{id}/approve, DELETE /api/users/{id}   (admin)
POST /api/nodes/{id}/share {uid}    anyone with access may share onward
DELETE /api/nodes/{id}/share/{uid}  owner/admin (or grantee removing itself)
GET|POST /api/directories, DELETE /api/directories/{id}   (per user)
GET  /api/browse?node_id=&path=     subdirs of a path on that PC ("" = home)
POST /api/sessions                  {node_id | from_session}; waits (10s)
                                    for node ack; from_session inherits cwd
DELETE /api/sessions/{id}
GET  /api/sessions/{id}/ws          browser terminal (cookie + Origin check)
GET  /api/node/ws                   node (Bearer adk_... token, sha256 in
                                    node_tokens; hub records which token a
                                    node connected with)
```

Every node/session-scoped route re-checks node access
(`canAccessNode`/`sessionForUser` in api.go); `authed` loads the user
row per request, so approval revocation applies immediately.

## Env vars (server)

`AGENTDOCK_ADDR` (:8080), `AGENTDOCK_DB` (./data/agentdock.db;
/data/agentdock.db in Docker), `AGENTDOCK_JWT_SECRET` (auto-generated,
persisted in SQLite settings), `AGENTDOCK_COOKIE_SECURE` (true), `AGENTDOCK_PUBLISH` (compose-only:
host publish spec for the container port, `.env.example` defaults to
127.0.0.1:8080). Compose build args `GOPROXY` / `NPM_REGISTRY`
(mirrors for hosts that can't reach proxy.golang.org /
registry.npmjs.org). No credential env vars: users register in the web UI,
node tokens are generated per user on the dashboard. Client:
`--server/--token/--name` with env fallbacks `AGENTDOCK_SERVER` /
`AGENTDOCK_NODE_TOKEN` / `AGENTDOCK_NODE_NAME`.

## Build & test

```
make test        # go vet ./... && go test ./...  (must pass before commit)
make all         # web build (embeds into server) + both binaries -> bin/
make dev-server  # local server :8080, insecure cookies; register admin in UI
cd web && npm run dev   # Vite :5173, proxies /api with ws
```

go.mod requires go >= 1.25 (toolchain auto-downloads). Frontend build
output is git-ignored except the placeholder
`server/internal/webui/dist/index.html`; never commit built assets.
Docker build: `Dockerfile` (3-stage: node -> go -> alpine, both
binaries in the image), `docker-compose.yml` reads `.env`.

## Tests

- `server/e2e/e2e_test.go` — the main check: registration (first =
  admin), node tokens, real PTY round-trip (create session,
  echo, detach, re-attach scrollback replay, kill, exited notice),
  directory browsing (`/api/browse` + `from_session` cwd inheritance),
  the multi-user flow (pending login 403, approval, node
  invisibility, 403s before sharing, share + onward share, revoke),
  and multi-token (second PC on a second token, state links node to
  token, delete disconnects + 401s that token, 16-token cap).
- `server/internal/auth/auth_test.go` — bcrypt + JWT.
- `server/internal/database/db_test.go` — legacy single-token
  migration into node_tokens (idempotent).
- `client/internal/session/ring_test.go` — scrollback ring buffer.

Any change to hub/relay/session/protocol logic must keep these green;
extend the e2e test when adding protocol behavior.

## Conventions & constraints

- Lazy-senior style (see workspace ponytail rule): stdlib first, no
  new deps or abstractions without need, `ponytail:` comments mark
  intentional simplifications and their upgrade path.
- Security invariants (do not weaken): PC dials out only; bcrypt
  passwords; no default credentials; node tokens stored hashed
  (sha256), plaintext shown once, capped at 16 per user, deleting one
  disconnects its PCs; pending users cannot log in and are
  re-checked per request; node ACL enforced on every node/session
  route; HttpOnly SameSite=Lax cookie; Origin check on mutating
  requests and the browser ws.
- Future extension points that exist but must stay UNimplemented for
  now: per-session permissions, agent-type metadata.
- Server tree may not import `client/internal/...` — use the
  `agentdock/client` facade (that's why it exists).

## Deployment state (reference)

Production: server runs via docker compose behind an HTTPS reverse
proxy; the repo is cloned on the host, so updating is `git pull &&
docker compose up -d --build` (host publish spec via `AGENTDOCK_PUBLISH`
in `.env`, loopback by default). docker save/load remains a fallback
for hosts that can't clone (see README). Client runs on the dev PC as
a systemd user service installed/upgraded by `scripts/install-client.sh`
(unit reads `~/.config/agentdock/client.env`, linger enabled). Secrets
and real hostnames (node token, passwords, domains) live only in the
server's `.env` and the client's `client.env` — never commit them, and
never write them into this repo's docs.
