// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// SecretScope is the type for system-managed secret scope identifiers.
//
// Invariants that every SecretScope value MUST satisfy:
//   - Non-empty: an empty scope would produce the prefix "__thv__", which is
//     ambiguous and cannot be reliably stripped.
//   - No underscores: the key format is "__thv_<scope>_<name>"; an underscore
//     inside the scope would make it impossible to determine where the scope
//     ends and the name begins.
//
// All constants declared in this package (ScopeRegistry, ScopeWorkloads,
// ScopeAuth, ScopeLLM) satisfy these invariants. Custom scopes introduced in
// the future must be validated against them.
type SecretScope string

const (
	// SystemKeyPrefix is the prefix used for all system-managed secret keys.
	// Any key starting with this prefix is reserved for internal use.
	// The double-underscore and trailing underscore make it visually distinct
	// and avoid conflicts with backends (e.g. 1Password) that treat "/" as a
	// path separator.
	SystemKeyPrefix = "__thv_"

	// ScopeRegistry is the scope for registry OAuth refresh tokens.
	ScopeRegistry SecretScope = "registry"

	// ScopeWorkloads is the scope for remote workload authentication tokens
	// (OAuth client secrets, bearer tokens, OAuth refresh tokens).
	ScopeWorkloads SecretScope = "workloads"

	// ScopeAuth is reserved for enterprise CLI/Desktop login tokens.
	ScopeAuth SecretScope = "auth"

	// ScopeLLM is the scope for LLM gateway OIDC refresh tokens.
	ScopeLLM SecretScope = "llm"
)

// ErrReservedKeyName is returned when a user command attempts to manage a
// secret whose name is reserved for system use.
var ErrReservedKeyName = errors.New("secret name is reserved for system use and cannot be managed via user commands")

// ScopedProvider wraps a Provider and namespaces all operations under a
// system-managed scope prefix ("__thv_<scope>_"). It is intended for
// internal callers such as registry auth and workload auth that need isolated
// key spaces inside the shared secrets store.
type ScopedProvider struct {
	provider Provider
	scope    SecretScope
}

// NewScopedProvider creates a Provider that transparently prefixes every key
// with "__thv_<scope>_", keeping system secrets isolated from user secrets.
func NewScopedProvider(inner Provider, scope SecretScope) Provider {
	return &ScopedProvider{
		provider: inner,
		scope:    scope,
	}
}

// GetSecret retrieves the secret identified by name under this provider's scope.
// If the scoped key is not found, it falls back to the bare (pre-migration) key.
// This makes the provider safe to use before or during secret scope migration:
// once migration completes and bare keys are deleted, the fallback is a no-op.
func (s *ScopedProvider) GetSecret(ctx context.Context, name string) (string, error) {
	val, err := s.provider.GetSecret(ctx, s.getScopedKey(name))
	if err == nil {
		return val, nil
	}
	if IsNotFoundError(err) {
		// Migration window: the scoped key does not exist yet. Try the bare key
		// that was used before secret scope migration ran. After migration
		// completes and the bare key is deleted, this lookup returns not-found
		// and we fall through to return the original scoped-key error.
		bareVal, bareErr := s.provider.GetSecret(ctx, name)
		if bareErr == nil {
			slog.Debug("secret scope migration fallback: returning bare key",
				"scope", s.scope, "name", name)
			return bareVal, nil
		}
		if !IsNotFoundError(bareErr) {
			// Bare-key lookup hit a real backend error (e.g. connection failure,
			// auth error). Surface it so the caller doesn't misdiagnose a backend
			// problem as "secret not found".
			return "", fmt.Errorf("scoped key not found and bare-key fallback failed: %w", bareErr)
		}
	}
	return "", err
}

// SetSecret stores value under the scoped key for name.
func (s *ScopedProvider) SetSecret(ctx context.Context, name, value string) error {
	return s.provider.SetSecret(ctx, s.getScopedKey(name), value)
}

// DeleteSecret removes the scoped key for name from the underlying store.
func (s *ScopedProvider) DeleteSecret(ctx context.Context, name string) error {
	return s.provider.DeleteSecret(ctx, s.getScopedKey(name))
}

// ListSecrets returns only the entries that belong to this provider's scope,
// with the "__thv_<scope>_" prefix stripped from each Key so callers receive bare names.
func (s *ScopedProvider) ListSecrets(ctx context.Context) ([]SecretDescription, error) {
	all, err := s.provider.ListSecrets(ctx)
	if err != nil {
		return nil, err
	}

	prefix := s.getScopePrefix()
	var result []SecretDescription
	for _, desc := range all {
		if strings.HasPrefix(desc.Key, prefix) {
			result = append(result, SecretDescription{
				Key:         strings.TrimPrefix(desc.Key, prefix),
				Description: desc.Description,
			})
		}
	}
	return result, nil
}

// DeleteSecrets removes all named keys under this scope by delegating to the inner provider.
func (s *ScopedProvider) DeleteSecrets(ctx context.Context, names []string) error {
	keys := make([]string, len(names))
	for i, name := range names {
		keys[i] = s.getScopedKey(name)
	}
	return s.provider.DeleteSecrets(ctx, keys)
}

