#!/bin/bash
# Run the MCP conformance suite against a ToolHive deployment.
#
# Packages the conformance repo's reference server (examples/servers/typescript)
# dynamically at a pinned version and points `npx @modelcontextprotocol/conformance`
# at a ToolHive-exposed endpoint. Two targets are supported via CONFORMANCE_TARGET:
#
#   proxy  (default)  Runs the reference server through `thv run` (streamable-http)
#                     and tests the transparent CLI proxy endpoint. This path does
#                     not exercise the migrated mcpcompat/go-sdk serving code.
#   vmcp              Runs the reference server as a backend in a group, aggregates
#                     it through `thv vmcp serve`, and tests the vMCP endpoint. This
#                     path DOES exercise the mcpcompat/go-sdk serving surface.
#
# A single CONFORMANCE_VERSION pins both the npm tool and the git server source
# (repo tags vX.Y.Z map 1:1 to npm versions), keeping fixtures and tool in sync.
#
# Env:
#   CONFORMANCE_TARGET   deployment under test: proxy|vmcp      (default: proxy)
#   CONFORMANCE_VERSION  conformance tool + server version      (default: 0.1.16)
#   CONFORMANCE_SUITE    suite to run: active|all|pending       (default: active)
#   THV_BINARY           path to the thv binary                 (default: thv)
#   VMCP_PORT            port for `thv vmcp serve` (vmcp target) (default: 8099)
set -euo pipefail

CONFORMANCE_TARGET="${CONFORMANCE_TARGET:-proxy}"
CONFORMANCE_VERSION="${CONFORMANCE_VERSION:-0.1.16}"
CONFORMANCE_SUITE="${CONFORMANCE_SUITE:-active}"
THV_BINARY="${THV_BINARY:-thv}"
VMCP_PORT="${VMCP_PORT:-8099}"
SERVER_NAME="conf-sut-ci"
GROUP_NAME="conf-vmcp-group"
IMAGE="mcp-conformance-server:ci"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RESULTS_DIR="${SCRIPT_DIR}/results"

# thv proxying to a container is a local-only, trusted-dev workflow.
export TOOLHIVE_DEV=true
export TOOLHIVE_SKIP_DESKTOP_CHECK=1

# Shared JSON-RPC initialize payload used by the readiness probe and the vMCP
# serverInfo sanity check.
INIT_PAYLOAD='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}'

CLONE_DIR=""
VMCP_PID=""
cleanup() {
  # Diagnostics: capture the ToolHive proxy logs before teardown so a CI failure
  # (e.g. the sampling round-trip timing out) is debuggable from the uploaded
  # results artifact, not just the client-side checks.json.
  mkdir -p "${RESULTS_DIR}"
  "${THV_BINARY}" logs --proxy "${SERVER_NAME}" > "${RESULTS_DIR}/proxy-logs.txt" 2>&1 || true
  if [ -n "${VMCP_PID}" ]; then
    kill "${VMCP_PID}" >/dev/null 2>&1 || true
  fi
  "${THV_BINARY}" rm -f "${SERVER_NAME}" >/dev/null 2>&1 || true
  if [ "${CONFORMANCE_TARGET}" = "vmcp" ]; then
    # `group rm` prompts for confirmation only when the group still has
    # workloads; feed "y" so the non-interactive EXIT trap can never block.
    echo y | "${THV_BINARY}" group rm "${GROUP_NAME}" >/dev/null 2>&1 || true
  fi
  [ -n "${CLONE_DIR}" ] && rm -rf "${CLONE_DIR}" || true
}
trap cleanup EXIT

# resolve_workload_url prints the proxy URL of a running workload, polling until
# it appears (or the retry budget is exhausted, in which case it prints nothing).
resolve_workload_url() {
  local name="$1" url=""
  for _ in $(seq 1 30); do
    url="$("${THV_BINARY}" list --format json 2>/dev/null \
      | python3 -c "import sys,json
d=json.load(sys.stdin)
print(next((w['url'] for w in d if w.get('name')=='${name}' and w.get('status')=='running' and w.get('url')), ''))" \
      2>/dev/null || true)"
    [ -n "${url}" ] && break
    sleep 2
  done
  printf '%s' "${url}"
}

echo "==> Target: ${CONFORMANCE_TARGET}"

echo "==> Cloning conformance repo v${CONFORMANCE_VERSION}"
CLONE_DIR="$(mktemp -d)"
for attempt in 1 2 3; do
  if git clone --depth 1 --branch "v${CONFORMANCE_VERSION}" \
      https://github.com/modelcontextprotocol/conformance "${CLONE_DIR}/repo"; then
    break
  fi
  echo "clone attempt ${attempt} failed; retrying..." >&2
  rm -rf "${CLONE_DIR}/repo"
  sleep 5
  [ "${attempt}" = 3 ] && { echo "ERROR: could not clone conformance repo" >&2; exit 1; }
done

