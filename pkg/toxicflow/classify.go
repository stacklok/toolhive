// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toxicflow

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	registrytypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/runner"
)

// ClassifyInput carries everything Classify needs for one server. All fields
// are optional: with no inputs the data and sink roles resolve to "unknown".
type ClassifyInput struct {
	// Name is the workload name.
	Name string
	// Config is the saved RunConfig; nil when it could not be loaded.
	Config *runner.RunConfig
	// Metadata is the server's registry metadata; nil when unavailable.
	Metadata registrytypes.ServerMetadata
	// Annotations maps tool name to MCP annotations harvested from a live
	// tools/list (used by --live); nil for static assessment.
	Annotations map[string]*authorizers.ToolAnnotations
	// Hints are raise-only suggestions from a SourceInference (keyword or LLM),
	// folded after the profile-derived baseline and before overrides.
	Hints []Hint
	// Overrides force role confidence for this server.
	Overrides []Override
}

// signal is a single piece of evidence supporting a role at some confidence.
type signal struct {
	conf   Confidence
	reason string
}

// untrustedTagKeywords mark registry tags that suggest a server ingests
// content the caller does not control (a prompt-injection vector).
var untrustedTagKeywords = []string{
	"web", "fetch", "browser", "search", "email", "rss",
	"scrape", "crawl", "http", "social", "news", "feed",
}

// untrustedToolKeywords mark tool-name fragments suggesting the same.
var untrustedToolKeywords = []string{
	"fetch", "browse", "search", "crawl", "scrape", "navigate",
	"read_url", "get_url", "read_email", "get_issue", "get_page",
	"download", "url",
}

// untrustedTextKeywords are whole words in a server's registry description or
// overview that suggest untrusted-content ingestion. Matched on word
// boundaries (not substrings) and only ever raise the source role to
// "possible" — free-text is non-authoritative.
var untrustedTextKeywords = map[string]struct{}{
	"web": {}, "internet": {}, "url": {}, "urls": {}, "fetch": {},
	"fetches": {}, "fetching": {}, "browse": {}, "browsing": {}, "browser": {},
	"scrape": {}, "scraping": {}, "crawl": {}, "crawling": {}, "rss": {},
	"email": {}, "emails": {}, "website": {}, "websites": {}, "search": {},
}

// Classify assesses a single server's role in a potential toxic flow. It is a
// pure function: given the same inputs it always produces the same assessment.
func Classify(in ClassifyInput) ServerAssessment {
	findings := map[Role]RoleFinding{
		RoleData:   classifyData(in),
		RoleSource: classifySource(in),
		RoleSink:   classifySink(in),
	}
	// Inference hints fold raise-only (they can only strengthen a leg); explicit
	// overrides are authoritative and applied last so they can also weaken one.
	applyHints(findings, in.Hints)
	applyOverrides(findings, in.Name, in.Overrides)
	return ServerAssessment{Name: in.Name, Findings: findings}
}

// classifyData looks for access to private or sensitive data. The permission
// profile (filesystem reads, injected secrets) is the authoritative signal, so
// a confident "none" requires the run config: without it the role is "unknown"
// rather than a misleading absence. Remoteness is deliberately NOT a data
// signal — it is egress (a sink signal); treating it as data too would
// double-count remote servers into two legs.
func classifyData(in ClassifyInput) RoleFinding {
	var sigs []signal

	if in.Config != nil {
		if p := in.Config.PermissionProfile; p != nil && len(p.Read) > 0 {
			sigs = append(sigs, signal{ConfLikely,
				fmt.Sprintf("filesystem read access (%d mount(s))", len(p.Read))})
		}
		if len(in.Config.Secrets) > 0 {
			sigs = append(sigs, signal{ConfLikely,
				fmt.Sprintf("%d secret(s) injected", len(in.Config.Secrets))})
		}
	}

	if in.Metadata != nil {
		for _, ev := range in.Metadata.GetEnvVars() {
			if ev != nil && ev.Secret {
				sigs = append(sigs, signal{ConfLikely,
					fmt.Sprintf("requires secret env var %q", ev.Name)})
			}
		}
	}

	if len(sigs) > 0 {
		return maxFinding(RoleData, sigs)
	}
	// Mounts and injected secrets live on the run config; only with it can we
	// assert a confident absence. Metadata alone is not enough.
	if in.Config != nil {
		return RoleFinding{Role: RoleData, Confidence: ConfNone}
	}
	return RoleFinding{Role: RoleData, Confidence: ConfUnknown}
}

