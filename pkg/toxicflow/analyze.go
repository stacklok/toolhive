// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toxicflow

// AnalyzeGroup folds per-server assessments into a group verdict. Because every
// server in a group shares the one agent context, a toxic flow exists whenever
// the group contains all three roles; the verdict reflects the weakest of the
// three (the flow is only as strong as its least-confident leg). It is a pure
// function.
func AnalyzeGroup(group string, assessments []ServerAssessment) GroupAssessment {
	ga := GroupAssessment{Group: group, Servers: assessments}

	best := map[Role]Confidence{
		RoleData:   ConfNone,
		RoleSource: ConfNone,
		RoleSink:   ConfNone,
	}

	for _, s := range assessments {
		for _, role := range AllRoles {
			f := s.Finding(role)
			if f.Confidence.rank() > best[role].rank() {
				best[role] = f.Confidence
			}
			if f.Confidence.atOrAbove(ConfPossible) {
				switch role {
				case RoleData:
					ga.DataHolders = append(ga.DataHolders, s.Name)
				case RoleSource:
					ga.Sources = append(ga.Sources, s.Name)
				case RoleSink:
					ga.Sinks = append(ga.Sinks, s.Name)
				}
			}
		}
		if hasUnknownRole(s) {
			ga.Unclassified = append(ga.Unclassified, s.Name)
		}
		if holdsAllRoles(s) {
			ga.SelfContainedFlow = append(ga.SelfContainedFlow, s.Name)
		}
	}

	ga.Verdict = verdict(best[RoleData], best[RoleSource], best[RoleSink])
	return ga
}

// verdict derives the group conclusion from the strongest confidence found for
// each role. The flow is gated by its weakest leg:
//   - all legs likely        -> present
//   - all legs >= possible    -> possible
//   - weakest leg unknown     -> indeterminate (cannot confirm or rule out)
//   - any leg confidently none -> none (flow broken)
func verdict(data, source, sink Confidence) Verdict {
	weakest := min(data.rank(), source.rank(), sink.rank())
	switch {
	case weakest >= ConfLikely.rank():
		return VerdictPresent
	case weakest >= ConfPossible.rank():
		return VerdictPossible
	case weakest >= ConfUnknown.rank():
		return VerdictIndeterminate
	default:
		return VerdictNone
	}
}

// hasUnknownRole reports whether any of the server's roles could not be assessed.
func hasUnknownRole(s ServerAssessment) bool {
	for _, role := range AllRoles {
		if s.Finding(role).Confidence == ConfUnknown {
			return true
		}
	}
	return false
}

// holdsAllRoles reports whether a single server holds all three roles at
// possible-or-likely confidence, forming a toxic flow on its own.
func holdsAllRoles(s ServerAssessment) bool {
	for _, role := range AllRoles {
		if !s.Finding(role).Confidence.atOrAbove(ConfPossible) {
			return false
		}
	}
	return true
}
