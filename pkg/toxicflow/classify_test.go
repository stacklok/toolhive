// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toxicflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/permissions"
	registrytypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/runner"
)

func boolPtr(b bool) *bool { return &b }

// noEgressProfile is the equivalent of the built-in "none" profile: a profile
// that was inspected and grants no outbound access.
func noEgressProfile() *permissions.Profile {
	return &permissions.Profile{
		Network: &permissions.NetworkPermissions{
			Outbound: &permissions.OutboundNetworkPermissions{},
		},
	}
}

func imageMeta(tags, tools []string) registrytypes.ServerMetadata {
	return &registrytypes.ImageMetadata{
		BaseServerMetadata: registrytypes.BaseServerMetadata{
			Tags:  tags,
			Tools: tools,
		},
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   ClassifyInput
		want    map[Role]Confidence
		wantOvr map[Role]bool // roles expected to be marked Overridden
	}{
		{
			name: "filesystem read mount marks data likely",
			input: ClassifyInput{
				Name: "fs",
				Config: &runner.RunConfig{PermissionProfile: &permissions.Profile{
					Read:    []permissions.MountDeclaration{"/home/user/notes:/notes"},
					Network: &permissions.NetworkPermissions{Outbound: &permissions.OutboundNetworkPermissions{}},
				}},
			},
			want: map[Role]Confidence{RoleData: ConfLikely, RoleSink: ConfNone, RoleSource: ConfUnknown},
		},
		{
			name: "injected secrets mark data likely",
			input: ClassifyInput{
				Name:   "svc",
				Config: &runner.RunConfig{Secrets: []string{"api-key,target=API_KEY"}, PermissionProfile: noEgressProfile()},
			},
			want: map[Role]Confidence{RoleData: ConfLikely, RoleSink: ConfNone, RoleSource: ConfUnknown},
		},
		{
			name: "no-egress profile yields confident none for data and sink",
			input: ClassifyInput{
				Name:   "inert",
				Config: &runner.RunConfig{PermissionProfile: noEgressProfile()},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfNone, RoleSource: ConfUnknown},
		},
		{
			name: "unrestricted egress marks sink likely",
			input: ClassifyInput{
				Name: "open",
				Config: &runner.RunConfig{PermissionProfile: &permissions.Profile{
					Network: &permissions.NetworkPermissions{Outbound: &permissions.OutboundNetworkPermissions{InsecureAllowAll: true}},
				}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfLikely, RoleSource: ConfUnknown},
		},
		{
			name: "host-restricted egress marks sink possible",
			input: ClassifyInput{
				Name: "scoped",
				Config: &runner.RunConfig{PermissionProfile: &permissions.Profile{
					Network: &permissions.NetworkPermissions{Outbound: &permissions.OutboundNetworkPermissions{AllowHost: []string{"api.example.com"}}},
				}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfPossible, RoleSource: ConfUnknown},
		},
		{
			name: "host network mode marks sink likely",
			input: ClassifyInput{
				Name: "hostnet",
				Config: &runner.RunConfig{PermissionProfile: &permissions.Profile{
					Network: &permissions.NetworkPermissions{Mode: "host", Outbound: &permissions.OutboundNetworkPermissions{}},
				}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfLikely, RoleSource: ConfUnknown},
		},
		{
			name: "privileged container marks sink likely",
			input: ClassifyInput{
				Name: "priv",
				Config: &runner.RunConfig{PermissionProfile: &permissions.Profile{
					Privileged: true,
					Network:    &permissions.NetworkPermissions{Outbound: &permissions.OutboundNetworkPermissions{}},
				}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfLikely, RoleSource: ConfUnknown},
		},
		{
			name: "remote url marks sink likely but not data (no double-count)",
			input: ClassifyInput{
				Name:   "remote",
				Config: &runner.RunConfig{RemoteURL: "https://mcp.example.com/sse", PermissionProfile: noEgressProfile()},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfLikely, RoleSource: ConfUnknown},
		},
		{
			name: "nil network policy reads sink possible (open egress by default)",
			input: ClassifyInput{
				Name:   "nonet",
				Config: &runner.RunConfig{PermissionProfile: &permissions.Profile{}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfPossible, RoleSource: ConfUnknown},
		},
		{
			name: "nil permission profile reads sink possible (open egress by default)",
			input: ClassifyInput{
				Name:   "noprofile",
				Config: &runner.RunConfig{},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfPossible, RoleSource: ConfUnknown},
		},
		{
			name: "nil outbound policy reads sink possible (open egress by default)",
			input: ClassifyInput{
				Name: "nooutbound",
				Config: &runner.RunConfig{PermissionProfile: &permissions.Profile{
					Network: &permissions.NetworkPermissions{},
				}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfPossible, RoleSource: ConfUnknown},
		},
		{
			name: "metadata without a run config leaves data and sink unknown",
			input: ClassifyInput{
				Name:     "noconfig",
				Metadata: imageMeta(nil, nil),
			},
			want: map[Role]Confidence{RoleData: ConfUnknown, RoleSink: ConfUnknown, RoleSource: ConfUnknown},
		},
		{
			name: "source hint raises source to possible",
			input: ClassifyInput{
				Name:   "fetcher",
				Config: &runner.RunConfig{PermissionProfile: noEgressProfile()},
				Hints:  []Hint{{Role: RoleSource, Confidence: ConfPossible, Reason: "tag \"web\""}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfNone, RoleSource: ConfPossible},
		},
		{
			name: "data hint raises remote data from none to possible",
			input: ClassifyInput{
				Name:   "remote",
				Config: &runner.RunConfig{RemoteURL: "https://mcp.example.com/sse", PermissionProfile: noEgressProfile()},
				Hints:  []Hint{{Role: RoleData, Confidence: ConfPossible, Reason: "LLM: holds account data"}},
			},
			want: map[Role]Confidence{RoleData: ConfPossible, RoleSink: ConfLikely, RoleSource: ConfUnknown},
		},
		{
			name: "hint stronger than possible is clamped to possible (cap enforced at the seam)",
			input: ClassifyInput{
				Name:   "fetcher",
				Config: &runner.RunConfig{PermissionProfile: noEgressProfile()},
				Hints:  []Hint{{Role: RoleSource, Confidence: ConfLikely, Reason: "overconfident backend"}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfNone, RoleSource: ConfPossible},
		},
		{
			name: "hint never lowers a stronger finding (raise-only)",
			input: ClassifyInput{
				Name:        "browser",
				Config:      &runner.RunConfig{PermissionProfile: noEgressProfile()},
				Annotations: map[string]*authorizers.ToolAnnotations{"browse": {OpenWorldHint: boolPtr(true)}},
				Hints:       []Hint{{Role: RoleSource, Confidence: ConfPossible, Reason: "weaker hint"}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfNone, RoleSource: ConfLikely},
		},
		{
			name: "openWorldHint annotation marks source likely",
			input: ClassifyInput{
				Name:        "browser",
				Config:      &runner.RunConfig{PermissionProfile: noEgressProfile()},
				Annotations: map[string]*authorizers.ToolAnnotations{"browse": {OpenWorldHint: boolPtr(true)}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfNone, RoleSource: ConfLikely},
		},
		{
			name: "annotations without openWorldHint leave source unknown (a server cannot self-declare safe)",
			input: ClassifyInput{
				Name:        "calc",
				Config:      &runner.RunConfig{PermissionProfile: noEgressProfile()},
				Annotations: map[string]*authorizers.ToolAnnotations{"add": {OpenWorldHint: boolPtr(false)}},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfNone, RoleSource: ConfUnknown},
		},
		{
			name:  "no inputs leave every role unknown",
			input: ClassifyInput{Name: "ghost"},
			want:  map[Role]Confidence{RoleData: ConfUnknown, RoleSink: ConfUnknown, RoleSource: ConfUnknown},
		},
		{
			name: "override downgrades source to none even against a raising hint",
			input: ClassifyInput{
				Name:   "intranet-fetch",
				Config: &runner.RunConfig{PermissionProfile: noEgressProfile()},
				Hints:  []Hint{{Role: RoleSource, Confidence: ConfPossible, Reason: "tag \"web\""}},
				Overrides: []Override{
					{Server: "intranet-fetch", Role: RoleSource, Confidence: ConfNone, Reason: "first-party intranet only"},
				},
			},
			want:    map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfNone, RoleSource: ConfNone},
			wantOvr: map[Role]bool{RoleSource: true},
		},
		{
			name: "override targeting a different server is ignored",
			input: ClassifyInput{
				Name:   "fetcher",
				Config: &runner.RunConfig{PermissionProfile: noEgressProfile()},
				Hints:  []Hint{{Role: RoleSource, Confidence: ConfPossible, Reason: "tag \"web\""}},
				Overrides: []Override{
					{Server: "other", Role: RoleSource, Confidence: ConfNone, Reason: "not this one"},
				},
			},
			want: map[Role]Confidence{RoleData: ConfNone, RoleSink: ConfNone, RoleSource: ConfPossible},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Classify(tt.input)
			require.Equal(t, tt.input.Name, got.Name)
			for role, wantConf := range tt.want {
				assert.Equalf(t, wantConf, got.Finding(role).Confidence,
					"role %s confidence", role)
			}
			for role, wantOvr := range tt.wantOvr {
				assert.Equalf(t, wantOvr, got.Finding(role).Overridden,
					"role %s overridden", role)
			}
		})
	}
}

func TestValidateOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ovr     Override
		wantErr bool
	}{
		{
			name: "valid override",
			ovr:  Override{Server: "fetch", Role: RoleSource, Confidence: ConfNone, Reason: "intranet only"},
		},
		{
			name: "wildcard server is allowed",
			ovr:  Override{Server: "", Role: RoleSink, Confidence: ConfNone, Reason: "all egress audited"},
		},
		{
			name:    "invalid role is rejected",
			ovr:     Override{Server: "x", Role: "egress", Confidence: ConfNone, Reason: "r"},
			wantErr: true,
		},
		{
			name:    "invalid confidence is rejected",
			ovr:     Override{Server: "x", Role: RoleSink, Confidence: "high", Reason: "r"},
			wantErr: true,
		},
		{
			name:    "missing reason is rejected",
			ovr:     Override{Server: "x", Role: RoleSink, Confidence: ConfNone, Reason: "  "},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateOverride(tt.ovr)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
