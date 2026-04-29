// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	types "github.com/stacklok/toolhive-core/registry/types"
)

// Kind identifies what kind of payload an Entry carries.
//
// New kinds extend the enum and add a corresponding optional pointer field
// to Entry. Exactly one payload pointer must be populated, matching Kind.
type Kind string

// Supported entry kinds.
const (
	KindServer Kind = "server"
	KindSkill  Kind = "skill"
)

// Entry is the unit a Registry exposes. The payload pointer matching Kind
// must be set; all other payload pointers must be nil.
//
// Entries from different registries may share a Name; conflict resolution
// is the Manager's responsibility.
type Entry struct {
	// Kind selects which payload field is populated.
	Kind Kind

	// Name is the entry identifier within its source Registry. For server
	// entries this is the reverse-DNS form used by ServerJSON (e.g.
	// "io.github.user/weather"). For skills this is the published name.
	Name string

	// Server is set when Kind == KindServer.
	Server *v0.ServerJSON

	// Skill is set when Kind == KindSkill.
	Skill *types.Skill
}

// Validate reports an error if the Entry is malformed: missing Name,
// unknown Kind, or a payload pointer mismatching Kind.
func (e *Entry) Validate() error {
	if e == nil {
		return fmt.Errorf("nil entry")
	}
	if e.Name == "" {
		return fmt.Errorf("entry has empty name")
	}
	switch e.Kind {
	case KindServer:
		if e.Server == nil {
			return fmt.Errorf("entry %q has Kind=server but Server is nil", e.Name)
		}
		if e.Skill != nil {
			return fmt.Errorf("entry %q has Kind=server but Skill is also set", e.Name)
		}
	case KindSkill:
		if e.Skill == nil {
			return fmt.Errorf("entry %q has Kind=skill but Skill is nil", e.Name)
		}
		if e.Server != nil {
			return fmt.Errorf("entry %q has Kind=skill but Server is also set", e.Name)
		}
	default:
		return fmt.Errorf("entry %q has unknown kind %q", e.Name, e.Kind)
	}
	return nil
}
