// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toxicflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// sa builds a server assessment with explicit confidences for each role.
func sa(name string, data, source, sink Confidence) ServerAssessment {
	return ServerAssessment{Name: name, Findings: map[Role]RoleFinding{
		RoleData:   {Role: RoleData, Confidence: data},
		RoleSource: {Role: RoleSource, Confidence: source},
		RoleSink:   {Role: RoleSink, Confidence: sink},
	}}
}

func TestAnalyzeGroupVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		assessments []ServerAssessment
		want        Verdict
	}{
		{
			name:        "all roles likely on one server is present",
			assessments: []ServerAssessment{sa("all", ConfLikely, ConfLikely, ConfLikely)},
			want:        VerdictPresent,
		},
		{
			name: "roles split across servers still forms present",
			assessments: []ServerAssessment{
				sa("data-and-sink", ConfLikely, ConfNone, ConfLikely),
				sa("source", ConfNone, ConfLikely, ConfNone),
			},
			want: VerdictPresent,
		},
		{
			name: "weakest leg only possible yields possible",
			assessments: []ServerAssessment{
				sa("data-sink", ConfLikely, ConfNone, ConfLikely),
				sa("weak-source", ConfNone, ConfPossible, ConfNone),
			},
			want: VerdictPossible,
		},
		{
			name: "unknown source with data and sink yields indeterminate",
			assessments: []ServerAssessment{
				sa("github", ConfLikely, ConfUnknown, ConfLikely),
			},
			want: VerdictIndeterminate,
		},
		{
			name: "confident absence of a leg yields none",
			assessments: []ServerAssessment{
				sa("data-sink", ConfLikely, ConfNone, ConfLikely),
				sa("inert", ConfNone, ConfNone, ConfNone),
			},
			want: VerdictNone,
		},
		{
			name:        "empty group is none",
			assessments: nil,
			want:        VerdictNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := AnalyzeGroup("g", tt.assessments)
			assert.Equal(t, tt.want, got.Verdict)
		})
	}
}

func TestAnalyzeGroupContributors(t *testing.T) {
	t.Parallel()

	got := AnalyzeGroup("research", []ServerAssessment{
		sa("notes", ConfLikely, ConfNone, ConfNone),       // data only
		sa("web-fetch", ConfNone, ConfLikely, ConfLikely), // source + sink
		sa("calc", ConfNone, ConfNone, ConfNone),          // nothing
	})

	assert.Equal(t, []string{"notes"}, got.DataHolders)
	assert.Equal(t, []string{"web-fetch"}, got.Sources)
	assert.Equal(t, []string{"web-fetch"}, got.Sinks)
	assert.Empty(t, got.Unclassified)
}

func TestAnalyzeGroupSelfContainedFlow(t *testing.T) {
	t.Parallel()

	// One server holds all three roles: a self-contained flow.
	self := AnalyzeGroup("solo", []ServerAssessment{
		sa("kitchen-sink", ConfLikely, ConfLikely, ConfLikely),
		sa("inert", ConfNone, ConfNone, ConfNone),
	})
	assert.Equal(t, VerdictPresent, self.Verdict)
	assert.Equal(t, []string{"kitchen-sink"}, self.SelfContainedFlow)

	// Flow spread across three single-role servers: no self-contained flow.
	spread := AnalyzeGroup("spread", []ServerAssessment{
		sa("d", ConfLikely, ConfNone, ConfNone),
		sa("s", ConfNone, ConfLikely, ConfNone),
		sa("k", ConfNone, ConfNone, ConfLikely),
	})
	assert.Equal(t, VerdictPresent, spread.Verdict)
	assert.Empty(t, spread.SelfContainedFlow)
}

func TestAnalyzeGroupUnclassified(t *testing.T) {
	t.Parallel()

	got := AnalyzeGroup("mixed", []ServerAssessment{
		sa("github", ConfLikely, ConfUnknown, ConfLikely),
		sa("notes", ConfLikely, ConfNone, ConfNone),
	})

	assert.Equal(t, VerdictIndeterminate, got.Verdict)
	assert.Equal(t, []string{"github"}, got.Unclassified)
}
