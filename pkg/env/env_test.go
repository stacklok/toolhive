package env

import (
	"os"
	"testing"
)

func TestOSReader_Getenv(t *testing.T) { //nolint:paralleltest // Modifies environment variables
	// Cannot run in parallel because it modifies environment variables
	testKey := "TEST_ENV_VARIABLE_FOR_TESTING"
	testValue := "test_value_123"

	// Set an environment variable for testing
	originalValue, wasSet := os.LookupEnv(testKey)
	os.Setenv(testKey, testValue)
	t.Cleanup(func() {
		if wasSet {
			os.Setenv(testKey, originalValue)
		} else {
			os.Unsetenv(testKey)
		}
	})

	reader := &OSReader{}

	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "existing environment variable",
			key:  testKey,
			want: testValue,
		},
		{
			name: "non-existing environment variable",
			key:  "NONEXISTENT_ENV_VAR_TESTING_12345",
			want: "",
		},
		{
			name: "empty key",
			key:  "",
			want: "",
		},
	}

	for _, tt := range tests { //nolint:paralleltest // Test modifies environment variables
		t.Run(tt.name, func(t *testing.T) {
			// Cannot run in parallel because parent test modifies environment variables
			got := reader.Getenv(tt.key)
			if got != tt.want {
				t.Errorf("OSReader.Getenv() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReader_InterfaceCompliance ensures OSReader implements the Reader interface
func TestReader_InterfaceCompliance(t *testing.T) {
	t.Parallel()
	var _ Reader = &OSReader{}
	// If this compiles, the test passes
}
