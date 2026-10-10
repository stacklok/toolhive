// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// EnvVarValidator defines the interface for checking that the expected
// environment variables and secrets have been supplied when creating a
// workload. This is implemented as a strategy pattern since the handling
// is different for the CLI vs the API and k8s.
type EnvVarValidator interface {
	// Validate checks that all required environment variables and secrets are provided
	// and returns the processed environment variables to be set.
	Validate(
		ctx context.Context,
		metadata *registry.ImageMetadata,
		runConfig *RunConfig,
		suppliedEnvVars map[string]string,
	) (map[string]string, error)
}

// DetachedEnvVarValidator implements the EnvVarValidator interface for
// scenarios where the user cannot be prompted for input. Any missing,
// mandatory variables will result in an error being returned.
type DetachedEnvVarValidator struct{}

// Validate checks that all required environment variables and secrets are provided
// and returns the processed environment variables to be set.
func (*DetachedEnvVarValidator) Validate(
	_ context.Context,
	metadata *registry.ImageMetadata,
	runConfig *RunConfig,
	suppliedEnvVars map[string]string,
) (map[string]string, error) {
	// Check variables in metadata if we are processing an image from our registry.
	if metadata != nil {
		secretsList := runConfig.Secrets
		registryEnvVars := metadata.EnvVars
		for _, envVar := range registryEnvVars {
			if isEnvVarProvided(envVar.Name, suppliedEnvVars, secretsList) {
				continue
			} else if envVar.Required {
				return nil, fmt.Errorf("missing required environment variable: %s", envVar.Name)
			} else if envVar.Default != "" {
				addAsEnvironmentVariable(envVar, envVar.Default, &suppliedEnvVars)
			}
		}
	}

	return suppliedEnvVars, nil
}

// CLIEnvVarValidator implements the EnvVarValidator interface for
// CLI usage. If any missing, mandatory variables are found, this code will
// prompt the user to supply them through stdin.
type CLIEnvVarValidator struct {
	configProvider config.Provider
}

// NewCLIEnvVarValidator creates a new CLI environment variable validator with the given config provider.
func NewCLIEnvVarValidator(configProvider config.Provider) *CLIEnvVarValidator {
	return &CLIEnvVarValidator{
		configProvider: configProvider,
	}
}

// Validate checks that all required environment variables and secrets are provided
// and returns the processed environment variables to be set.
func (v *CLIEnvVarValidator) Validate(
	ctx context.Context,
	metadata *registry.ImageMetadata,
	runConfig *RunConfig,
	suppliedEnvVars map[string]string,
) (map[string]string, error) {
	envVars := make(map[string]string)

	// Copy the supplied environment variables
	for k, v := range suppliedEnvVars {
		envVars[k] = v
	}

	// If we are processing an image from our registry, we need to check the
	// variables defined in the metadata.
	if metadata != nil {
		secretsConfig := runConfig.Secrets
		// Create new slice for extra secrets
		secretsList := make([]string, 0, len(secretsConfig))

		// Copy existing secrets
		secretsList = append(secretsList, secretsConfig...)
		registryEnvVars := metadata.EnvVars

		// Initialize secrets manager if needed
		secretsManager := v.initializeSecretsManagerIfNeeded(registryEnvVars)

		// One reader for every prompt in this call: a fresh bufio.Reader per prompt
		// would throw away any line it had already buffered from os.Stdin, which is
		// how a piped answer for the next variable would be lost.
		stdinReader := bufio.NewReader(os.Stdin)

		// Process each environment variable from the registry
		for _, envVar := range registryEnvVars {
			if isEnvVarProvided(envVar.Name, envVars, secretsList) {
				continue
			}

			if envVar.Required {

				if envVar.Secret {
					// Check if secrets manager is available before attempting to retrieve secret.
					// Falls back to prompt if unavailable or secret not found.
					if secretsManager != nil {
						value, err := secretsManager.GetSecret(ctx, envVar.Name)
						if err != nil {
							slog.Warn("unable to find secret in the secrets manager", "name", envVar.Name, "error", err)
						} else {
							addNewVariable(ctx, envVar, value, secretsManager, &envVars, &secretsList)
							continue
						}
					} else {
						slog.Warn("secrets manager not configured (setup incomplete or missing provider) - " +
							"falling back to prompt")
					}

					// If secrets manager unavailable or secret not found, fall through to prompt
				}

				value, err := promptForEnvironmentVariable(envVar, stdinReader)
				if err != nil {
					// The variable is required: without a value the workload starts
					// misconfigured, so fail instead of warning and skipping it.
					return nil, fmt.Errorf("failed to read value for required environment variable %s: %w",
						envVar.Name, err)
				}
				if value == "" {
					return nil, fmt.Errorf("no value provided for required environment variable: %s", envVar.Name)
				}
				addNewVariable(ctx, envVar, value, secretsManager, &envVars, &secretsList)
			} else if envVar.Default != "" {
				addNewVariable(ctx, envVar, envVar.Default, secretsManager, &envVars, &secretsList)
			}
		}

		runConfig.Secrets = secretsList
	}

	return envVars, nil
}

