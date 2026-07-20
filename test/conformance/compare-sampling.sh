#!/bin/bash
# Diagnostic: WHERE does tools-call-sampling flake? The reference server sends
# its server->client sampling/createMessage on the client's standalone GET SSE
# stream, which races the tools/call POST. Direct it never flakes; through the
# ToolHive stack it does. This tallies the sampling result across three tiers to
# localize which stack element widens the race:
#
#   DIRECT : client -> container                              (no ToolHive)
#   SQUID  : client -> Squid ingress -> container             (no Go proxy)
#   STACK  : client -> Go transparent proxy -> Squid -> container (full)
#
# DIRECT green + SQUID flaky            -> the Squid ingress is the trigger.
# DIRECT green + SQUID green + STACK bad -> the Go transparent proxy is the trigger.
# all flaky                            -> the container/network setup itself.
#
# Reuses the mcp-conformance-server:ci image built by run-conformance.sh (run
# this step after it).
#
# Env: CONFORMANCE_VERSION (default 0.1.16), THV_BINARY (default thv),
#      SAMPLING_COMPARE_N (default 6).
set -uo pipefail

CONFORMANCE_VERSION="${CONFORMANCE_VERSION:-0.1.16}"
THV_BINARY="${THV_BINARY:-thv}"
N="${SAMPLING_COMPARE_N:-4}"
IMAGE="mcp-conformance-server:ci"
DIRECT_NAME="conf-cmp-direct"
PROXY_NAME="conf-cmp-proxy"
DIRECT_PORT=3009

export TOOLHIVE_DEV=true
export TOOLHIVE_SKIP_DESKTOP_CHECK=1

cleanup() {
  docker rm -f "${DIRECT_NAME}" >/dev/null 2>&1 || true
  "${THV_BINARY}" rm -f "${PROXY_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

if ! docker image inspect "${IMAGE}" >/dev/null 2>&1; then
  echo "ERROR: ${IMAGE} not present; run run-conformance.sh first (it builds it)." >&2
  exit 1
fi

wait_ready() { # $1=url
  for _ in $(seq 1 30); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "$1" -X POST \
      -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
      -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}' 2>/dev/null || true)"
    [ "${code}" = "200" ] && return 0
    sleep 2
  done
  return 1
}

# Runs the sampling scenario $N times against $2, tallies failures into the
# variable named by $1. Echoes per-iteration results.
tally() { # $1=label $2=url -> echoes "<label>: <fail> / <N> failed"; sets _fail
  local label="$1" url="$2" fail=0 r
  for i in $(seq 1 "$N"); do
    # Run the FULL active suite, not --scenario: the sampling flake only
    # reproduces in the full-suite context (isolated --scenario completes in
    # ~1s and never hits the 60s hang). Grep the suite summary line.
    out="$(npx -y "@modelcontextprotocol/conformance@${CONFORMANCE_VERSION}" server \
      --url "${url}" --suite active -o /tmp/cmp-results 2>&1 || true)"
    if echo "$out" | grep -qE "✓ tools-call-sampling"; then r=PASS
    elif echo "$out" | grep -qE "✗ tools-call-sampling"; then r=FAIL; fail=$((fail+1))
    else r=UNKNOWN; fi
    echo "  ${label} ${i}: ${r}"
  done
  _fail="${fail}"
}

# --- DIRECT: plain container, no ToolHive ------------------------------------
echo "==> DIRECT: client -> container (no ToolHive)"
docker rm -f "${DIRECT_NAME}" >/dev/null 2>&1 || true
docker run -d --rm -p "${DIRECT_PORT}:3000" --name "${DIRECT_NAME}" "${IMAGE}" >/dev/null
wait_ready "http://127.0.0.1:${DIRECT_PORT}/mcp" || echo "  (direct server not ready)"
tally direct "http://127.0.0.1:${DIRECT_PORT}/mcp"; dfail="${_fail}"
docker rm -f "${DIRECT_NAME}" >/dev/null 2>&1 || true

# --- Bring up ONE ToolHive stack; it gives both the SQUID and STACK tiers -----
echo "==> Starting ToolHive stack (${PROXY_NAME})"
"${THV_BINARY}" rm -f "${PROXY_NAME}" >/dev/null 2>&1 || true
"${THV_BINARY}" run "${IMAGE}" --transport streamable-http --target-port 3000 --name "${PROXY_NAME}"
URL=""
for _ in $(seq 1 30); do
  URL="$("${THV_BINARY}" list --format json 2>/dev/null | python3 -c "import sys,json;d=json.load(sys.stdin);print(next((w['url'] for w in d if w.get('name')=='${PROXY_NAME}' and w.get('status')=='running' and w.get('url')),''))" 2>/dev/null || true)"
  [ -n "${URL}" ] && break
  sleep 2
done
[ -z "${URL}" ] && { echo "ERROR: could not resolve ${PROXY_NAME} URL" >&2; exit 1; }

# --- SQUID: hit the ingress Squid directly, bypassing the Go transparent proxy.
# thv publishes the ingress squid (container "<name>-ingress") to a host port;
# the Go proxy forwards to it. Reach it directly to drop the Go proxy from the path.
squid_hp="$(docker port "${PROXY_NAME}-ingress" 2>/dev/null | awk -F: 'NR==1{print $NF}')"
sfail="n/a"
if [ -n "${squid_hp}" ]; then
  SQUID_URL="http://127.0.0.1:${squid_hp}/mcp"
  echo "==> SQUID: client -> Squid ingress (${SQUID_URL}) -> container (no Go proxy)"
  if wait_ready "${SQUID_URL}"; then
    tally squid "${SQUID_URL}"; sfail="${_fail}"
  else
    echo "  (squid ingress not reachable at ${SQUID_URL}; skipping tier)"
  fi
else
  echo "==> SQUID: could not resolve ${PROXY_NAME}-ingress host port; skipping tier"
fi

# --- STACK: full path through the Go transparent proxy ------------------------
echo "==> STACK: client -> thv (Go proxy -> Squid ingress) -> container"
wait_ready "${URL}" || echo "  (stack server not ready)"
tally stack "${URL}"; pfail="${_fail}"
"${THV_BINARY}" rm -f "${PROXY_NAME}" >/dev/null 2>&1 || true

echo ""
echo "===== tools-call-sampling failure rate (n=${N} per tier) ====="
echo "DIRECT (container only):        ${dfail} / ${N} failed"
echo "SQUID  (Squid ingress only):    ${sfail} / ${N} failed"
echo "STACK  (Go proxy + Squid):      ${pfail} / ${N} failed"
