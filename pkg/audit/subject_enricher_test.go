// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
)

// enricherTestIdentity carries a subject and a name but no client_name or
// client_version claims, so a reserved key the enricher returns has no
// built-in value to hide behind.
func enricherTestIdentity() *auth.Identity {
	return &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{
			Subject: "user-123",
			Name:    "Jane Doe",
			Claims:  map[string]any{"cid": "okta-client"},
		},
	}
}

// subjectEnricherCases is shared by the HTTP and workflow auditor tests so
// both paths are held to the same merge rules.
var subjectEnricherCases = []struct {
	name         string
	identity     *auth.Identity
	enricher     SubjectEnricher
	wantSubjects map[string]any
}{
	{
		name:     "no enricher leaves subjects unchanged",
		identity: enricherTestIdentity(),
		enricher: nil,
		wantSubjects: map[string]any{
			SubjectKeyUserID: "user-123",
			SubjectKeyUser:   "Jane Doe",
		},
	},
	{
		name:     "enricher subject is added",
		identity: enricherTestIdentity(),
		enricher: func(context.Context, *auth.Identity) map[string]string {
			return map[string]string{"client_id": "abc"}
		},
		wantSubjects: map[string]any{
			SubjectKeyUserID: "user-123",
			SubjectKeyUser:   "Jane Doe",
			"client_id":      "abc",
		},
	},
	{
		name:     "enricher reads the caller identity",
		identity: enricherTestIdentity(),
		enricher: func(_ context.Context, identity *auth.Identity) map[string]string {
			cid, _ := identity.Claims["cid"].(string)
			return map[string]string{"client_id": cid}
		},
		wantSubjects: map[string]any{
			SubjectKeyUserID: "user-123",
			SubjectKeyUser:   "Jane Doe",
			"client_id":      "okta-client",
		},
	},
	{
		name:     "reserved keys are never taken from the enricher",
		identity: enricherTestIdentity(),
		enricher: func(context.Context, *auth.Identity) map[string]string {
			return map[string]string{
				SubjectKeyUser:          "x",
				SubjectKeyUserID:        "spoofed-id",
				SubjectKeyClientName:    "spoofed-client",
				SubjectKeyClientVersion: "9.9.9",
				"principal_type":        "user",
			}
		},
		wantSubjects: map[string]any{
			SubjectKeyUserID: "user-123",
			SubjectKeyUser:   "Jane Doe",
			"principal_type": "user",
		},
	},
	{
		name:     "empty keys and values are dropped",
		identity: enricherTestIdentity(),
		enricher: func(context.Context, *auth.Identity) map[string]string {
			return map[string]string{"tenant": "", "": "orphan", "client_id": "abc"}
		},
		wantSubjects: map[string]any{
			SubjectKeyUserID: "user-123",
			SubjectKeyUser:   "Jane Doe",
			"client_id":      "abc",
		},
	},
	{
		name:     "nil result adds nothing",
		identity: enricherTestIdentity(),
		enricher: func(context.Context, *auth.Identity) map[string]string {
			return nil
		},
		wantSubjects: map[string]any{
			SubjectKeyUserID: "user-123",
			SubjectKeyUser:   "Jane Doe",
		},
	},
	{
		name:     "enricher runs with a nil identity for anonymous callers",
		identity: nil,
		enricher: func(_ context.Context, identity *auth.Identity) map[string]string {
			if identity != nil {
				return map[string]string{"principal_type": "unexpected"}
			}
			return map[string]string{"principal_type": "anonymous"}
		},
		wantSubjects: map[string]any{
			SubjectKeyUser:   "anonymous",
			"principal_type": "anonymous",
		},
	},
}

// eventSubjects decodes the single audit event in buf and returns its
// subjects object.
func eventSubjects(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry), "audit output: %s", buf.String())
	subjects, ok := entry["subjects"].(map[string]any)
	require.True(t, ok, "event has no subjects object: %s", buf.String())
	return subjects
}

func TestAuditorMiddleware_SubjectEnricher(t *testing.T) {
	t.Parallel()

	for _, tt := range subjectEnricherCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auditor, err := NewAuditorWithTransport(&Config{}, "streamable-http", WithSubjectEnricher(tt.enricher))
			require.NoError(t, err)
			var buf bytes.Buffer
			auditor.auditLogger = NewAuditLogger(&buf)

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.identity != nil {
				req = req.WithContext(auth.WithIdentity(req.Context(), tt.identity))
			}
			handler := auditor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), req)

			assert.Equal(t, tt.wantSubjects, eventSubjects(t, &buf))
		})
	}
}

func TestWorkflowAuditor_SubjectEnricher(t *testing.T) {
	t.Parallel()

	for _, tt := range subjectEnricherCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auditor, err := NewWorkflowAuditor(&Config{}, WithSubjectEnricher(tt.enricher))
			require.NoError(t, err)
			var buf bytes.Buffer
			auditor.auditLogger = NewAuditLogger(&buf)

			ctx := context.Background()
			if tt.identity != nil {
				ctx = auth.WithIdentity(ctx, tt.identity)
			}
			auditor.LogWorkflowStarted(ctx, "wf-1", "wf", nil, time.Second)

			assert.Equal(t, tt.wantSubjects, eventSubjects(t, &buf))
		})
	}
}

// TestSubjectEnricher_ReceivesRequestContext pins that the HTTP auditor hands
// the enricher the request's context, so an embedder can read request-scoped
// values it attached upstream.
func TestSubjectEnricher_ReceivesRequestContext(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}
	enricher := func(ctx context.Context, _ *auth.Identity) map[string]string {
		v, _ := ctx.Value(ctxKey{}).(string)
		return map[string]string{"tenant": v}
	}

	auditor, err := NewAuditorWithTransport(&Config{}, "streamable-http", WithSubjectEnricher(enricher))
	require.NoError(t, err)
	var buf bytes.Buffer
	auditor.auditLogger = NewAuditLogger(&buf)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, "acme"))
	auditor.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "acme", eventSubjects(t, &buf)["tenant"])
}

// TestReservedSubjectKeys_CoverBuiltInSubjects pins reservedSubjectKeys to the
// keys extractSubjectsFromIdentity writes, so a new built-in subject cannot be
// added without also protecting it from enricher overwrites.
func TestReservedSubjectKeys_CoverBuiltInSubjects(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{
			Subject: "user-123",
			Name:    "Jane Doe",
			Email:   "jane@example.com",
			Claims: map[string]any{
				"preferred_username": "jdoe",
				"client_name":        "mcp-client",
				"client_version":     "1.0.0",
			},
		},
	}

	subjects := extractSubjectsFromIdentity(identity)
	require.Len(t, subjects, len(reservedSubjectKeys),
		"every reserved key should be written by extractSubjectsFromIdentity and vice versa")
	for key := range subjects {
		assert.Contains(t, reservedSubjectKeys, key, "built-in subject %q must be reserved", key)
	}
}
