package auth

import (
	"context"
	"testing"
)

// TestIdentityContext_StoreAndRetrieve verifies basic context storage and retrieval functionality.
func TestIdentityContext_StoreAndRetrieve(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Create a test identity
	identity := &Identity{
		Subject: "user123",
		Name:    "Alice Smith",
		Email:   "alice@example.com",
		Groups:  []string{"admins", "developers"},
		Claims: map[string]any{
			"org_id": "org456",
		},
		Token:     "test-token",
		TokenType: "Bearer",
		Metadata: map[string]string{
			"source": "test",
		},
	}

	// Store identity in context
	ctx = WithIdentity(ctx, identity)

	// Retrieve identity from context
	retrieved, ok := IdentityFromContext(ctx)
	if !ok {
		t.Fatal("expected identity to be present in context")
	}

	// Verify all fields match
	if retrieved.Subject != identity.Subject {
		t.Errorf("expected Subject %q, got %q", identity.Subject, retrieved.Subject)
	}
	if retrieved.Name != identity.Name {
		t.Errorf("expected Name %q, got %q", identity.Name, retrieved.Name)
	}
	if retrieved.Email != identity.Email {
		t.Errorf("expected Email %q, got %q", identity.Email, retrieved.Email)
	}
	if len(retrieved.Groups) != len(identity.Groups) {
		t.Errorf("expected %d groups, got %d", len(identity.Groups), len(retrieved.Groups))
	}
	if retrieved.Token != identity.Token {
		t.Errorf("expected Token %q, got %q", identity.Token, retrieved.Token)
	}
	if retrieved.TokenType != identity.TokenType {
		t.Errorf("expected TokenType %q, got %q", identity.TokenType, retrieved.TokenType)
	}
}

// TestIdentityContext_NilIdentity verifies that WithIdentity returns the original context
// when called with a nil identity.
func TestIdentityContext_NilIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Store nil identity - should return original context unchanged
	resultCtx := WithIdentity(ctx, nil)

	// Verify we got the same context back
	if resultCtx != ctx {
		t.Error("expected WithIdentity to return original context when identity is nil")
	}

	// Verify no identity is stored
	identity, ok := IdentityFromContext(resultCtx)
	if ok {
		t.Errorf("expected no identity in context, got %v", identity)
	}
	if identity != nil {
		t.Errorf("expected nil identity, got %v", identity)
	}
}

// TestIdentityContext_NotPresent verifies that IdentityFromContext returns nil and false
// when no identity has been stored in the context.
func TestIdentityContext_NotPresent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Try to retrieve identity from empty context
	identity, ok := IdentityFromContext(ctx)

	// Verify nothing was found
	if ok {
		t.Error("expected ok to be false when no identity is present")
	}
	if identity != nil {
		t.Errorf("expected nil identity, got %v", identity)
	}
}
