// Package secrets contains the secrets management logic for Vibe Tool.
package secrets

import (
	"fmt"
	"regexp"
)

// regex to extract name and target from secret parameter, e.g. "name,target=target"
var secretParamRegex = regexp.MustCompile(`^([^,]+),target=(.+)$`)

// Manager describes a type which can manage secrets.
type Manager interface {
	GetSecret(name string) (string, error)
	SetSecret(name, value string) error
	DeleteSecret(name string) error
	ListSecrets() ([]string, error)
	Cleanup() error
}

// SecretParameter represents a parsed `--secret` parameter.
type SecretParameter struct {
	Name   string
	Target string
}

// ParseSecretParameter creates an instance of SecretParameter from a string.
// Expected format: `<Name>,target=<Target>`.
func ParseSecretParameter(parameter string) (SecretParameter, error) {
	if parameter == "" {
		return SecretParameter{}, fmt.Errorf("secret parameter cannot be empty")
	}

	// extract name and target using secretParamRegex
	matches := secretParamRegex.FindStringSubmatch(parameter)
	if len(matches) != 3 { // The first element is the full match, followed by capture groups
		return SecretParameter{}, fmt.Errorf("invalid secret parameter format: %s", parameter)
	}

	name := matches[1]
	target := matches[2]

	return SecretParameter{
		Name:   name,
		Target: target,
	}, nil
}
