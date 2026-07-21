#!/bin/bash
# Run the MCP conformance suite against a ToolHive deployment.
#
# Packages the conformance repo's reference server (examples/servers/typescript)
# dynamically at a pinned version, runs it through `thv run` (streamable-http),
# and points `npx @modelcontextprotocol/conformance` at the proxied endpoint.
#
# A single CONFORMANCE_VERSION pins both the npm tool and the git server source
# (repo tags vX.Y.Z map 1:1 to npm versions), keeping fixtures and tool in sync.
#
# Env:
#   CONFORMANCE_VERSION  conformance tool + server version   (default: 0.1.16)
#   CONFORMANCE_SUITE    suite to run: active|all|pending    (default: active)
#   THV_BINARY           path to the thv binary              (default: thv)
set -euo pipefail

CONFORMANCE_VERSION="${CONFORMANCE_VERSION:-0.1.16}"
CONFORMANCE_SUITE="${CONFORMANCE_SUITE:-active}"
THV_BINARY="${THV_BINARY:-thv}"
SERVER_NAME="conf-sut-ci"
IMAGE="mcp-conformance-server:ci"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RESULTS_DIR="${SCRIPT_DIR}/results"
EXPECTED_FAILURES="${SCRIPT_DIR}/expected-failures.yaml"

# thv proxying to a container is a local-only, trusted-dev workflow.
export TOOLHIVE_DEV=true
export TOOLHIVE_SKIP_DESKTOP_CHECK=1

CLONE_DIR=""
cleanup() {
  # Diagnostics: capture the ToolHive proxy logs before teardown so a CI failure
  # (e.g. the sampling round-trip timing out) is debuggable from the uploaded
  # results artifact, not just the client-side checks.json.
  mkdir -p "${RESULTS_DIR}"
  "${THV_BINARY}" logs --proxy "${SERVER_NAME}" > "${RESULTS_DIR}/proxy-logs.txt" 2>&1 || true
  "${THV_BINARY}" rm "${SERVER_NAME}" >/dev/null 2>&1 || true
  [ -n "${CLONE_DIR}" ] && rm -rf "${CLONE_DIR}" || true
}
trap cleanup EXIT

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

echo "==> Starting server through ToolHive"
"${THV_BINARY}" rm -f "${SERVER_NAME}" >/dev/null 2>&1 || true
"${THV_BINARY}" run "${IMAGE}" \
  --transport streamable-http --target-port 3000 --name "${SERVER_NAME}" --debug

echo "==> Resolving proxy URL"
URL=""
for _ in $(seq 1 30); do
  URL="$("${THV_BINARY}" list --format json 2>/dev/null \
    | python3 -c "import sys,json;   d=json.load(sys.stdin)
print(next((w['url'] for w in d if w.get('name')=='${SERVER_NAME}' and w.get('status')=='running' and w.get('url')), ''))" \
    2>/dev/null || true)"
  [ -n "${URL}" ] && break
  sleep 2
done
[ -z "${URL}" ] && { echo "ERROR: could not resolve ${SERVER_NAME} URL" >&2; "${THV_BINARY}" list || true; exit 1; }
echo "    URL: ${URL}"

echo "==> Waiting for endpoint to be ready"
for _ in $(seq 1 30); do
  code="$(curl -s -o /dev/null -w '%{http_code}' "${URL}" -X POST \
    -H 'content-type: application/json' \
    -H 'accept: application/json, text/event-stream' \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}' || true)"
  [ "${code}" = "200" ] && break
  sleep 2
done
[ "${code}" = "200" ] || { echo "ERROR: endpoint never became ready (last HTTP ${code})" >&2; exit 1; }

echo "==> Running conformance suite (${CONFORMANCE_SUITE})"
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
