// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
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
// AuditSubjectEnricher set on core.Config reaches the workflow auditor, so
// composite-tool workflow events carry the embedder's subjects.
func TestCallTool_CompositeWorkflowAuditCarriesEnricherSubjects(t *testing.T) {
	t.Parallel()

	cfg, m := baseConfig(t)
	auditCfg, auditLogPath := fileAuditConfig(t)
	auditCfg.EventTypes = []string{audit.EventTypeWorkflowStarted}
	cfg.AuditConfig = auditCfg
	cfg.AuditSubjectEnricher = func(context.Context, *auth.Identity) map[string]string {
		return map[string]string{"client_id": "abc"}
	}
	cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{
		"wf": {Name: "wf", Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.echo"}}},
	}

	target := backendTarget()
	expectAggregation(m, &aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{backendTool("be1.echo")},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"be1.echo": target}},
	})
	m.client.EXPECT().
		CallTool(gomock.Any(), gomock.Any(), "be1.echo", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&vmcp.ToolCallResult{StructuredContent: map[string]any{"ok": true}}, nil)

	c, err := New(cfg)
	require.NoError(t, err)

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-123", Name: "Jane Doe"}}
	ctx := auth.WithIdentity(context.Background(), identity)
	_, err = c.CallTool(ctx, identity, "wf", nil, nil)
	require.NoError(t, err)
	// Close flushes and releases the audit log before it is read.
	require.NoError(t, c.Close())

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
	assert.Equal(t, map[string]string{
		audit.SubjectKeyUserID: "user-123",
		audit.SubjectKeyUser:   "Jane Doe",
		"client_id":            "abc",
	}, entry.Subjects)
}
