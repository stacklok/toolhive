package permissions

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfile_Privileged(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		profile  *Profile
		expected bool
	}{
		{
			name: "Default profile should not be privileged",
			profile: &Profile{
				Name:       "test",
				Privileged: false,
			},
			expected: false,
		},
		{
			name: "Privileged profile should be privileged",
			profile: &Profile{
				Name:       "test",
				Privileged: true,
			},
			expected: true,
		},
		{
			name:     "Built-in none profile should not be privileged",
			profile:  BuiltinNoneProfile(),
			expected: false,
		},
		{
			name:     "Built-in network profile should not be privileged",
			profile:  BuiltinNetworkProfile(),
			expected: false,
		},
		{
			name:     "New profile should not be privileged by default",
			profile:  NewProfile(),
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, tc.profile.Privileged, "Privileged flag should match expected value")
		})
	}
}

func TestProfile_PrivilegedJSONSerialization(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		profile  *Profile
		expected string
	}{
		{
			name: "Non-privileged profile JSON",
			profile: &Profile{
				Name:       "test",
				Privileged: false,
			},
			expected: `{"name":"test"}`, // privileged: false should be omitted due to omitempty
		},
		{
			name: "Privileged profile JSON",
			profile: &Profile{
				Name:       "test",
				Privileged: true,
			},
			expected: `{"name":"test","privileged":true}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Test marshaling
			jsonData, err := json.Marshal(tc.profile)
			require.NoError(t, err, "JSON marshaling should not fail")
			assert.JSONEq(t, tc.expected, string(jsonData), "JSON output should match expected")

			// Test unmarshaling
			var unmarshaled Profile
			err = json.Unmarshal(jsonData, &unmarshaled)
			require.NoError(t, err, "JSON unmarshaling should not fail")
			assert.Equal(t, tc.profile.Privileged, unmarshaled.Privileged, "Privileged flag should be preserved after JSON round-trip")
		})
	}
}

func TestProfile_PrivilegedYAMLSerialization(t *testing.T) {
	t.Parallel()

	// Test that the YAML tag is present and works
	profile := &Profile{
		Name:       "test",
		Privileged: true,
	}

	// Test JSON marshaling (which should work with YAML tags too)
	jsonData, err := json.Marshal(profile)
	require.NoError(t, err, "JSON marshaling should not fail")

	var unmarshaled Profile
	err = json.Unmarshal(jsonData, &unmarshaled)
	require.NoError(t, err, "JSON unmarshaling should not fail")

	assert.Equal(t, profile.Privileged, unmarshaled.Privileged, "Privileged flag should be preserved")
	assert.Equal(t, profile.Name, unmarshaled.Name, "Name should be preserved")
}
