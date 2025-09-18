package versions

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDevVersion = "dev"

func TestGetVersionInfo(t *testing.T) {
	t.Parallel()

	// Test with current global values (whatever they are)
	info := GetVersionInfo()

	// Basic structure validation
	assert.NotEmpty(t, info.Version)
	assert.NotEmpty(t, info.Commit)
	assert.NotEmpty(t, info.BuildDate)
	assert.Equal(t, runtime.Version(), info.GoVersion)
	assert.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, info.Platform)
}

func TestGetVersionInfoWithValues(t *testing.T) {
	t.Parallel()

	t.Run("with default dev values", func(t *testing.T) {
		t.Parallel()
		info := getVersionInfoWithValues(testDevVersion, unknownStr, unknownStr)

		// Should start with "build-" when version is "dev"
		assert.True(t, strings.HasPrefix(info.Version, "build-"), "Version should start with 'build-' for dev builds")
		assert.Equal(t, runtime.Version(), info.GoVersion)
		assert.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, info.Platform)
		// Commit and BuildDate might be populated from build info, so we just check they exist
		assert.NotEmpty(t, info.Commit)
		assert.NotEmpty(t, info.BuildDate)
	})

	t.Run("with release values", func(t *testing.T) {
		t.Parallel()
		info := getVersionInfoWithValues("v1.2.3", "abc123def456", "2023-01-01T12:00:00Z")

		assert.Equal(t, "v1.2.3", info.Version)
		assert.Equal(t, "abc123def456", info.Commit)
		// BuildDate should be formatted from RFC3339
		assert.Contains(t, info.BuildDate, "2023-01-01")
		assert.Equal(t, runtime.Version(), info.GoVersion)
		assert.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, info.Platform)
	})

	t.Run("with invalid build date", func(t *testing.T) {
		t.Parallel()
		info := getVersionInfoWithValues("v1.0.0", "abc123", "invalid-date")

		assert.Equal(t, "v1.0.0", info.Version)
		assert.Equal(t, "abc123", info.Commit)
		// Invalid date should remain unchanged
		assert.Equal(t, "invalid-date", info.BuildDate)
		assert.Equal(t, runtime.Version(), info.GoVersion)
		assert.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, info.Platform)
	})

	t.Run("with valid RFC3339 build date", func(t *testing.T) {
		t.Parallel()
		info := getVersionInfoWithValues("v2.0.0", "def456", "2023-12-25T15:30:45Z")

		assert.Equal(t, "v2.0.0", info.Version)
		assert.Equal(t, "def456", info.Commit)

		// Parse the expected time and format it
		expectedTime, err := time.Parse(time.RFC3339, "2023-12-25T15:30:45Z")
		require.NoError(t, err)
		expectedFormatted := expectedTime.Format("2006-01-02 15:04:05 MST")

		assert.Equal(t, expectedFormatted, info.BuildDate)
		assert.Equal(t, runtime.Version(), info.GoVersion)
		assert.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, info.Platform)
	})

	t.Run("commit truncation in dev version", func(t *testing.T) {
		t.Parallel()
		info := getVersionInfoWithValues(testDevVersion, "abcdef1234567890abcdef", "2023-01-01T12:00:00Z")

		// Should truncate commit to 8 characters in version
		assert.Equal(t, "build-abcdef12", info.Version)
		// But full commit should be preserved in Commit field
		assert.Equal(t, "abcdef1234567890abcdef", info.Commit)
	})
}

func TestVersionInfoStruct(t *testing.T) {
	t.Parallel()

	info := VersionInfo{
		Version:   "test-version",
		Commit:    "test-commit",
		BuildDate: "test-date",
		GoVersion: "test-go",
		Platform:  "test-platform",
	}

	assert.Equal(t, "test-version", info.Version)
	assert.Equal(t, "test-commit", info.Commit)
	assert.Equal(t, "test-date", info.BuildDate)
	assert.Equal(t, "test-go", info.GoVersion)
	assert.Equal(t, "test-platform", info.Platform)
}

func TestConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "unknown", unknownStr)
}
