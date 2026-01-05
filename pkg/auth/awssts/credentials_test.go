package awssts

import (
	"testing"
	"time"
)

const (
	testRoleArn = "arn:aws:iam::123456789012:role/TestRole"
	testToken   = "test-token-123"
)

func TestCredentialCache_GetSet(t *testing.T) {
	t.Parallel()

	cache := NewCredentialCache(10)

	roleArn := testRoleArn
	token := testToken
	creds := &Credentials{
		AccessKeyID:     "AKIATEST",
		SecretAccessKey: "secret",
		SessionToken:    "session",
		Expiration:      time.Now().Add(time.Hour),
	}

	// Initially empty
	got := cache.Get(roleArn, token)
	if got != nil {
		t.Errorf("Get() on empty cache = %v, want nil", got)
	}

	// Set and retrieve
	cache.Set(roleArn, token, creds)
	got = cache.Get(roleArn, token)
	if got == nil {
		t.Fatal("Get() after Set() = nil, want credentials")
	}
	if got.AccessKeyID != creds.AccessKeyID {
		t.Errorf("Get().AccessKeyID = %v, want %v", got.AccessKeyID, creds.AccessKeyID)
	}
}

func TestCredentialCache_ExpiredCredentials(t *testing.T) {
	t.Parallel()

	cache := NewCredentialCache(10)

	roleArn := testRoleArn
	token := testToken

	// Set expired credentials
	expiredCreds := &Credentials{
		AccessKeyID:     "AKIATEST",
		SecretAccessKey: "secret",
		SessionToken:    "session",
		Expiration:      time.Now().Add(-time.Hour), // Already expired
	}
	cache.Set(roleArn, token, expiredCreds)

	// Should return nil for expired credentials
	got := cache.Get(roleArn, token)
	if got != nil {
		t.Errorf("Get() for expired credentials = %v, want nil", got)
	}
}

func TestCredentialCache_ShouldRefreshCredentials(t *testing.T) {
	t.Parallel()

	cache := NewCredentialCache(10)

	roleArn := testRoleArn
	token := testToken

	// Set credentials that should be refreshed (within buffer)
	soonToExpire := &Credentials{
		AccessKeyID:     "AKIATEST",
		SecretAccessKey: "secret",
		SessionToken:    "session",
		Expiration:      time.Now().Add(3 * time.Minute), // Less than 5-minute buffer
	}
	cache.Set(roleArn, token, soonToExpire)

	// Should return nil when credentials should be refreshed
	got := cache.Get(roleArn, token)
	if got != nil {
		t.Errorf("Get() for credentials needing refresh = %v, want nil", got)
	}
}

func TestCredentialCache_DifferentKeys(t *testing.T) {
	t.Parallel()

	cache := NewCredentialCache(10)

	roleArn1 := "arn:aws:iam::123456789012:role/Role1"
	roleArn2 := "arn:aws:iam::123456789012:role/Role2"
	token1 := "token-1"
	token2 := "token-2"

	creds1 := &Credentials{
		AccessKeyID: "AKIA1",
		Expiration:  time.Now().Add(time.Hour),
	}
	creds2 := &Credentials{
		AccessKeyID: "AKIA2",
		Expiration:  time.Now().Add(time.Hour),
	}
	creds3 := &Credentials{
		AccessKeyID: "AKIA3",
		Expiration:  time.Now().Add(time.Hour),
	}

	// Different role ARN
	cache.Set(roleArn1, token1, creds1)
	cache.Set(roleArn2, token1, creds2)

	// Different token
	cache.Set(roleArn1, token2, creds3)

	// Verify isolation
	got1 := cache.Get(roleArn1, token1)
	got2 := cache.Get(roleArn2, token1)
	got3 := cache.Get(roleArn1, token2)

	if got1 == nil || got1.AccessKeyID != "AKIA1" {
		t.Errorf("roleArn1/token1 AccessKeyID = %v, want AKIA1", got1)
	}
	if got2 == nil || got2.AccessKeyID != "AKIA2" {
		t.Errorf("roleArn2/token1 AccessKeyID = %v, want AKIA2", got2)
	}
	if got3 == nil || got3.AccessKeyID != "AKIA3" {
		t.Errorf("roleArn1/token2 AccessKeyID = %v, want AKIA3", got3)
	}
}

