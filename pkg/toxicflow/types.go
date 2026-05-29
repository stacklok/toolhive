// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package toxicflow assesses a ToolHive group (a set of MCP servers) for
// "lethal trifecta" risk: the co-location, within a single agent context, of
// access to private data, exposure to untrusted content, and the ability to
// exfiltrate. Following information-flow terminology, the hazard is modelled as
// a toxic flow from an untrusted-content source, through a private-data holder,
// to an exfiltration sink. Because every server in a group shares the one model
// context, a flow exists whenever the group contains all three roles.
//
// The package separates a pure classifier (Classify) and a pure analyzer
// (AnalyzeGroup) from the impure data collection (Collector) so the heuristics
// can be unit-tested without a running ToolHive.
//
// ToolHive observes permission profiles, network egress, and registry metadata
// — not the model's reasoning. It therefore classifies the data and sink roles
// with reasonable confidence (they derive from the permission profile) but the
// source role poorly: untrusted-content ingestion has no first-class signal, so
// the source role defaults to "unknown" rather than "none".
package toxicflow

// Role is the part a server can play in a toxic flow. A single server may hold
// more than one role (e.g. a web-fetch server is both a source and a sink).
type Role string

const (
	// RoleData marks a server with access to private or sensitive data.
	RoleData Role = "data"
	// RoleSource marks a server that ingests potentially untrusted content,
	// i.e. a prompt-injection delivery vector.
	RoleSource Role = "source"
	// RoleSink marks a server able to communicate externally (exfiltrate).
	RoleSink Role = "sink"
)

// AllRoles is the canonical role order used for deterministic output.
var AllRoles = []Role{RoleData, RoleSource, RoleSink}

// Confidence expresses how strongly the evidence supports a role. The ordering
// none < unknown < possible < likely is significant: "unknown" means ToolHive
// lacked the inputs to rule the role in or out, whereas "none" is a confident
// absence (we inspected the relevant inputs and found nothing).
type Confidence string

const (
	// ConfNone is a confident absence — the inputs were available and clear.
	ConfNone Confidence = "none"
	// ConfUnknown means the role could not be assessed from available inputs.
	ConfUnknown Confidence = "unknown"
	// ConfPossible means weak/inferred evidence supports the role.
	ConfPossible Confidence = "possible"
	// ConfLikely means strong evidence supports the role.
	ConfLikely Confidence = "likely"
)

// rank returns the ordinal of a confidence level for comparison.
func (c Confidence) rank() int {
	switch c {
	case ConfLikely:
		return 3
	case ConfPossible:
		return 2
	case ConfUnknown:
		return 1
	case ConfNone:
		return 0
	default:
		return 0
	}
}

// atOrAbove reports whether c is as strong as, or stronger than, other.
func (c Confidence) atOrAbove(other Confidence) bool {
	return c.rank() >= other.rank()
}

// RoleFinding is the assessment of a single role for a single server.
type RoleFinding struct {
	Role       Role       `json:"role"`
	Confidence Confidence `json:"confidence"`
	// Evidence holds human-readable reasons supporting the confidence level.
	Evidence []string `json:"evidence,omitempty"`
	// Overridden is true when an explicit override set this finding.
	Overridden bool `json:"overridden,omitempty"`
}

// ServerAssessment is the per-server result of classification.
type ServerAssessment struct {
	Name     string               `json:"name"`
	Findings map[Role]RoleFinding `json:"findings"`
}

// Finding returns the finding for a role, defaulting to a ConfNone finding when
// absent so callers never have to nil-check.
func (s ServerAssessment) Finding(role Role) RoleFinding {
	if f, ok := s.Findings[role]; ok {
		return f
	}
	return RoleFinding{Role: role, Confidence: ConfNone}
}

// Verdict is the group-level conclusion.
type Verdict string

const (
	// VerdictNone means at least one role is confidently absent, so no toxic
	// flow can form in this group.
	VerdictNone Verdict = "none"
	// VerdictPossible means all three roles are present at possible-or-likely
	// confidence, but not all reach "likely".
	VerdictPossible Verdict = "possible"
	// VerdictPresent means all three roles are present at "likely" confidence.
	VerdictPresent Verdict = "present"
	// VerdictIndeterminate means a role cannot be confirmed or ruled out
	// (stuck at "unknown"), so the group may or may not form a toxic flow.
	VerdictIndeterminate Verdict = "indeterminate"
)

// GroupAssessment is the result of analyzing a whole group.
type GroupAssessment struct {
	Group   string             `json:"group"`
	Verdict Verdict            `json:"verdict"`
	Servers []ServerAssessment `json:"servers"`
	// Sources, DataHolders and Sinks list the servers contributing each role
	// at possible-or-likely confidence — the attack path at group granularity.
	Sources     []string `json:"sources,omitempty"`
	DataHolders []string `json:"data_holders,omitempty"`
	Sinks       []string `json:"sinks,omitempty"`
	// Unclassified lists servers whose source role could not be assessed; these
	// are why a verdict may be indeterminate.
	Unclassified []string `json:"unclassified,omitempty"`
	// SelfContainedFlow names servers that hold all three roles at
	// possible-or-likely confidence on their own. Such a server forms a toxic
	// flow by itself; the cheapest fix is to tighten its permission profile,
	// not to split the group.
	SelfContainedFlow []string `json:"self_contained_flow,omitempty"`
}

// Override forces a role's confidence for a named server, e.g. to correct a
// mis-classified common server. A reason is required for auditability.
type Override struct {
	Server     string     `json:"server"`
	Role       Role       `json:"role"`
	Confidence Confidence `json:"confidence"`
	Reason     string     `json:"reason"`
}
