# Container Exit Handling with Automatic Restart

## Problem

When a container exits (crashes or is stopped), the toolhive proxy would silently lose connection. The proxy would continue running but requests would fail without clear feedback to the user about what happened or how to recover.

## Solution

**Simple and Automatic**: Detect container exit → Check if workload exists → Either restart or clean up

When the container monitor detects that a container has stopped:

1. **Log a clear warning** - `WARN: Container fetch exited`
2. **Stop the proxy cleanly** - Releases port and resources
3. **Check if workload exists** - Uses `thv ls` logic to see if workload was removed or just exited
4. **If removed** (not in `thv ls`):
   - Remove from client config (updates `~/.cursor/mcp.json` etc.)
   - Exit gracefully, no restart
5. **If still exists** (in `thv ls`):
   - Remove from client config temporarily
   - Automatic restart with backoff (5s, 10s, 20s, 40s, 60s)
   - Re-add to client config after successful restart
   - Up to 10 attempts (~10 minutes)

## Benefits

✅ **No silent failures** - Clear logging about what happened  
✅ **Automatic recovery** - Restarts automatically without user intervention  
✅ **Exponential backoff** - Smart retry delays (5s → 60s max)  
✅ **Clean shutdown** - Proxy stops properly between retries  
✅ **Eventually consistent** - Keeps trying for transient issues  
✅ **Gives up gracefully** - After 10 attempts, stops cleanly

## What Changed

### Transport Layer (`pkg/transport/http.go`, `pkg/transport/stdio.go`)
- Updated `handleContainerExit()` to log at WARN level with clear messages
- Cleanly stops proxy when container exits

### Runner Layer (`pkg/runner/runner.go`)
- When transport stops, checks if workload still exists using `DoesWorkloadExist`
- **If workload doesn't exist in `thv ls`**: Removes from client config and exits gracefully
- **If workload exists**: Returns special error "container exited, restart needed"
- This simple check determines whether to restart or clean up

### Workload Manager (`pkg/workloads/manager.go`)
- Added retry loop with exponential backoff in `RunWorkload()`
- Retries up to 10 times with delays: 5s, 10s, 20s, 40s, 60s (capped)
- Sets workload status to "starting" during retry attempts
- **Removes from client config before each restart** - Forces clients to reconnect
- **Re-adds to client config after restart** - Runner does this automatically after starting
- Only retries on "container exited, restart needed" errors, not other failures

## Retry Timeline

```
Container exits
├─ Attempt 1: Immediate restart
├─ Attempt 2: After 5s delay
├─ Attempt 3: After 10s delay  
├─ Attempt 4: After 20s delay
├─ Attempt 5: After 40s delay
├─ Attempt 6: After 60s delay
├─ Attempt 7: After 60s delay
├─ Attempt 8: After 60s delay
├─ Attempt 9: After 60s delay
├─ Attempt 10: After 60s delay
└─ Give up (after ~10 minutes total)
```

## User Experience

**Before:**
```
Container exits → Proxy keeps running → Requests fail silently → Confusion
```

**After (container exits but still in `thv ls`):**
```
Container exits → WARN logged → Proxy stops → Check `thv ls` → Still exists →
Remove from client config → Automatic restart (10 attempts) → 
Re-add to client config → ✅ Recovered
Cursor sees config change → Reconnects with fresh session → Tool works!
```

**After (container removed with `docker rm` or `thv delete`):**
```
Container exits → WARN logged → Proxy stops → Check `thv ls` → Doesn't exist →
Remove from client config → Exit gracefully → ✅ Clean shutdown
Cursor sees config removed → No longer shows the tool
```

**If container keeps crashing:**
```
Container exits → Retries with backoff → After 10 attempts → Gives up → Status: Error
```

The system now:
- **Handles transient container failures automatically** with exponential backoff
- **Distinguishes between exit and removal** using `thv ls` as source of truth
- **Updates client config** to force clean reconnection (remove then re-add)
- **Gives up gracefully** on persistent issues or when workload is intentionally removed
- Clients (like Cursor) see the config changes and reconnect with new sessions

