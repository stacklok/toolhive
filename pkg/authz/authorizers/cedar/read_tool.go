// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cedar

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/cedar-policy/cedar-go/x/exp/ast"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// readToolActionEntity is the Cedar action that grants tool visibility on tools/list
// without allowing the tool to be called.
const readToolActionEntity = "Action::read_tool"

// readToolActionQuoted is how cedar-go writes the read_tool action in a policy's text.
const readToolActionQuoted = `Action::"read_tool"`

// callToolActionEntity is the Cedar action that allows a tool to be called.
const callToolActionEntity = "Action::call_tool"

// AnswersToolRead reports that this authorizer decides tool:read itself. The
// tool:call fallback in the decorator must not apply: a read_tool forbid has to
// hide the tool even when call_tool allows it.
func (*Authorizer) AnswersToolRead() bool { return true }

// actionScopeNamesReadTool reports whether the policy's action scope names
// Action::"read_tool" directly, with ==, in, or in a set. A scope of "action" with no
// constraint does not name it, so an unscoped permit grants no visibility.
func actionScopeNamesReadTool(p *cedar.Policy) bool {
	switch scope := p.AST().Action.(type) {
	case ast.ScopeTypeEq:
		return isReadToolUID(scope.Entity)
	case ast.ScopeTypeIn:
		return isReadToolUID(scope.Entity)
	case ast.ScopeTypeInSet:
		for _, e := range scope.Entities {
			if isReadToolUID(e) {
				return true
			}
		}
	}
	return false
}

func isReadToolUID(e cedar.EntityUID) bool {
	return e.Type == "Action" && e.ID == "read_tool"
}

// readToolPolicySet returns the policies that can affect tool:read visibility: the
// permits and forbids whose action scope names read_tool. Everything else is left out,
// so an existing policy set without read_tool keeps its current visibility.
// A forbid that mentions read_tool in its conditions but does not name it in its scope is
// ignored; it is logged so the author can see the gap.
func readToolPolicySet(ps *cedar.PolicySet) *cedar.PolicySet {
	out := cedar.NewPolicySet()
	if ps == nil {
		return out
	}
	for id, p := range ps.All() {
		if actionScopeNamesReadTool(p) {
			out.Add(id, p)
			continue
		}
		if mentionsReadToolOutsideScope(p) {
			slog.Warn("cedar forbid mentions Action::\"read_tool\" without naming it in its action scope; "+
				"it does not hide tools from tools/list",
				"policy", id)
		}
	}
	return out
}

// mentionsReadToolOutsideScope reports whether a forbid refers to Action::"read_tool"
// in its text while its action scope does not name it. Such a forbid is left out of the
// read set, so the author should know it does not hide tools from tools/list.
func mentionsReadToolOutsideScope(p *cedar.Policy) bool {
	return p.Effect() == cedar.Forbid &&
		!actionScopeNamesReadTool(p) &&
		strings.Contains(string(p.MarshalCedar()), readToolActionQuoted)
}

// erroredForbid reports whether a Cedar evaluation error must fail the check closed.
// An error from a permit only means that permit did not match (Cedar already treats it
// that way), so a read_tool permit can still list a tool that a call_tool permit
// errors on. An error from a forbid, or one with no policy to attribute it to, fails
// closed so a forbid that cannot be evaluated never stops hiding its tool.
func erroredForbid(ps *cedar.PolicySet, diag cedar.Diagnostic) bool {
	for _, e := range diag.Errors {
		p := ps.Get(e.PolicyID)
		if p == nil || p.Effect() != cedar.Permit {
			return true
		}
	}
	return false
}

// authorizeToolRead decides whether a tool is listed on tools/list.
//
// A tool is listed when either:
//   - the call_tool check allows it and no read_tool-scoped forbid matches, or
//   - a permit scoped to read_tool matches it and no forbid matches for call_tool.
//
// Forbids always win. A policy set with no read_tool-scoped policy gives the same
// result as the call check alone. An error from Cedar on either check fails closed:
// the tool is not listed.
func (a *Authorizer) authorizeToolRead(
	ctx context.Context,
	clientID, toolName string,
	claimsMap map[string]interface{},
	attrsMap map[string]interface{},
	groups []string,
) (bool, error) {
	callPrincipal, callResource, callContext, callEntities, err := a.toolRequest(
		ctx, clientID, toolName, callToolActionEntity, "call", claimsMap, attrsMap, groups)
	if err != nil {
		return false, err
	}
	readPrincipal, readResource, readContext, readEntities, err := a.toolRequest(
		ctx, clientID, toolName, readToolActionEntity, "read", claimsMap, attrsMap, groups)
	if err != nil {
		return false, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	callDecision, callDiag, err := a.evaluate(
		a.policySet, callPrincipal, callToolActionEntity, callResource, callContext, callEntities)
	if err != nil {
		return false, err
	}
	if erroredForbid(a.policySet, callDiag) {
		return false, fmt.Errorf("authorization error: %v", callDiag.Errors)
	}

	readDecision, readDiag, err := a.evaluate(
		a.readSet, readPrincipal, readToolActionEntity, readResource, readContext, readEntities)
	if err != nil {
		return false, err
	}
	if len(readDiag.Errors) > 0 {
		return false, fmt.Errorf("authorization error: %v", readDiag.Errors)
	}

	// Deny with reasons means a forbid matched; deny with no reasons means no permit matched.
	callAllowed := callDecision == cedar.Allow
	callForbidden := callDecision == cedar.Deny && len(callDiag.Reasons) > 0
	readAllowed := readDecision == cedar.Allow
	readForbidden := readDecision == cedar.Deny && len(readDiag.Reasons) > 0

	return (callAllowed && !readForbidden) || (readAllowed && !callForbidden), nil
}

var _ authorizers.ToolReadNative = (*Authorizer)(nil)
