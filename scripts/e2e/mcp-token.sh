#!/usr/bin/env bash
# Mint an MCP token on a TEST panel and print the `claude mcp add` line that connects a coding agent to it.
#
#   bash scripts/e2e-wsl.sh --hold            # a test panel with a node, kept running (Ctrl-C ends it); prints the env file
#   bash scripts/e2e/mcp-token.sh /tmp/mg-e2e.XXXXXX/owner.env [--profile readonly|operator|admin] [--ttl-days N]
#
# The env file (written by `e2e-wsl.sh --hold`, mode 0600) holds the test panel's admin URL, the owner's session cookie and
# the authenticator secret: MG_ADMIN_URL, MG_OWNER_COOKIE, MG_TOTP_SECRET, and MG_NETNS, the network namespace the panel
# listens in (it is loopback-only there). Without a file argument the three variables are read from the environment, which
# also fits any other throwaway panel you started yourself. Making a token is the owner's act (a session plus a step-up with
# a fresh authenticator code); this reuses the same step-up as scripts/e2e/mcpe2e.
#
# The token is printed once, in the line on stdout. Never use this on a production panel.
set -euo pipefail
cd "$(dirname "$0")/../.."
export GOTOOLCHAIN=${GOTOOLCHAIN:-auto}

if [ $# -gt 0 ] && [ "${1#--}" = "$1" ]; then
  [ -r "$1" ] || { echo "mcp-token: cannot read $1" >&2; exit 2; }
  set -a
  # shellcheck disable=SC1090
  . "$1"
  set +a
  shift
fi
: "${MG_ADMIN_URL:?set MG_ADMIN_URL (or pass the env file of e2e-wsl.sh --hold)}"
: "${MG_OWNER_COOKIE:?set MG_OWNER_COOKIE}"
: "${MG_TOTP_SECRET:?set MG_TOTP_SECRET}"

run=(go run ./scripts/e2e/mcptoken "$@")
if [ -n "${MG_NETNS:-}" ] && command -v ip >/dev/null && [ "$(ip netns identify $$ 2>/dev/null)" != "$MG_NETNS" ]; then
  # the panel listens on loopback inside the run's namespace of e2e-wsl.sh: reach it from there
  echo "mcp-token: the panel is in network namespace $MG_NETNS; the agent has to run there too:" >&2
  echo "           ip netns exec $MG_NETNS <your agent>" >&2
  run=(ip netns exec "$MG_NETNS" "${run[@]}")
fi
exec "${run[@]}"
