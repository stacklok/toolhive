// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"fmt"
	"time"
)

// Redis keys for untrusted pod lifecycle state. All are single-key operations
// under the tenant's ':'-terminated session-storage prefix (validated the same
// way as pkg/transport/session/storage_redis.go). The pod→user mapping lives
// only in pod labels/annotations (downward-API registry); Redis holds only
// counters and TTLs.
//
// Key shapes (prefix = tenant prefix, e.g. "thv:vmcp:session:"):
//
//	<prefix>untrusted:podttl:<pod>               string      idle TTL      pod liveness lease
//	<prefix>untrusted:pods                       set         none (rebuilt) admission global capacity
//	<prefix>untrusted:pods:<mcpserverUID>        set         none (rebuilt) admission per-server cap
//	<prefix>untrusted:userquota:<userHash>       counter     idle TTL      per-user concurrent pods (INCR/DECR ledger)
//	<prefix>untrusted:quotakeys                  set         none            registry of quota keys for reaper correction
//	<prefix>untrusted:ratelimit:<userHash>:<min> counter     2m            per-user create rate
//	<prefix>untrusted:heartbeat:<vmcpUID>        string      5m            vMCP liveness for zombie rule
func (r *redisStore) podTTLKey(podName string) string {
	return r.prefix + "untrusted:podttl:" + podName
}

func (r *redisStore) podsSetKey() string {
	return r.prefix + "untrusted:pods"
}

func (r *redisStore) serverPodsSetKey(mcpserverUID string) string {
	return r.prefix + "untrusted:pods:" + mcpserverUID
}

func (r *redisStore) userQuotaKey(userKey string) string {
	return r.prefix + "untrusted:userquota:" + userHash(userKey)
}

func (r *redisStore) rateLimitKey(userKey string, t time.Time) string {
	return fmt.Sprintf("%suntrusted:ratelimit:%s:%d", r.prefix, userHash(userKey), t.Unix()/60)
}

func (r *redisStore) heartbeatKey(vmcpUID string) string {
	return r.prefix + "untrusted:heartbeat:" + vmcpUID
}
