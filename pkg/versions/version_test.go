package versions

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestGetVersionInfo(t *testing.T) { //nolint:paralleltest // Modifies global variables
	// Cannot run in parallel because it modifies global variables
	// Save original values
	origVersion := Version
	origCommit := Commit
	origBuildDate := BuildDate

	tests := []struct {
		name      string
		version   string
		commit    string
		buildDate string
		wantCheck func(VersionInfo) bool
	}{
		{
			name:      "dev version with unknown commit",
			version:   "dev",
			commit:    unknownStr,
			buildDate: unknownStr,
			wantCheck: func(v VersionInfo) bool {
				// When version is "dev" and commit is unknown, version should be "build-unknown"
				return strings.HasPrefix(v.Version, "build-") &&
					v.Commit == unknownStr &&
					v.BuildDate == unknownStr &&
					v.GoVersion == runtime.Version() &&
					v.Platform == fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
			},
		},
		{
			name:      "dev version with commit",
			version:   "dev",
			commit:    "abc123def456789",
			buildDate: unknownStr,
			wantCheck: func(v VersionInfo) bool {
				// When version is "dev" with a commit, version should be "build-abc123de" (8 chars)
				return v.Version == "build-abc123de" &&
					v.Commit == "abc123def456789" &&
					v.BuildDate == unknownStr &&
					v.GoVersion == runtime.Version() &&
					v.Platform == fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
			},
		},
		{
			name:      "release version",
			version:   "v1.2.3",
			commit:    "abc123def456789",
			buildDate: "2024-01-15T10:30:00Z",
			wantCheck: func(v VersionInfo) bool {
				return v.Version == "v1.2.3" &&
					v.Commit == "abc123def456789" &&
					v.BuildDate == "2024-01-15 10:30:00 UTC" &&
					v.GoVersion == runtime.Version() &&
					v.Platform == fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
			},
		},
		{
			name:      "custom build version",
			version:   "custom-build-1",
			commit:    "xyz789",
			buildDate: "2024-03-20T15:45:30Z",
			wantCheck: func(v VersionInfo) bool {
				return v.Version == "custom-build-1" &&
					v.Commit == "xyz789" &&
					v.BuildDate == "2024-03-20 15:45:30 UTC" &&
					v.GoVersion == runtime.Version() &&
					v.Platform == fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
			},
		},
		{
			name:      "invalid date format",
			version:   "v2.0.0",
			commit:    "def456",
			buildDate: "not-a-date",
			wantCheck: func(v VersionInfo) bool {
				return v.Version == "v2.0.0" &&
					v.Commit == "def456" &&
					v.BuildDate == "not-a-date" && // Should remain unchanged if not parseable
					v.GoVersion == runtime.Version() &&
					v.Platform == fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
			},
		},
		{
			name:      "dev version with short commit",
			version:   "dev",
			commit:    "short",
			buildDate: unknownStr,
			wantCheck: func(v VersionInfo) bool {
				// When commit is shorter than 8 chars, it should use the full length
				return v.Version == "build-short" &&
					v.Commit == "short" &&
					v.BuildDate == unknownStr
			},
		},
	}

	for _, tt := range tests { //nolint:paralleltest // Test modifies global variables
		t.Run(tt.name, func(t *testing.T) {
			// Cannot run in parallel because parent test modifies global variables
			// Set test values
			Version = tt.version
			Commit = tt.commit
			BuildDate = tt.buildDate

			got := GetVersionInfo()

			if !tt.wantCheck(got) {
				t.Errorf("GetVersionInfo() check failed, got = %+v", got)
			}
		})
	}

	// Restore original values
	Version = origVersion
	Commit = origCommit
	BuildDate = origBuildDate
}

func TestVersionInfo_Fields(t *testing.T) {
	t.Parallel()
	vi := VersionInfo{
		Version:   "test-version",
		Commit:    "test-commit",
		BuildDate: "test-date",
		GoVersion: "test-go-version",
		Platform:  "test-platform",
	}

	if vi.Version != "test-version" {
		t.Errorf("Version = %v, want %v", vi.Version, "test-version")
	}
	if vi.Commit != "test-commit" {
		t.Errorf("Commit = %v, want %v", vi.Commit, "test-commit")
	}
	if vi.BuildDate != "test-date" {
		t.Errorf("BuildDate = %v, want %v", vi.BuildDate, "test-date")
	}
	if vi.GoVersion != "test-go-version" {
		t.Errorf("GoVersion = %v, want %v", vi.GoVersion, "test-go-version")
	}
	if vi.Platform != "test-platform" {
		t.Errorf("Platform = %v, want %v", vi.Platform, "test-platform")
	}
}