// classifySink looks for the ability to communicate externally. ToolHive's
// runtime is open-egress-by-default: a nil permission profile, nil network
// policy, or nil outbound policy all resolve to unrestricted egress at runtime
// (see pkg/container/docker/squid.go and the default network profile). The
// sink role therefore reaches a confident "none" ONLY on positive evidence of
// closed egress; any unspecified policy is treated as open, never as safe.
func classifySink(in ClassifyInput) RoleFinding {
	// Without a run config, egress is unknowable from here.
	if in.Config == nil {
		return RoleFinding{Role: RoleSink, Confidence: ConfUnknown}
	}

	var sigs []signal
	if in.Config.RemoteURL != "" {
		sigs = append(sigs, signal{ConfLikely,
			"remote server sends requests to an external endpoint"})
	}

	switch p := in.Config.PermissionProfile; p {
	case nil:
		sigs = append(sigs, signal{ConfPossible, "no permission profile; runtime egress is open by default"})
	default:
		if p.Privileged {
			sigs = append(sigs, signal{ConfLikely, "privileged container (network controls bypassable)"})
		}
		switch n := p.Network; n {
		case nil:
			sigs = append(sigs, signal{ConfPossible, "network policy unspecified; runtime egress is open by default"})
		default:
			if n.Mode == "host" {
				sigs = append(sigs, signal{ConfLikely, "host network mode (egress not controlled)"})
			}
			switch o := n.Outbound; {
			case o == nil:
				sigs = append(sigs, signal{ConfPossible, "outbound policy unspecified; runtime egress is open by default"})
			case o.InsecureAllowAll:
				sigs = append(sigs, signal{ConfLikely, "unrestricted outbound network access"})
			case len(o.AllowHost) > 0 || len(o.AllowPort) > 0:
				sigs = append(sigs, signal{ConfPossible,
					fmt.Sprintf("restricted outbound access (%d host(s), %d port(s))", len(o.AllowHost), len(o.AllowPort))})
			}
			// Outbound present with no allow rules and not insecure-allow-all
			// is the only positive evidence of closed egress: no signal added.
		}
	}

	if len(sigs) == 0 {
		return RoleFinding{Role: RoleSink, Confidence: ConfNone,
			Evidence: []string{"outbound network access denied by permission profile"}}
	}
	return maxFinding(RoleSink, sigs)
}

// classifySource looks for exposure to untrusted content from the one signal it
// can trust as evidence of presence: a live tools/list openWorldHint. Tag,
// tool, and description heuristics and LLM judgements are non-authoritative and
// arrive separately as raise-only Hints (see SourceInference / applyHints).
//
// Source classification is raise-only: it never returns a confident "none".
// Untrusted-content exposure cannot be ruled out from a server's own tool list,
// because annotations are advisory and a compromised server can simply
// under-report openWorldHint to hide. Only an explicit operator override may
// assert source "none".
func classifySource(in ClassifyInput) RoleFinding {
	var sigs []signal

	// Iterate annotations in sorted key order so evidence is deterministic.
	for _, name := range slices.Sorted(maps.Keys(in.Annotations)) {
		a := in.Annotations[name]
		if a != nil && a.OpenWorldHint != nil && *a.OpenWorldHint {
			sigs = append(sigs, signal{ConfLikely, fmt.Sprintf("tool %q has openWorldHint", name)})
		}
	}

	if len(sigs) > 0 {
		return maxFinding(RoleSource, sigs)
	}
	return RoleFinding{Role: RoleSource, Confidence: ConfUnknown}
}

// maxFinding folds a non-empty signal set into a finding, taking the strongest
// confidence and collecting every reason as evidence. Each classifier decides
// its own "no signals" semantics (confident none vs unknown) before calling
// this, because that decision differs per role.
func maxFinding(role Role, sigs []signal) RoleFinding {
	best := ConfNone
	ev := make([]string, 0, len(sigs))
	for _, s := range sigs {
		ev = append(ev, s.reason)
		if s.conf.rank() > best.rank() {
			best = s.conf
		}
	}
	return RoleFinding{Role: role, Confidence: best, Evidence: ev}
}