SERVER_DIR="${CLONE_DIR}/repo/examples/servers/typescript"
if [ ! -f "${SERVER_DIR}/everything-server.ts" ]; then
  echo "ERROR: reference server not found at ${SERVER_DIR}" >&2
  exit 1
fi

echo "==> Building reference server image"
cp "${SCRIPT_DIR}/Dockerfile" "${SERVER_DIR}/Dockerfile"
docker build -t "${IMAGE}" "${SERVER_DIR}"

if [ "${CONFORMANCE_TARGET}" = "proxy" ]; then
  # ---------------------------------------------------------------------------
  # proxy target: test the transparent `thv run` CLI proxy endpoint directly.
  # ---------------------------------------------------------------------------
  EXPECTED_FAILURES="${SCRIPT_DIR}/expected-failures.yaml"

  echo "==> Starting server through ToolHive"
  "${THV_BINARY}" rm -f "${SERVER_NAME}" >/dev/null 2>&1 || true
  "${THV_BINARY}" run "${IMAGE}" \
    --transport streamable-http --target-port 3000 --name "${SERVER_NAME}" --debug

  echo "==> Resolving proxy URL"
  URL="$(resolve_workload_url "${SERVER_NAME}")"
  [ -z "${URL}" ] && { echo "ERROR: could not resolve ${SERVER_NAME} URL" >&2; "${THV_BINARY}" list || true; exit 1; }
  echo "    URL: ${URL}"

elif [ "${CONFORMANCE_TARGET}" = "vmcp" ]; then
  # ---------------------------------------------------------------------------
  # vmcp target: run the reference server as a group backend, aggregate it
  # through vMCP, and test the vMCP endpoint (exercises mcpcompat/go-sdk).
  # ---------------------------------------------------------------------------
  EXPECTED_FAILURES="${SCRIPT_DIR}/expected-failures-vmcp.yaml"
  VMCP_CONFIG="${CLONE_DIR}/vmcp.yaml"

  echo "==> Creating group ${GROUP_NAME}"
  echo y | "${THV_BINARY}" group rm "${GROUP_NAME}" >/dev/null 2>&1 || true
  "${THV_BINARY}" group create "${GROUP_NAME}"

  echo "==> Starting backend workload through ToolHive"
  "${THV_BINARY}" rm -f "${SERVER_NAME}" >/dev/null 2>&1 || true
  "${THV_BINARY}" run "${IMAGE}" \
    --transport streamable-http --target-port 3000 \
    --name "${SERVER_NAME}" --group "${GROUP_NAME}"

  echo "==> Waiting for backend to become running (so vmcp init can discover it)"
  BACKEND_URL="$(resolve_workload_url "${SERVER_NAME}")"
  [ -z "${BACKEND_URL}" ] && { echo "ERROR: backend ${SERVER_NAME} never became running" >&2; "${THV_BINARY}" list || true; exit 1; }
  echo "    backend URL: ${BACKEND_URL}"

  echo "==> Generating vMCP config with thv vmcp init"
  "${THV_BINARY}" vmcp init --group "${GROUP_NAME}" --config "${VMCP_CONFIG}"

  # Patch the generated config so tool names are PRESERVED (not prefixed with the
  # backend name), otherwise the conformance suite's tool fixtures won't match.
  # `thv vmcp init` emits:
  #     aggregation:
  #       conflictResolution: prefix
  #       conflictResolutionConfig:
  #         prefixFormat: "{workload}_"
  # We rewrite it to the "priority" strategy with a single-backend priorityOrder:
  #     aggregation:
  #       conflictResolution: priority
  #       conflictResolutionConfig:
  #         priorityOrder:
  #           - conf-sut-ci
  # PyYAML may be absent, so this is a plain-text substitution (no yaml module).
  echo "==> Patching aggregation strategy prefix -> priority"
  python3 - "${VMCP_CONFIG}" "${SERVER_NAME}" <<'PY'
import sys
path, server = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as f:
    text = f.read()
text = text.replace(
    "  conflictResolution: prefix",
    "  conflictResolution: priority",
)
text = text.replace(
    '    prefixFormat: "{workload}_"',
    "    priorityOrder:\n      - " + server,
)
if "  conflictResolution: priority" not in text:
    sys.exit("ERROR: could not patch conflictResolution to priority (template changed?)")
if "prefixFormat" in text:
    sys.exit("ERROR: prefixFormat still present after patch (template changed?)")
with open(path, "w", encoding="utf-8") as f:
    f.write(text)
PY

  echo "==> Patched vMCP config:"
  echo "-----------------------------------------------------------------"
  cat "${VMCP_CONFIG}"
  echo "-----------------------------------------------------------------"

  echo "==> Validating patched config"
  "${THV_BINARY}" vmcp validate --config "${VMCP_CONFIG}"

  echo "==> Starting thv vmcp serve on port ${VMCP_PORT}"
  "${THV_BINARY}" vmcp serve --config "${VMCP_CONFIG}" --port "${VMCP_PORT}" &
  VMCP_PID=$!
  echo "    vmcp serve PID: ${VMCP_PID}"

  URL="http://127.0.0.1:${VMCP_PORT}/mcp"
  echo "    URL: ${URL}"

