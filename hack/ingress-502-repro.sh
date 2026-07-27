#!/bin/bash
# Diagnostic stress harness for the ingress-proxy 502 / status "starting" flake.
#
# NOT a product test — a throwaway repro that concurrently starts many
# network-isolated `thv run fetch` workloads (the worst case the group e2e specs
# hit) and, on the first workload that fails to reach "running", dumps enough
# state to decide *why* the ingress Squid is returning 502:
#   - is the MCP upstream resolvable from the ingress container? (DNS-at-start)
#   - is it reachable right now?                                 (dead-peer latch)
#   - what does the ingress Squid access log say?               (HIER_NONE / peer dead)
#
# Meant to run on the real CI runner (ubuntu-8cores) where the flake reproduces;
# local macOS Docker Desktop does not exercise the same bridge/DNS path.
set -u

THV="${THV_BINARY:-./bin/thv}"
export TOOLHIVE_DEV=true
CONC="${CONC:-6}"
ITERS="${ITERS:-30}"
READY_TIMEOUT="${READY_TIMEOUT:-90}"
PREFIX="race502"

log() { echo "[$(date +%H:%M:%S)] $*"; }

status_of() { # $1=name -> "status|context"
  "$THV" list --all --format json 2>/dev/null | python3 -c "
import json,sys
d=json.load(sys.stdin)
w=next((x for x in d if x.get('name')==sys.argv[1]),None)
print(((w.get('status') if w else 'gone') or '?'), '|', ((w.get('status_context') or '') if w else ''))
" "$1" 2>/dev/null
}

cleanup_all() {
  for n in $("$THV" list --all --format json 2>/dev/null | python3 -c "import json,sys;[print(w['name']) for w in json.load(sys.stdin) if w.get('name','').startswith('$PREFIX')]" 2>/dev/null); do
    "$THV" rm "$n" >/dev/null 2>&1
  done
  docker ps -aq --filter "name=$PREFIX" | xargs -r docker rm -f >/dev/null 2>&1
  docker network ls --filter "name=toolhive-$PREFIX" -q | xargs -r docker network rm >/dev/null 2>&1
}
trap cleanup_all EXIT

dump_stalled() { # $1=workload name
  local w="$1" ing="$1-ingress" net="toolhive-$1-internal"
  echo "==================== DIAGNOSTICS: $w ===================="
  echo "## thv status: $(status_of "$w")"
  echo "## containers:"; docker ps -a --filter "name=^/${w}" --format '   {{.Names}} | {{.Status}}'
  echo "## thv logs (tail 40):"; "$THV" logs "$w" 2>/dev/null | tail -40 | sed 's/^/   /'
  echo "## ingress squid log (tail 40) — TCP_MISS_ABORTED/502 with HIER_NONE means a dead/unresolved peer:"
  docker logs "$ing" 2>&1 | tail -40 | sed 's/^/   /'
  echo "## ingress squid.conf (upstream peer + port):"
  docker exec "$ing" cat /etc/squid/squid.conf 2>/dev/null | grep -E "cache_peer|http_port|defaultsite" | sed 's/^/   /'
  # Probe from a throwaway container on the SAME internal network so we do not
  # depend on tools inside the squid image. curlimages/curl is pre-pulled.
  echo "## DNS: is the mcp name resolvable on the internal network? (DNS-at-start hypothesis):"
  docker run --rm --network "$net" curlimages/curl:latest \
    sh -c "nslookup $w 2>&1 | tail -4 || echo UNRESOLVABLE" 2>&1 | sed 's/^/   /'
  echo "## reachability: is the mcp reachable by name RIGHT NOW? (dead-peer-latch hypothesis — healthy here + 502 above == latch):"
  docker run --rm --network "$net" curlimages/curl:latest \
    -s -o /dev/null -m 5 -w "   direct http://$w:8080/ -> %{http_code}\n" "http://$w:8080/" 2>&1 | sed 's/^/   /'
  echo "## networks — mcp vs ingress attachment (must share a network to route):"
  docker inspect "$w"   --format '   mcp     nets={{range $k,$v := .NetworkSettings.Networks}}{{$k}}({{$v.IPAddress}}) {{end}}' 2>/dev/null
  docker inspect "$ing" --format '   ingress nets={{range $k,$v := .NetworkSettings.Networks}}{{$k}}({{$v.IPAddress}}) {{end}}' 2>/dev/null
  echo "## mcp server container log (tail 15) — is the server listening?:"
  docker logs "$w" 2>&1 | tail -15 | sed 's/^/   /'
  echo "========================================================"
}

log "stress: CONC=$CONC ITERS=$ITERS READY_TIMEOUT=${READY_TIMEOUT}s binary=$THV"
for it in $(seq 1 "$ITERS"); do
  names=(); for j in $(seq 1 "$CONC"); do names+=("${PREFIX}-${it}-${j}"); done

  for n in "${names[@]}"; do
    timeout 150 "$THV" run fetch --name "$n" >/dev/null 2>&1 &
  done
  wait

  deadline=$((SECONDS + READY_TIMEOUT)); stalled=()
  while :; do
    stalled=()
    for n in "${names[@]}"; do
      s=$(status_of "$n" | awk -F'|' '{gsub(/ /,"",$1);print $1}')
      [ "$s" != "running" ] && stalled+=("$n")
    done
    [ ${#stalled[@]} -eq 0 ] && break
    [ $SECONDS -ge $deadline ] && break
    sleep 3
  done

  if [ ${#stalled[@]} -eq 0 ]; then
    log "iteration $it: all $CONC running"
    cleanup_all
    continue
  fi

  log "iteration $it: REPRODUCED — ${#stalled[@]}/${CONC} did not reach running: ${stalled[*]}"
  for n in "${stalled[@]}"; do dump_stalled "$n"; done
  exit 1
done
log "no reproduction across $ITERS iterations at concurrency $CONC"