// promptForEnvironmentVariable prompts the user for an environment variable value
// and reads it as a whole line. Required registry variables routinely take values
// containing spaces (a path with a space, a space-separated OAuth scope list),
// which fmt.Scanln cannot read: it stops after the first whitespace-delimited
// token and reports "expected newline" for the rest.
func promptForEnvironmentVariable(envVar *registry.EnvVar, stdin *bufio.Reader) (string, error) {
	var byteValue []byte
	var err error
	if envVar.Secret {
		fmt.Printf("Required secret environment variable: %s (%s)", envVar.Name, envVar.Description)
		fmt.Printf("Enter value for %s (input will be hidden): ", envVar.Name)
		byteValue, err = term.ReadPassword(int(os.Stdin.Fd())) //nolint:gosec // G115: stdin fd is always small
		fmt.Println()                                          // Move to the next line after hidden input
		if err != nil {
			return "", fmt.Errorf("failed to read hidden input: %w", err)
		}
	} else {
		fmt.Printf("Required environment variable: %s (%s)", envVar.Name, envVar.Description)
		fmt.Printf("Enter value for %s: ", envVar.Name)
		// Read the full line, matching the other interactive prompts in this repo.
		// A value that arrives without a trailing newline still counts: ReadString
		// returns the data alongside io.EOF in that case.
		line, readErr := stdin.ReadString('\n')
		if readErr != nil && line == "" {
			return "", fmt.Errorf("failed to read input: %w", readErr)
		}
		byteValue = []byte(line)
	}

	return strings.TrimSpace(string(byteValue)), nil
}

// addNewVariable adds an environment variable or secret to the appropriate list
func addNewVariable(
	ctx context.Context,
	envVar *registry.EnvVar,
	value string,
	secretsManager secrets.Provider,
	envVars *map[string]string,
	secretsList *[]string,
) {
	if envVar.Secret && secretsManager != nil {
		addAsSecret(ctx, envVar, value, secretsManager, secretsList, envVars)
	} else {
		addAsEnvironmentVariable(envVar, value, envVars)
	}
}

// addAsSecret stores the value as a secret and adds a secret reference
func addAsSecret(
	ctx context.Context,
	envVar *registry.EnvVar,
	value string,
	secretsManager secrets.Provider,
	secretsList *[]string,
	envVars *map[string]string,
) {
	var secretName string
	if envVar.Required {
		secretName = fmt.Sprintf("registry-user-%s", strings.ToLower(envVar.Name))
	} else {
		secretName = fmt.Sprintf("registry-default-%s", strings.ToLower(envVar.Name))
	}

	if err := secretsManager.SetSecret(ctx, secretName, value); err != nil {
		slog.Warn("failed to store secret", "secret_name", secretName, "error", err)
		slog.Warn("falling back to environment variable", "name", envVar.Name)
		(*envVars)[envVar.Name] = value
		slog.Debug("added environment variable (secret fallback)", "name", envVar.Name)
	} else {
		// Create secret reference for RunConfig
		secretEntry := fmt.Sprintf("%s,target=%s", secretName, envVar.Name)
		*secretsList = append(*secretsList, secretEntry)
		if envVar.Required {
			slog.Debug("created secret", "name", envVar.Name, "secret_name", secretName)
		} else {
			slog.Debug("created secret with default value", "name", envVar.Name, "secret_name", secretName)
		}
	}
}

// initializeSecretsManagerIfNeeded initializes the secrets manager if there are secret environment variables
func (v *CLIEnvVarValidator) initializeSecretsManagerIfNeeded(registryEnvVars []*registry.EnvVar) secrets.Provider {
	// Check if we have any secret environment variables
	hasSecrets := false
	for _, envVar := range registryEnvVars {
		if envVar.Secret {
			hasSecrets = true
			break
		}
	}

	if !hasSecrets {
		return nil
	}

	secretsManager, err := v.getSecretsManager()
	if err != nil {
		slog.Warn("failed to initialize secrets manager", "error", err)
		slog.Warn("secret environment variables will be stored as regular environment variables")
		return nil
	}

	return secretsManager
}

// Duplicated from cmd/thv/app/app.go
// It may be possible to de-duplicate this in future.
func (v *CLIEnvVarValidator) getSecretsManager() (secrets.Provider, error) {
	cfg := v.configProvider.GetConfig()

	// Check if secrets setup has been completed
	if !cfg.Secrets.SetupCompleted {
		return nil, secrets.ErrSecretsNotSetup
	}

	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets provider type: %w", err)
	}

	manager, err := secrets.CreateProvider(providerType, secrets.WithScope(secrets.ScopeWorkloads))
	if err != nil {
		return nil, fmt.Errorf("failed to create secrets manager: %w", err)
	}

	return manager, nil
}

// Shared Logic follows

// isEnvVarProvided checks if an environment variable is already provided
func isEnvVarProvided(name string, envVars map[string]string, secretsConfig []string) bool {
	// Check if the environment variable is already provided in the command line
	if _, exists := envVars[name]; exists {
		return true
	}

	// Check if the environment variable is provided as a secret
	return findEnvironmentVariableFromSecrets(secretsConfig, name)
}

func findEnvironmentVariableFromSecrets(secs []string, envVarName string) bool {
	for _, secret := range secs {
		if isSecretReferenceEnvVar(secret, envVarName) {
			return true
		}
	}

	return false
}

func isSecretReferenceEnvVar(secret, envVarName string) bool {
	parts := strings.Split(secret, ",")
	if len(parts) != 2 {
		return false
	}

	targetSplit := strings.Split(parts[1], "=")
	if len(targetSplit) != 2 {
		return false
	}

	if targetSplit[1] == envVarName {
		return true
	}

	return false
}

// addAsEnvironmentVariable adds the value as a regular environment variable
func addAsEnvironmentVariable(
	envVar *registry.EnvVar,
	value string,
	envVars *map[string]string,
) {
	(*envVars)[envVar.Name] = value

	if envVar.Secret {
		if envVar.Required {
			slog.Debug("added secret as environment variable (no secrets manager)", "name", envVar.Name)
		} else {
			slog.Debug("added default secret as environment variable (no secrets manager)", "name", envVar.Name)
		}
	} else {
		if envVar.Required {
			slog.Debug("added environment variable", "name", envVar.Name)
		} else {
			slog.Debug("using default value", "name", envVar.Name, "default_value", value)
		}
	}
}
