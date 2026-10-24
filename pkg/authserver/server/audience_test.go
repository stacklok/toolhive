// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

func TestValidateAudienceURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource string
		wantErr  bool
	}{
		{"empty is valid", "", false},
		{"valid https", "https://api.example.com", false},
		{"valid http", "http://localhost:8080", false},
		{"relative URI", "/api/resource", true},
		{"has fragment", "https://api.example.com#section", true},
		{"wrong scheme", "ftp://files.example.com", true},
		{"no host", "https://", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAudienceURI(tt.resource)
			if tt.wantErr {
				assert.Error(t, err, "resource: %q", tt.resource)
			} else {
				assert.NoError(t, err, "resource: %q", tt.resource)
			}
		})
	}
}

func TestValidateAudienceAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		resource  string
		allowlist []string
		wantErr   bool
	}{
		{"empty resource always valid", "", nil, false},
		{"nil allowlist rejects all", "https://a.com", nil, true},
		{"empty allowlist rejects all", "https://a.com", []string{}, true},
		{"exact match", "https://a.com", []string{"https://a.com"}, false},
		{"not in list", "https://a.com", []string{"https://b.com"}, true},
		{"case sensitive", "https://a.com", []string{"https://A.com"}, true},
		{"multiple with match", "https://b.com", []string{"https://a.com", "https://b.com"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAudienceAllowed(tt.resource, tt.allowlist)
			if tt.wantErr {
				assert.Error(t, err, "resource: %q, allowlist: %v", tt.resource, tt.allowlist)
			} else {
				assert.NoError(t, err, "resource: %q, allowlist: %v", tt.resource, tt.allowlist)
			}
		})
	}
}

func TestValidateSubjectIdentifierAudiences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured []string
		allowed    []string
		wantErr    bool
	}{
		{name: "disabled by default"},
		{name: "empty disables", configured: []string{}},
		{name: "exact allowed audience", configured: []string{"https://a.example"}, allowed: []string{"https://a.example"}},
		{name: "all is not special", configured: []string{"all"}, allowed: []string{"https://a.example"}, wantErr: true},
		{name: "empty audience rejected", configured: []string{""}, allowed: []string{"https://a.example"}, wantErr: true},
		{name: "non-member entry rejected", configured: []string{"relative"}, allowed: []string{"https://a.example"}, wantErr: true},
		{name: "unknown audience rejected", configured: []string{"https://b.example"}, allowed: []string{"https://a.example"}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateSubjectIdentifierAudiences(tc.configured, tc.allowed)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// resourceClient keeps a resource allowlist independent of its audiences.
type resourceClient struct {
	*fosite.DefaultClient
	resources []string
}

func (c resourceClient) Resources() []string { return c.resources }

func TestValidateClientAudience(t *testing.T) {
	t.Parallel()

	plain := func(audiences ...string) fosite.Client { return &fosite.DefaultClient{Audience: audiences} }
	scoped := func(resources ...string) fosite.Client {
		return resourceClient{DefaultClient: &fosite.DefaultClient{}, resources: resources}
	}
	tests := []struct {
		name     string
		resource string
		client   fosite.Client
		wantErr  bool
	}{
		{"empty resource always valid", "", plain(), false},
		{"client without audiences permits nothing", "https://a.com", plain(), true},
		{"resource among client audiences", "https://b.com", plain("https://a.com", "https://b.com"), false},
		{"resource outside client audiences", "https://c.com", plain("https://a.com", "https://b.com"), true},
		{"path below the client audience", "https://a.com/x", plain("https://a.com"), false},
		{"path below a deeper client audience", "https://a.com/x/y", plain("https://a.com/x"), false},
		{"sibling path is not a segment prefix", "https://a.com/xy", plain("https://a.com/x"), true},
		{"parent path is not covered", "https://a.com", plain("https://a.com/x"), true},
		{"host that merely starts with the audience host", "https://a.com.evil.com", plain("https://a.com"), true},
		{"different port", "https://a.com:8443", plain("https://a.com"), true},
		{"different scheme", "http://a.com", plain("https://a.com"), true},
		{"resource allowlist replaces audiences", "https://a.com",
			resourceClient{DefaultClient: &fosite.DefaultClient{Audience: []string{"https://a.com"}}, resources: []string{"https://b.com"}}, true},
		{"resource allowlist permits its resource", "https://b.com", scoped("https://b.com"), false},
		{"empty resource allowlist permits nothing", "https://b.com", scoped(), true},
		{"DCR client is checked like any other", "https://new.com",
			registration.MarkDCRIssued(&fosite.DefaultClient{Audience: []string{"https://old.com"}}), true},
		{"DCR client with a matching audience", "https://old.com",
			registration.MarkDCRIssued(&fosite.DefaultClient{Audience: []string{"https://old.com"}}), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateClientAudience(tt.resource, tt.client)
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrInvalidTarget)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestValidateClientAudience_AgreesWithFosite pins the property the check
// exists for: a request it accepts passes fosite's own audience check at the
// authorization code callback and on refresh, and one it refuses would fail
// that check anyway.
func TestValidateClientAudience_AgreesWithFosite(t *testing.T) {
	t.Parallel()

	audiences := [][]string{nil, {"https://a.com"}, {"https://a.com/x"}, {"https://a.com", "https://b.com/y"}}
	resources := []string{"https://a.com", "https://a.com/x", "https://a.com/xy", "https://a.com.evil.com", "https://b.com/y/z", "http://a.com"}
	for _, aud := range audiences {
		client := &fosite.DefaultClient{Audience: aud}
		for _, resource := range resources {
			ours := ValidateClientAudience(resource, client) == nil
			theirs := fosite.DefaultAudienceMatchingStrategy(client.GetAudience(), []string{resource}) == nil
			assert.Equal(t, theirs, ours, "audiences %v, resource %q", aud, resource)
		}
	}
}

func TestValidateIDJAGClientAudience(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource string
		client   fosite.Client
		wantErr  bool
	}{
		{"bare client without audiences is not narrowed", "https://a.com", &fosite.DefaultClient{}, false},
		{"bare client with a prefix match", "https://a.com/x", &fosite.DefaultClient{Audience: []string{"https://a.com"}}, false},
		{"client with audiences and a mismatch", "https://b.com", &fosite.DefaultClient{Audience: []string{"https://a.com"}}, true},
		{"resource allowlist client with an empty allowlist", "https://a.com",
			resourceClient{DefaultClient: &fosite.DefaultClient{}}, true},
		{"resource allowlist client with a match", "https://a.com/x",
			resourceClient{DefaultClient: &fosite.DefaultClient{}, resources: []string{"https://a.com"}}, false},
		{"resource allowlist client with a mismatch", "https://b.com",
			resourceClient{DefaultClient: &fosite.DefaultClient{}, resources: []string{"https://a.com"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateIDJAGClientAudience(tt.resource, tt.client)
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrInvalidTarget)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
