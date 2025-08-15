//go:build !linux
// +build !linux

package keyring

import "fmt"

// NewKeyctlProvider creates a new keyctl provider. This provider is only available on Linux.
func NewKeyctlProvider() (Provider, error) {
	return nil, fmt.Errorf("keyctl provider is only available on Linux")
}
