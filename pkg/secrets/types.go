// Package secrets contains the secrets management logic for ToolHive.
package secrets

import (
	"context"
	"fmt"
	"regexp"
)

// regex to extract name and target from secret parameter, e.g. "name,target=target"
var secretParamRegex = regexp.MustCompile(`^([^,]+),target=(.+)$`)

// Provider describes a type which can manage secrets.
type Provider interface {
	GetSecret(ctx context.Context, name string) (string, error)
	SetSecret(ctx context.Context, name, value string) error
	DeleteSecret(ctx context.Context, name string) error
	ListSecrets(ctx context.Context) ([]SecretDescription, error)
	Cleanup() error
}

// SecretParameter represents a parsed `--secret` parameter.
type SecretParameter struct {
	Name   string `json:"name"`
	Target string `json:"target"`
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

// SecretParametersToCLI does the reverse of `ParseSecretParameter`
// TODO: It may be possible to get rid of this with refactoring.
func SecretParametersToCLI(params []SecretParameter) []string {
	result := make([]string, len(params))
	for i, p := range params {
		result[i] = fmt.Sprintf("%s,target=%s", p.Name, p.Target)
	}
	return result
}

// SecretDescription is returned by `ListSecrets`.
type SecretDescription struct {
	// Key is the unique identifier for the secret, used when retrieving it.
	Key string `json:"key"`
	// Description provides a human-readable description of the secret
	// Particularly useful for 1password.
	// May be empty if no description is available.
	Description string `json:"description"`
}
