// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
)

// TestCallTool_CompositeWorkflowAuditCarriesEnricherSubjects verifies that the
// AuditSubjectEnricher set on core.Config reaches the workflow auditor, and that
// workflow audit events, the enricher, and the workflow's backend calls all see
// the identity passed to CallTool rather than any identity in ctx, as the VMCP
// contract requires. With AuditConfig nil the enricher never runs.
func TestCallTool_CompositeWorkflowAuditCarriesEnricherSubjects(t *testing.T) {
	t.Parallel()

	jane := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "user-123", Name: "Jane Doe", Claims: map[string]any{"cid": "jane-client"},
	}}
	mallory := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "user-666", Name: "Mallory", Claims: map[string]any{"cid": "mallory-client"},
	}}

	tests := []struct {
		name         string
		ctxIdentity  *auth.Identity
		identity     *auth.Identity
		noAudit      bool
		wantSubjects map[string]string
		wantBackend  string // Subject the backend call sees in ctx; "" for none
	}{
		{
			name:     "identity passed only as a parameter",
			identity: jane,
			wantSubjects: map[string]string{
				audit.SubjectKeyUserID: "user-123",
				audit.SubjectKeyUser:   "Jane Doe",
				"client_id":            "jane-client",
			},
			wantBackend: "user-123",
		},
		{
			name:        "parameter wins over a conflicting context identity",
			ctxIdentity: mallory,
			identity:    jane,
			wantSubjects: map[string]string{
				audit.SubjectKeyUserID: "user-123",
				audit.SubjectKeyUser:   "Jane Doe",
				"client_id":            "jane-client",
			},
			wantBackend: "user-123",
		},
		{
			name:        "nil identity stays anonymous despite a context identity",
			ctxIdentity: mallory,
			identity:    nil,
			wantSubjects: map[string]string{
				audit.SubjectKeyUser: "anonymous",
				"client_id":          "none",
			},
			wantBackend: "",
		},
		{
			name:        "enricher never runs without AuditConfig",
			identity:    jane,
			noAudit:     true,
			wantBackend: "user-123",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, m := baseConfig(t)
			var auditLogPath string
			if !tt.noAudit {
				auditCfg, path := fileAuditConfig(t)
				auditCfg.EventTypes = []string{audit.EventTypeWorkflowStarted}
				cfg.AuditConfig = auditCfg
				auditLogPath = path
			}
			var enricherCalls atomic.Int32
			cfg.AuditSubjectEnricher = func(ctx context.Context) map[string]string {
				enricherCalls.Add(1)
				id, ok := auth.IdentityFromContext(ctx)
				if !ok {
					return map[string]string{"client_id": "none"}
				}
				cid, _ := id.Claims["cid"].(string)
				return map[string]string{"client_id": cid}
			}
			cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{
				"wf": {Name: "wf", Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.echo"}}},
			}

			target := backendTarget()
			expectAggregation(m, &aggregator.AggregatedCapabilities{
				Tools:        []vmcp.Tool{backendTool("be1.echo")},
				RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"be1.echo": target}},
			})
			var backendSubject string
			m.client.EXPECT().
				CallTool(gomock.Any(), gomock.Any(), "be1.echo", gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, _ *vmcp.BackendTarget, _ string, _, _ map[string]any, _ map[string]string) (*vmcp.ToolCallResult, error) {
					if id, ok := auth.IdentityFromContext(ctx); ok {
						backendSubject = id.Subject
					}
					return &vmcp.ToolCallResult{StructuredContent: map[string]any{"ok": true}}, nil
				})

			c, err := New(cfg)
			require.NoError(t, err)

			ctx := context.Background()
			if tt.ctxIdentity != nil {
				ctx = auth.WithIdentity(ctx, tt.ctxIdentity)
			}
			_, err = c.CallTool(ctx, tt.identity, "wf", nil, nil)
			require.NoError(t, err)
			// Close flushes and releases the audit log before it is read.
			require.NoError(t, c.Close())

			assert.Equal(t, tt.wantBackend, backendSubject)
			if tt.noAudit {
				assert.Zero(t, enricherCalls.Load())
				return
			}

			content, err := os.ReadFile(auditLogPath)
			require.NoError(t, err)
			scanner := bufio.NewScanner(bytes.NewReader(content))
			require.True(t, scanner.Scan(), "expected a workflow_started audit event")

			var entry struct {
				Type     string            `json:"type"`
				Subjects map[string]string `json:"subjects"`
			}
			require.NoError(t, json.Unmarshal(scanner.Bytes(), &entry))
			assert.Equal(t, audit.EventTypeWorkflowStarted, entry.Type)
			assert.Equal(t, tt.wantSubjects, entry.Subjects)
		})
	}
}
