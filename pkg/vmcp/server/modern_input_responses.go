// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Elicitation result actions (ElicitResult.action) that refuse the request.
// The third action, "accept", is not a refusal.
const (
	elicitActionDecline = "decline"
	elicitActionCancel  = "cancel"
)

// modernURLElicitationRefused reports whether a Modern tools/call retry
// declined or cancelled any elicitation the URL elicitation answer asks for
// again. It reads params.inputResponses, which maps each input request key
// (for the answer, the elicitation ID) to the client's response, and looks
// only at the answer's own elicitation IDs.
//
// Params that do not decode, a missing inputResponses, and a response with no
// recognizable action all report false: the request is treated as a first
// call or an accept, and the answer is asked again. That never shows the URL
// to a client that did not declare URL mode, because this is reached only
// after that check.
func modernURLElicitationRefused(params json.RawMessage, answer *vmcp.URLElicitationRequiredError) bool {
	if len(params) == 0 {
		return false
	}
	var p struct {
		InputResponses map[string]struct {
			Action string `json:"action"`
		} `json:"inputResponses"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.InputResponses) == 0 {
		return false
	}
	for _, e := range answer.Elicitations() {
		resp, ok := p.InputResponses[e.ElicitationID()]
		if !ok {
			continue
		}
		if resp.Action == elicitActionDecline || resp.Action == elicitActionCancel {
			return true
		}
	}
	return false
}
