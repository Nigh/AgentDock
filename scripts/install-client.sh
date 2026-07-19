#!/bin/sh
# Install or upgrade agent-client as a systemd user service (Linux).
# Run from a checkout of the repo: ./scripts/install-client.sh
# First run asks for server/token/name and writes ~/.config/agentdock/client.env;
# later runs just rebuild, reinstall and restart (config is never touched).
set -eu

BIN="$HOME/.local/bin/agent-client"
CONF="$HOME/.config/agentdock/client.env"
UNIT="$HOME/.config/systemd/user/agent-client.service"

cd "$(dirname "$0")/.."
git pull --ff-only
make client
mkdir -p "$(dirname "$BIN")"
install -m755 bin/agent-client "$BIN"

if [ ! -f "$CONF" ]; then
    printf 'Server URL (https://...): '; read -r server
    printf 'Node token (adk_...): '; read -r token
    printf 'Node name [%s]: ' "$(hostname)"; read -r name
    [ -n "$server" ] && [ -n "$token" ] || { echo "server and token are required" >&2; exit 1; }
    mkdir -p "$(dirname "$CONF")"
    umask 077
    cat > "$CONF" <<EOF
AGENTDOCK_SERVER=$server
AGENTDOCK_NODE_TOKEN=$token
AGENTDOCK_NODE_NAME=${name:-$(hostname)}
EOF
fi

mkdir -p "$(dirname "$UNIT")"
cat > "$UNIT" <<EOF
[Unit]
Description=AgentDock client (PC node)
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=$CONF
ExecStart=$BIN connect
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now agent-client
systemctl --user restart agent-client
loginctl enable-linger "$USER" 2>/dev/null || true
echo "agent-client: $(systemctl --user is-active agent-client) ($BIN)"
