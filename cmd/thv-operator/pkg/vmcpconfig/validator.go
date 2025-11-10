package vmcpconfig

import (
	"context"
	"fmt"

	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// Validator validates vmcp Config
type Validator struct{}

// NewValidator creates a new Validator instance
func NewValidator() *Validator {
	return &Validator{}
}

// Validate validates a vmcp Config
func (*Validator) Validate(_ context.Context, config *vmcpconfig.Config) error {
	if config == nil {
		return fmt.Errorf("vmcp Config cannot be nil")
	}

	if config.Name == "" {
		return fmt.Errorf("name is required")
	}

	if config.Group == "" {
		return fmt.Errorf("groupRef is required")
	}

	return nil
}
