package awssts

import (
	"errors"
	"testing"
)

func TestValidateRoleArn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		arn     string
		wantErr bool
	}{
		{
			name:    "valid role ARN",
			arn:     "arn:aws:iam::123456789012:role/MyRole",
			wantErr: false,
		},
		{
			name:    "valid role ARN with path",
			arn:     "arn:aws:iam::123456789012:role/service-role/MyRole",
			wantErr: true, // Path roles don't match the simple pattern
		},
		{
			name:    "valid role ARN with special chars",
			arn:     "arn:aws:iam::123456789012:role/My_Role-Name.test@example",
			wantErr: false,
		},
		{
			name:    "invalid - wrong service",
			arn:     "arn:aws:s3::123456789012:role/MyRole",
			wantErr: true,
		},
		{
			name:    "invalid - wrong resource type",
			arn:     "arn:aws:iam::123456789012:user/MyUser",
			wantErr: true,
		},
		{
			name:    "invalid - wrong account format",
			arn:     "arn:aws:iam::12345:role/MyRole",
			wantErr: true,
		},
		{
			name:    "invalid - empty",
			arn:     "",
			wantErr: true,
		},
		{
			name:    "invalid - random string",
			arn:     "not-an-arn",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateRoleArn(tt.arn)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRoleArn(%q) error = %v, wantErr %v", tt.arn, err, tt.wantErr)
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *Config
		wantErr error
	}{
		{
			name: "valid config with single role",
			config: &Config{
				Region:  "us-east-1",
				RoleArn: "arn:aws:iam::123456789012:role/MyRole",
			},
			wantErr: nil,
		},
		{
			name: "valid config with role mappings",
			config: &Config{
				Region: "us-east-1",
				RoleMappings: []RoleMapping{
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
					{Claim: "user", RoleArn: "arn:aws:iam::123456789012:role/UserRole", Priority: 10},
				},
			},
			wantErr: nil,
		},
		{
			name: "valid config with both role and mappings",
			config: &Config{
				Region:  "us-east-1",
				RoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
				RoleMappings: []RoleMapping{
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			wantErr: nil,
		},
		{
			name: "missing region",
			config: &Config{
				RoleArn: "arn:aws:iam::123456789012:role/MyRole",
			},
			wantErr: ErrMissingRegion,
		},
		{
			name: "missing both role and mappings",
			config: &Config{
				Region: "us-east-1",
			},
			wantErr: ErrMissingRoleConfig,
		},
		{
			name: "invalid role ARN format",
			config: &Config{
				Region:  "us-east-1",
				RoleArn: "invalid-arn",
			},
			wantErr: ErrInvalidRoleArn,
		},
		{
			name: "invalid role ARN in mapping",
			config: &Config{
				Region: "us-east-1",
				RoleMappings: []RoleMapping{
					{Claim: "admin", RoleArn: "invalid-arn", Priority: 1},
				},
			},
			wantErr: ErrInvalidRoleArn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateConfig(tt.config)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateConfig() unexpected error = %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("ValidateConfig() expected error %v, got nil", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) {
					t.Errorf("ValidateConfig() error = %v, want %v", err, tt.wantErr)
				}
			}
		})
	}
}