func TestCredentialCache_LRUEviction(t *testing.T) {
	t.Parallel()

	// Small cache to test eviction
	cache := NewCredentialCache(3)

	roleArn := testRoleArn
	baseCreds := func(id string) *Credentials {
		return &Credentials{
			AccessKeyID: id,
			Expiration:  time.Now().Add(time.Hour),
		}
	}

	// Fill cache
	cache.Set(roleArn, "token-1", baseCreds("AKIA1"))
	cache.Set(roleArn, "token-2", baseCreds("AKIA2"))
	cache.Set(roleArn, "token-3", baseCreds("AKIA3"))

	if cache.Size() != 3 {
		t.Errorf("Size() = %d, want 3", cache.Size())
	}

	// Access token-1 to make it recently used
	_ = cache.Get(roleArn, "token-1")

	// Add new entry, should evict token-2 (LRU)
	cache.Set(roleArn, "token-4", baseCreds("AKIA4"))

	if cache.Size() != 3 {
		t.Errorf("Size() after eviction = %d, want 3", cache.Size())
	}

	// token-1 should still be present (was accessed)
	if cache.Get(roleArn, "token-1") == nil {
		t.Error("token-1 should not have been evicted")
	}

	// token-2 should be evicted (LRU)
	if cache.Get(roleArn, "token-2") != nil {
		t.Error("token-2 should have been evicted")
	}

	// token-3 and token-4 should be present
	if cache.Get(roleArn, "token-3") == nil {
		t.Error("token-3 should still be present")
	}
	if cache.Get(roleArn, "token-4") == nil {
		t.Error("token-4 should still be present")
	}
}

func TestCredentialCache_Delete(t *testing.T) {
	t.Parallel()

	cache := NewCredentialCache(10)

	roleArn := testRoleArn
	token := testToken
	creds := &Credentials{
		AccessKeyID: "AKIATEST",
		Expiration:  time.Now().Add(time.Hour),
	}

	cache.Set(roleArn, token, creds)
	if cache.Size() != 1 {
		t.Errorf("Size() after Set() = %d, want 1", cache.Size())
	}

	cache.Delete(roleArn, token)
	if cache.Size() != 0 {
		t.Errorf("Size() after Delete() = %d, want 0", cache.Size())
	}

	got := cache.Get(roleArn, token)
	if got != nil {
		t.Errorf("Get() after Delete() = %v, want nil", got)
	}
}

func TestCredentialCache_Clear(t *testing.T) {
	t.Parallel()

	cache := NewCredentialCache(10)

	roleArn := testRoleArn
	creds := &Credentials{
		AccessKeyID: "AKIATEST",
		Expiration:  time.Now().Add(time.Hour),
	}

	cache.Set(roleArn, "token-1", creds)
	cache.Set(roleArn, "token-2", creds)
	cache.Set(roleArn, "token-3", creds)

	if cache.Size() != 3 {
		t.Errorf("Size() before Clear() = %d, want 3", cache.Size())
	}

	cache.Clear()

	if cache.Size() != 0 {
		t.Errorf("Size() after Clear() = %d, want 0", cache.Size())
	}
}

func TestCredentialCache_Update(t *testing.T) {
	t.Parallel()

	cache := NewCredentialCache(10)

	roleArn := testRoleArn
	token := testToken

	creds1 := &Credentials{
		AccessKeyID: "AKIA1",
		Expiration:  time.Now().Add(time.Hour),
	}
	creds2 := &Credentials{
		AccessKeyID: "AKIA2",
		Expiration:  time.Now().Add(time.Hour),
	}

	cache.Set(roleArn, token, creds1)
	cache.Set(roleArn, token, creds2) // Update

	if cache.Size() != 1 {
		t.Errorf("Size() after update = %d, want 1", cache.Size())
	}

	got := cache.Get(roleArn, token)
	if got == nil {
		t.Fatal("Get() after update = nil, want credentials")
	}
	if got.AccessKeyID != "AKIA2" {
		t.Errorf("Get().AccessKeyID after update = %v, want AKIA2", got.AccessKeyID)
	}
}

func TestCredentialCache_NilCredentials(t *testing.T) {
	t.Parallel()

	cache := NewCredentialCache(10)

	roleArn := testRoleArn
	token := testToken

	// Setting nil credentials should be a no-op
	cache.Set(roleArn, token, nil)

	if cache.Size() != 0 {
		t.Errorf("Size() after Set(nil) = %d, want 0", cache.Size())
	}
}

func TestNewCredentialCache_DefaultSize(t *testing.T) {
	t.Parallel()

	// Zero should use default
	cache := NewCredentialCache(0)
	if cache.maxSize != DefaultCacheSize {
		t.Errorf("maxSize with 0 = %d, want %d", cache.maxSize, DefaultCacheSize)
	}

	// Negative should use default
	cache = NewCredentialCache(-5)
	if cache.maxSize != DefaultCacheSize {
		t.Errorf("maxSize with -5 = %d, want %d", cache.maxSize, DefaultCacheSize)
	}
}

func TestBuildCacheKey(t *testing.T) {
	t.Parallel()

	// Keys should be different for different role ARNs
	key1 := buildCacheKey("role1", "token")
	key2 := buildCacheKey("role2", "token")
	if key1 == key2 {
		t.Error("Keys should differ for different role ARNs")
	}

	// Keys should be different for different tokens
	key3 := buildCacheKey("role1", "token1")
	key4 := buildCacheKey("role1", "token2")
	if key3 == key4 {
		t.Error("Keys should differ for different tokens")
	}

	// Same inputs should produce same key
	key5 := buildCacheKey("role1", "token")
	key6 := buildCacheKey("role1", "token")
	if key5 != key6 {
		t.Error("Same inputs should produce same key")
	}
}
