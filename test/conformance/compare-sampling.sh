#!/bin/bash
# Diagnostic: does tools-call-sampling flake THROUGH the ToolHive stack
# (Go transparent proxy -> Squid ingress -> container) but not DIRECT
# (client -> container)? Runs the conformance suite N times each way and
# tallies the tools-call-sampling result. Reuses the mcp-conformance-server:ci
# image built by run-conformance.sh (run this step after it).
#
# Env: CONFORMANCE_VERSION (default 0.1.16), THV_BINARY (default thv),
#      SAMPLING_COMPARE_N (default 4).
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

run_suite() { # $1=url -> echoes PASS or FAIL for tools-call-sampling
  out="$(npx -y "@modelcontextprotocol/conformance@${CONFORMANCE_VERSION}" server \
    --url "$1" --suite active -o /tmp/cmp-results 2>&1 || true)"
  if echo "$out" | grep -qE "✓ tools-call-sampling"; then echo PASS
  elif echo "$out" | grep -qE "✗ tools-call-sampling"; then echo FAIL
  else echo "UNKNOWN"; fi
}

echo "==> DIRECT: client -> container (no ToolHive)"
docker rm -f "${DIRECT_NAME}" >/dev/null 2>&1 || true
docker run -d --rm -p "${DIRECT_PORT}:3000" --name "${DIRECT_NAME}" "${IMAGE}" >/dev/null
wait_ready "http://127.0.0.1:${DIRECT_PORT}/mcp" || { echo "direct server not ready"; }
dfail=0
for i in $(seq 1 "$N"); do r="$(run_suite "http://127.0.0.1:${DIRECT_PORT}/mcp")"; echo "  direct $i: $r"; [ "$r" = FAIL ] && dfail=$((dfail+1)); done
docker rm -f "${DIRECT_NAME}" >/dev/null 2>&1 || true

echo "==> STACK: client -> thv (Go proxy -> Squid ingress) -> container"
"${THV_BINARY}" rm -f "${PROXY_NAME}" >/dev/null 2>&1 || true
"${THV_BINARY}" run "${IMAGE}" --transport streamable-http --target-port 3000 --name "${PROXY_NAME}"
URL=""
for _ in $(seq 1 30); do
  URL="$("${THV_BINARY}" list --format json 2>/dev/null | python3 -c "import sys,json;d=json.load(sys.stdin);print(next((w['url'] for w in d if w.get('name')=='${PROXY_NAME}' and w.get('status')=='running' and w.get('url')),''))" 2>/dev/null || true)"
  [ -n "${URL}" ] && break
  sleep 2
done
[ -z "${URL}" ] && { echo "ERROR: could not resolve ${PROXY_NAME} URL" >&2; exit 1; }
wait_ready "${URL}" || { echo "stack server not ready"; }
pfail=0
for i in $(seq 1 "$N"); do r="$(run_suite "${URL}")"; echo "  stack  $i: $r"; [ "$r" = FAIL ] && pfail=$((pfail+1)); done
"${THV_BINARY}" rm -f "${PROXY_NAME}" >/dev/null 2>&1 || true

echo ""
echo "===== tools-call-sampling failure rate ====="
echo "DIRECT (no ToolHive):        ${dfail} / ${N} failed"
echo "STACK  (thv Go+Squid proxy): ${pfail} / ${N} failed"