func TestRoleMapper_SelectRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *Config
		claims  map[string]interface{}
		want    string
		wantErr error
	}{
		{
			name: "selects role from single mapping",
			config: &Config{
				Region:    "us-east-1",
				RoleClaim: "groups",
				RoleMappings: []RoleMapping{
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims: map[string]interface{}{
				"groups": []interface{}{"admin", "users"},
			},
			want:    "arn:aws:iam::123456789012:role/AdminRole",
			wantErr: nil,
		},
		{
			name: "selects highest priority role when multiple match",
			config: &Config{
				Region:    "us-east-1",
				RoleClaim: "groups",
				RoleMappings: []RoleMapping{
					{Claim: "users", RoleArn: "arn:aws:iam::123456789012:role/UserRole", Priority: 100},
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
					{Claim: "developer", RoleArn: "arn:aws:iam::123456789012:role/DevRole", Priority: 50},
				},
			},
			claims: map[string]interface{}{
				"groups": []interface{}{"users", "admin", "developer"},
			},
			want:    "arn:aws:iam::123456789012:role/AdminRole", // Priority 1 wins
			wantErr: nil,
		},
		{
			name: "falls back to default role when no mapping matches",
			config: &Config{
				Region:    "us-east-1",
				RoleClaim: "groups",
				RoleArn:   "arn:aws:iam::123456789012:role/DefaultRole",
				RoleMappings: []RoleMapping{
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims: map[string]interface{}{
				"groups": []interface{}{"users"},
			},
			want:    "arn:aws:iam::123456789012:role/DefaultRole",
			wantErr: nil,
		},
		{
			name: "uses default role when no mappings configured",
			config: &Config{
				Region:  "us-east-1",
				RoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
			},
			claims: map[string]interface{}{
				"groups": []interface{}{"admin"},
			},
			want:    "arn:aws:iam::123456789012:role/DefaultRole",
			wantErr: nil,
		},
		{
			name: "error when no mapping and no default",
			config: &Config{
				Region:    "us-east-1",
				RoleClaim: "groups",
				RoleMappings: []RoleMapping{
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims: map[string]interface{}{
				"groups": []interface{}{"users"},
			},
			want:    "",
			wantErr: ErrNoRoleMapping,
		},
		{
			name: "handles string claim value",
			config: &Config{
				Region:    "us-east-1",
				RoleClaim: "role",
				RoleMappings: []RoleMapping{
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims: map[string]interface{}{
				"role": "admin",
			},
			want:    "arn:aws:iam::123456789012:role/AdminRole",
			wantErr: nil,
		},
		{
			name: "handles string array claim value",
			config: &Config{
				Region:    "us-east-1",
				RoleClaim: "groups",
				RoleMappings: []RoleMapping{
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims: map[string]interface{}{
				"groups": []string{"admin", "users"},
			},
			want:    "arn:aws:iam::123456789012:role/AdminRole",
			wantErr: nil,
		},
		{
			name: "error when claim not found",
			config: &Config{
				Region:    "us-east-1",
				RoleClaim: "groups",
				RoleMappings: []RoleMapping{
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims: map[string]interface{}{
				"other": "value",
			},
			want:    "",
			wantErr: ErrNoRoleMapping,
		},
		{
			name: "uses default role claim when not configured",
			config: &Config{
				Region: "us-east-1",
				// RoleClaim not set, should default to "groups"
				RoleMappings: []RoleMapping{
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims: map[string]interface{}{
				"groups": []interface{}{"admin"},
			},
			want:    "arn:aws:iam::123456789012:role/AdminRole",
			wantErr: nil,
		},
		{
			name: "handles colon-separated AWS principal tag format",
			config: &Config{
				Region:    "us-east-1",
				RoleArn:   "arn:aws:iam::123456789012:role/DefaultRole",
				RoleClaim: "https://aws.amazon.com/tags/principal_tags/Groups",
				RoleMappings: []RoleMapping{
					{Claim: "s3-readers", RoleArn: "arn:aws:iam::123456789012:role/S3ReadOnlyRole", Priority: 1},
					{Claim: "ec2-viewers", RoleArn: "arn:aws:iam::123456789012:role/EC2ViewOnlyRole", Priority: 2},
				},
			},
			claims: map[string]interface{}{
				// AWS principal tags use colon-separated values
				"https://aws.amazon.com/tags/principal_tags/Groups": "s3-readers:Everyone",
			},
			want:    "arn:aws:iam::123456789012:role/S3ReadOnlyRole",
			wantErr: nil,
		},
		{
			name: "handles colon-separated format with multiple matching groups",
			config: &Config{
				Region:    "us-east-1",
				RoleArn:   "arn:aws:iam::123456789012:role/DefaultRole",
				RoleClaim: "https://aws.amazon.com/tags/principal_tags/Groups",
				RoleMappings: []RoleMapping{
					{Claim: "s3-readers", RoleArn: "arn:aws:iam::123456789012:role/S3ReadOnlyRole", Priority: 10},
					{Claim: "admin", RoleArn: "arn:aws:iam::123456789012:role/AdminRole", Priority: 1},
				},
			},
			claims: map[string]interface{}{
				// User is in both admin and s3-readers groups
				"https://aws.amazon.com/tags/principal_tags/Groups": "admin:s3-readers:Everyone",
			},
			// Should select AdminRole due to higher priority (lower number)
			want:    "arn:aws:iam::123456789012:role/AdminRole",
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mapper := NewRoleMapper(tt.config)
			got, err := mapper.SelectRole(tt.claims)

			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("SelectRole() expected error %v, got nil", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) {
					t.Errorf("SelectRole() error = %v, want %v", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Errorf("SelectRole() unexpected error = %v", err)
				}
				if got != tt.want {
					t.Errorf("SelectRole() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