else
  echo "ERROR: unknown CONFORMANCE_TARGET '${CONFORMANCE_TARGET}' (expected proxy|vmcp)" >&2
  exit 1
fi

echo "==> Waiting for endpoint to be ready"
code=""
for _ in $(seq 1 30); do
  code="$(curl -s -o /dev/null -w '%{http_code}' "${URL}" -X POST \
    -H 'content-type: application/json' \
    -H 'accept: application/json, text/event-stream' \
    -d "${INIT_PAYLOAD}" || true)"
  [ "${code}" = "200" ] && break
  sleep 2
done
[ "${code}" = "200" ] || { echo "ERROR: endpoint never became ready (last HTTP ${code})" >&2; exit 1; }

# For the vmcp target, prove we actually hit vMCP and not a mis-resolved URL by
# asserting the initialize response identifies vMCP in result.serverInfo.name.
if [ "${CONFORMANCE_TARGET}" = "vmcp" ]; then
  echo "==> Sanity check: verifying serverInfo identifies vMCP"
  INIT_RESP="$(curl -s "${URL}" -X POST \
    -H 'content-type: application/json' \
    -H 'accept: application/json, text/event-stream' \
    -d "${INIT_PAYLOAD}" || true)"
  SERVER_INFO_NAME="$(printf '%s' "${INIT_RESP}" | python3 -c "
import sys, json
raw = sys.stdin.read()
# streamable-http may frame the response as SSE ('data: {json}' lines).
chunks = [l[5:].strip() for l in raw.splitlines() if l.strip().startswith('data:')]
for c in (chunks or [raw]):
    try:
        d = json.loads(c)
    except Exception:
        continue
    info = (d.get('result') or {}).get('serverInfo') or {}
    if info.get('name'):
        print(info['name'])
        break
" 2>/dev/null || true)"
  echo "    serverInfo.name: ${SERVER_INFO_NAME:-<none>}"
  case "$(printf '%s' "${SERVER_INFO_NAME}" | tr '[:upper:]' '[:lower:]')" in
    *vmcp*|*virtual*)
      echo "    OK: endpoint identifies as vMCP"
      ;;
    *)
      echo "ERROR: serverInfo.name '${SERVER_INFO_NAME}' does not identify vMCP;" \
           "the URL may not point at the vMCP server" >&2
      echo "    raw initialize response: ${INIT_RESP}" >&2
      exit 1
      ;;
  esac
fi

echo "==> Running conformance suite (${CONFORMANCE_SUITE}) against ${CONFORMANCE_TARGET}"
echo "    expected-failures: ${EXPECTED_FAILURES}"
rm -rf "${RESULTS_DIR}"
mkdir -p "${RESULTS_DIR}"
set +e
npx -y "@modelcontextprotocol/conformance@${CONFORMANCE_VERSION}" server \
  --url "${URL}" \
  --suite "${CONFORMANCE_SUITE}" \
  --expected-failures "${EXPECTED_FAILURES}" \
  -o "${RESULTS_DIR}" 2>&1 | tee "${RESULTS_DIR}/suite-output.log"
suite_rc=${PIPESTATUS[0]}
set -e

# Quarantine: tools-call-sampling flakes intermittently (~1/3) due to an upstream
# reference-server bug, NOT a ToolHive defect. The everything-server sends its
# server->client sampling/createMessage on the client's standalone GET SSE stream
# without a relatedRequestId, so it races the tools/call handler; when the handler
# wins, the request is dropped and the client times out at 60s. See:
#   upstream: https://github.com/modelcontextprotocol/conformance/issues/407
#   toolhive: #5886 (ingress Squid cold-start mitigation, reduces but can't
#             eliminate the client-side ordering race)
# We can't use expected-failures.yaml (a flaky entry stale-fails the ~2/3 of runs
# where it passes), so ignore ONLY tools-call-sampling in the gating here; every
# other scenario still fails the job. Remove this block once #407 is fixed
# upstream and CONFORMANCE_VERSION is bumped to a release that contains the fix.
if [ "${suite_rc}" -ne 0 ]; then
  unexpected="$(sed -n '/Unexpected failures (not in baseline):/,$p' "${RESULTS_DIR}/suite-output.log" \
    | grep -oE '✗ [a-z0-9-]+' | sed 's/✗ //' | sort -u)"
  others="$(printf '%s\n' "${unexpected}" | grep -vE '^(tools-call-sampling)?$' || true)"
  if [ -n "${unexpected}" ] && [ -z "${others}" ]; then
    echo "==> Ignoring quarantined flaky scenario 'tools-call-sampling' (upstream conformance#407 / toolhive#5886); no other unexpected failures."
    exit 0
  fi
  exit "${suite_rc}"
fi