// Cleanup removes only the secrets that belong to this scope, leaving all
// other secrets untouched.
func (s *ScopedProvider) Cleanup() error {
	ctx := context.Background()

	all, err := s.provider.ListSecrets(ctx)
	if err != nil {
		return err
	}

	prefix := s.getScopePrefix()
	var toDelete []string
	for _, desc := range all {
		if strings.HasPrefix(desc.Key, prefix) {
			toDelete = append(toDelete, desc.Key)
		}
	}
	if len(toDelete) == 0 {
		return nil
	}
	return s.provider.DeleteSecrets(ctx, toDelete)
}

// Capabilities delegates to the underlying provider.
func (s *ScopedProvider) Capabilities() ProviderCapabilities {
	return s.provider.Capabilities()
}

// getScopedKey builds the internal storage key in the form "__thv_<scope>_<name>".
func (s *ScopedProvider) getScopedKey(name string) string {
	return SystemKeyPrefix + string(s.scope) + "_" + name
}

// getScopePrefix returns the key prefix for this scope, i.e. "__thv_<scope>_".
func (s *ScopedProvider) getScopePrefix() string {
	return SystemKeyPrefix + string(s.scope) + "_"
}

// UserProvider wraps a Provider and hides all system-reserved keys from
// user-facing callers (CLI, API, MCP tool server). Any attempt to read or
// modify a key that starts with the system prefix is rejected with
// ErrReservedKeyName.
type UserProvider struct {
	provider Provider
}

// NewUserProvider creates a Provider that filters out system-reserved keys so
// that user-facing callers cannot accidentally read or overwrite internal
// secrets managed by ScopedProvider.
func NewUserProvider(inner Provider) Provider {
	return &UserProvider{provider: inner}
}

// GetSecret returns the secret for name, or ErrReservedKeyName if the name is
// a system-reserved key.
func (u *UserProvider) GetSecret(ctx context.Context, name string) (string, error) {
	if IsSystemKey(name) {
		return "", fmt.Errorf("%w: cannot get %q", ErrReservedKeyName, name)
	}
	return u.provider.GetSecret(ctx, name)
}

// SetSecret stores value under name, or returns ErrReservedKeyName if the name
// is system-reserved.
func (u *UserProvider) SetSecret(ctx context.Context, name, value string) error {
	if IsSystemKey(name) {
		return fmt.Errorf("%w: cannot set %q", ErrReservedKeyName, name)
	}
	return u.provider.SetSecret(ctx, name, value)
}

// DeleteSecret removes name from the underlying store, or returns
// ErrReservedKeyName if the name is system-reserved.
func (u *UserProvider) DeleteSecret(ctx context.Context, name string) error {
	if IsSystemKey(name) {
		return fmt.Errorf("%w: cannot delete %q", ErrReservedKeyName, name)
	}
	return u.provider.DeleteSecret(ctx, name)
}

// ListSecrets returns all non-system secrets from the underlying store.
// Entries whose Key starts with the system prefix are silently omitted.
func (u *UserProvider) ListSecrets(ctx context.Context) ([]SecretDescription, error) {
	all, err := u.provider.ListSecrets(ctx)
	if err != nil {
		return nil, err
	}

	var result []SecretDescription
	for _, desc := range all {
		if !IsSystemKey(desc.Key) {
			result = append(result, desc)
		}
	}
	return result, nil
}

// DeleteSecrets removes all named keys with all-or-nothing semantics: it
// validates every name in the list before issuing any delete to the underlying
// store. If any name is system-reserved the entire operation is aborted and
// ErrReservedKeyName is returned without deleting anything.
func (u *UserProvider) DeleteSecrets(ctx context.Context, names []string) error {
	for _, name := range names {
		if IsSystemKey(name) {
			return fmt.Errorf("%w: cannot delete %q", ErrReservedKeyName, name)
		}
	}
	return u.provider.DeleteSecrets(ctx, names)
}

// Cleanup removes only user-owned secrets (those that do not start with the
// system prefix). System secrets are managed independently through their own
// ScopedProvider.Cleanup calls and must not be touched here.
func (u *UserProvider) Cleanup() error {
	ctx := context.Background()

	all, err := u.provider.ListSecrets(ctx)
	if err != nil {
		return err
	}

	var toDelete []string
	for _, desc := range all {
		if !IsSystemKey(desc.Key) {
			toDelete = append(toDelete, desc.Key)
		}
	}
	if len(toDelete) == 0 {
		return nil
	}
	return u.provider.DeleteSecrets(ctx, toDelete)
}

// Capabilities delegates to the underlying provider.
func (u *UserProvider) Capabilities() ProviderCapabilities {
	return u.provider.Capabilities()
}

// IsSystemKey reports whether name is reserved for system use, i.e. whether it
// starts with the system key prefix "__thv_".
func IsSystemKey(name string) bool {
	return strings.HasPrefix(name, SystemKeyPrefix)
}

// ParseSystemKey parses a system-managed key of the form "__thv_<scope>_<name>"
// and returns its scope and name components. ok is false if key does not start
// with SystemKeyPrefix or contains no scope separator.
func ParseSystemKey(key string) (scope, name string, ok bool) {
	rest, found := strings.CutPrefix(key, SystemKeyPrefix)
	if !found {
		return "", "", false
	}
	scope, name, ok = strings.Cut(rest, "_")
	return scope, name, ok
}