// applyHints folds raise-only inference hints into the findings. A hint takes
// effect only when it would strengthen the role's confidence, so hints can
// never weaken a leg or manufacture a confident "none". The hint reason is
// prepended to the evidence.
func applyHints(findings map[Role]RoleFinding, hints []Hint) {
	for _, h := range hints {
		if !isRole(h.Role) {
			continue
		}
		// Clamp at the choke point: inference backends (keyword or LLM) are
		// non-authoritative, so no hint may push a leg past "possible" — only
		// the structured openWorldHint signal reaches "likely". This enforces
		// the cap structurally rather than trusting each backend to honor it.
		conf := h.Confidence
		if conf.rank() > ConfPossible.rank() {
			conf = ConfPossible
		}
		prev := findings[h.Role]
		// Raise-only: a hint applies only when it strictly strengthens the leg.
		// Hints run before overrides (see Classify), so prev is never overridden.
		if conf.rank() <= prev.Confidence.rank() {
			continue
		}
		findings[h.Role] = RoleFinding{
			Role:       h.Role,
			Confidence: conf,
			Evidence:   append([]string{h.Reason}, prev.Evidence...),
		}
	}
}

// applyOverrides replaces findings for the named server with explicit overrides,
// recording the reason as top-priority evidence.
func applyOverrides(findings map[Role]RoleFinding, server string, overrides []Override) {
	for _, ov := range overrides {
		if ov.Server != "" && ov.Server != server {
			continue
		}
		if !isRole(ov.Role) {
			continue
		}
		reason := ov.Reason
		if reason == "" {
			reason = "(no reason given)"
		}
		prev := findings[ov.Role]
		ev := append([]string{fmt.Sprintf("overridden to %s: %s", ov.Confidence, reason)}, prev.Evidence...)
		findings[ov.Role] = RoleFinding{
			Role:       ov.Role,
			Confidence: ov.Confidence,
			Evidence:   ev,
			Overridden: true,
		}
	}
}

// matchKeyword returns the first value containing any keyword (case-insensitive),
// or "" if none match.
func matchKeyword(values, keywords []string) string {
	for _, v := range values {
		lower := strings.ToLower(v)
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				return v
			}
		}
	}
	return ""
}

// matchWord returns the first whole word in text that is in the keyword set
// (case-insensitive), or "" if none match. Word-boundary matching avoids
// substring false positives (e.g. "web" inside "cobweb").
func matchWord(text string, keywords map[string]struct{}) string {
	for _, field := range strings.Fields(strings.ToLower(text)) {
		word := strings.Trim(field, ".,;:!?()[]{}\"'`")
		if _, ok := keywords[word]; ok {
			return word
		}
	}
	return ""
}

// isRole reports whether r is one of the three recognized roles.
func isRole(r Role) bool {
	return r == RoleData || r == RoleSource || r == RoleSink
}

// isConfidence reports whether c is one of the four recognized levels.
func isConfidence(c Confidence) bool {
	return c == ConfNone || c == ConfUnknown || c == ConfPossible || c == ConfLikely
}

// ValidateOverride checks that an override is well-formed. A parse success only
// means valid JSON; an unknown confidence would otherwise map to ConfNone and
// silently zero a leg (the most dangerous direction for a security advisory),
// so reject unknown values loudly. A reason is required for auditability.
func ValidateOverride(o Override) error {
	if !isRole(o.Role) {
		return fmt.Errorf("override for server %q: invalid role %q (want data, source, or sink)", o.Server, o.Role)
	}
	if !isConfidence(o.Confidence) {
		return fmt.Errorf("override for server %q: invalid confidence %q (want none, unknown, possible, or likely)",
			o.Server, o.Confidence)
	}
	if strings.TrimSpace(o.Reason) == "" {
		return fmt.Errorf("override for server %q role %q: a reason is required", o.Server, o.Role)
	}
	return nil
}
