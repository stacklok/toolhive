#!/bin/bash
# Diagnostic: WHERE does tools-call-sampling flake? The reference server sends
# its server->client sampling/createMessage on the client's standalone GET SSE
# stream, which races the tools/call POST. Direct it never flakes; through the
# ToolHive stack it does. This tallies the sampling result across three tiers to
# localize which stack element widens the race:
#
#   DIRECT : client -> container                                  (no ToolHive)
#   SQUID  : client -> Squid ingress -> container                 (no Go proxy)
#   STACK  : client -> Go transparent proxy -> Squid -> container (full)
#
# DIRECT green + SQUID flaky             -> the Squid ingress is the trigger.
# DIRECT green + SQUID green + STACK bad -> the Go transparent proxy is the trigger.
# all flaky                             -> the container/network setup itself.
#
# COLD-START is the crux: the flake only manifests on the FIRST suite run
# against a freshly-created stack; reusing one warm stack across iterations
# never reproduces it. So every iteration gets its OWN fresh stack/container,
# and SQUID and STACK use SEPARATE fresh stacks (measuring both off one stack
# would leave the second tier warm and hide the flake). This costs 2N `thv run`s.
#
# Reuses the mcp-conformance-server:ci image built by run-conformance.sh (run
# this step after it).
#
# Env: CONFORMANCE_VERSION (default 0.1.16), THV_BINARY (default thv),
#      SAMPLING_COMPARE_N (default 4) = cold stacks per tier.
set -uo pipefail

CONFORMANCE_VERSION="${CONFORMANCE_VERSION:-0.1.16}"
THV_BINARY="${THV_BINARY:-thv}"
N="${SAMPLING_COMPARE_N:-3}"  # cold stacks per tier; 2N thv runs must fit the 30m job budget
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

# One FULL active-suite run against $1; echoes PASS/FAIL/UNKNOWN for the sampling
# scenario. --scenario in isolation never reproduces the hang; the full suite does.
run_suite_once() { # $1=url
  local out
  out="$(npx -y "@modelcontextprotocol/conformance@${CONFORMANCE_VERSION}" server \
    --url "$1" --suite active -o /tmp/cmp-results 2>&1 || true)"
  if echo "$out" | grep -qE "✓ tools-call-sampling"; then echo PASS
  elif echo "$out" | grep -qE "✗ tools-call-sampling"; then echo FAIL
  else echo UNKNOWN; fi
}

# Start a fresh (cold) ToolHive stack; sets STACK_URL and SQUID_URL (SQUID_URL
# empty if the ingress host port can't be resolved).
start_stack() {
  "${THV_BINARY}" rm -f "${PROXY_NAME}" >/dev/null 2>&1 || true
  "${THV_BINARY}" run "${IMAGE}" --transport streamable-http --target-port 3000 --name "${PROXY_NAME}"
  STACK_URL=""
  for _ in $(seq 1 30); do
    STACK_URL="$("${THV_BINARY}" list --format json 2>/dev/null | python3 -c "import sys,json;d=json.load(sys.stdin);print(next((w['url'] for w in d if w.get('name')=='${PROXY_NAME}' and w.get('status')=='running' and w.get('url')),''))" 2>/dev/null || true)"
    [ -n "${STACK_URL}" ] && break
    sleep 2
  done
  local hp; hp="$(docker port "${PROXY_NAME}-ingress" 2>/dev/null | awk -F: 'NR==1{print $NF}')"
  SQUID_URL=""; [ -n "${hp}" ] && SQUID_URL="http://127.0.0.1:${hp}/mcp"
}
stop_stack() { "${THV_BINARY}" rm -f "${PROXY_NAME}" >/dev/null 2>&1 || true; }

# --- DIRECT: fresh plain container per iteration (cold control, no ToolHive) --
echo "==> DIRECT: client -> container (no ToolHive), cold per iteration"
dfail=0
for i in $(seq 1 "$N"); do
  docker rm -f "${DIRECT_NAME}" >/dev/null 2>&1 || true
  docker run -d --rm -p "${DIRECT_PORT}:3000" --name "${DIRECT_NAME}" "${IMAGE}" >/dev/null
  wait_ready "http://127.0.0.1:${DIRECT_PORT}/mcp" || echo "  (direct ${i} not ready)"
  r="$(run_suite_once "http://127.0.0.1:${DIRECT_PORT}/mcp")"; echo "  direct ${i}: ${r}"
  [ "${r}" = FAIL ] && dfail=$((dfail+1))
  docker rm -f "${DIRECT_NAME}" >/dev/null 2>&1 || true
done

# --- SQUID: fresh stack per iteration, hit the ingress Squid directly ---------
echo "==> SQUID: client -> Squid ingress -> container (no Go proxy), cold per iteration"
sfail=0; sruns=0
for i in $(seq 1 "$N"); do
  start_stack
  if [ -n "${SQUID_URL}" ] && wait_ready "${SQUID_URL}"; then
    r="$(run_suite_once "${SQUID_URL}")"; echo "  squid ${i}: ${r} (${SQUID_URL})"
    sruns=$((sruns+1)); [ "${r}" = FAIL ] && sfail=$((sfail+1))
  else
    echo "  squid ${i}: SKIP (ingress host port unresolved/unreachable)"
  fi
  stop_stack
done

# --- STACK: fresh stack per iteration, full path via the Go transparent proxy -
echo "==> STACK: client -> thv (Go proxy -> Squid ingress) -> container, cold per iteration"
pfail=0
for i in $(seq 1 "$N"); do
  start_stack
  if wait_ready "${STACK_URL}"; then
    r="$(run_suite_once "${STACK_URL}")"; echo "  stack ${i}: ${r}"
    [ "${r}" = FAIL ] && pfail=$((pfail+1))
  else
    echo "  stack ${i}: not ready"
  fi
  stop_stack
done

echo ""
echo "===== tools-call-sampling failure rate (cold, n=${N} per tier) ====="
echo "DIRECT (container only):     ${dfail} / ${N} failed"
if [ "${sruns:-0}" -gt 0 ]; then
  echo "SQUID  (Squid ingress only): ${sfail} / ${sruns} failed"
else
  echo "SQUID  (Squid ingress only): n/a (tier unreachable)"
fi
echo "STACK  (Go proxy + Squid):   ${pfail} / ${N} failed"
echo ""
echo "Read: DIRECT+SQUID green & STACK bad => Go transparent proxy is the trigger."
echo "      DIRECT green & SQUID bad       => Squid ingress is the trigger."
