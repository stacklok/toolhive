package awssts

import (
	"testing"
	"time"
)

func TestCredentials_IsExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expiration time.Time
		want       bool
	}{
		{
			name:       "not expired - future expiration",
			expiration: time.Now().Add(time.Hour),
			want:       false,
		},
		{
			name:       "expired - past expiration",
			expiration: time.Now().Add(-time.Hour),
			want:       true,
		},
		{
			name:       "expired - exactly now",
			expiration: time.Now(),
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &Credentials{
				AccessKeyID:     "AKIATEST",
				SecretAccessKey: "secret",
				SessionToken:    "token",
				Expiration:      tt.expiration,
			}
			if got := c.IsExpired(); got != tt.want {
				t.Errorf("IsExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCredentials_ShouldRefresh(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expiration time.Time
		want       bool
	}{
		{
			name:       "should not refresh - plenty of time left",
			expiration: time.Now().Add(time.Hour),
			want:       false,
		},
		{
			name:       "should refresh - within buffer",
			expiration: time.Now().Add(3 * time.Minute), // Less than 5-minute buffer
			want:       true,
		},
		{
			name:       "should refresh - expired",
			expiration: time.Now().Add(-time.Hour),
			want:       true,
		},
		{
			name:       "should not refresh - exactly at buffer boundary",
			expiration: time.Now().Add(RefreshBuffer + time.Second),
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &Credentials{
				AccessKeyID:     "AKIATEST",
				SecretAccessKey: "secret",
				SessionToken:    "token",
				Expiration:      tt.expiration,
			}
			if got := c.ShouldRefresh(); got != tt.want {
				t.Errorf("ShouldRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_GetService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		service string
		want    string
	}{
		{
			name:    "returns configured service",
			service: "custom-service",
			want:    "custom-service",
		},
		{
			name:    "returns default when empty",
			service: "",
			want:    DefaultService,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &Config{Service: tt.service}
			if got := c.GetService(); got != tt.want {
				t.Errorf("GetService() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_GetRoleClaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		roleClaim string
		want      string
	}{
		{
			name:      "returns configured claim",
			roleClaim: "custom_groups",
			want:      "custom_groups",
		},
		{
			name:      "returns default when empty",
			roleClaim: "",
			want:      DefaultRoleClaim,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &Config{RoleClaim: tt.roleClaim}
			if got := c.GetRoleClaim(); got != tt.want {
				t.Errorf("GetRoleClaim() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_GetSessionDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		sessionDuration int32
		want            int32
	}{
		{
			name:            "returns configured duration",
			sessionDuration: 7200,
			want:            7200,
		},
		{
			name:            "returns default when zero",
			sessionDuration: 0,
			want:            DefaultSessionDuration,
		},
		{
			name:            "clamps to minimum",
			sessionDuration: 100,
			want:            MinSessionDuration,
		},
		{
			name:            "clamps to maximum",
			sessionDuration: 100000,
			want:            MaxSessionDuration,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &Config{SessionDuration: tt.sessionDuration}
			if got := c.GetSessionDuration(); got != tt.want {
				t.Errorf("GetSessionDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}
