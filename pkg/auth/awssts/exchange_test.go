package awssts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/sts/types"
)

// mockSTSClient implements STSClient for testing.
type mockSTSClient struct {
	response *sts.AssumeRoleWithWebIdentityOutput
	err      error
}

func (m *mockSTSClient) AssumeRoleWithWebIdentity(
	_ context.Context,
	_ *sts.AssumeRoleWithWebIdentityInput,
	_ ...func(*sts.Options),
) (*sts.AssumeRoleWithWebIdentityOutput, error) {
	return m.response, m.err
}

func TestExchanger_ExchangeToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	expiration := time.Now().Add(time.Hour)

	tests := []struct {
		name        string
		token       string
		roleArn     string
		sessionName string
		duration    int32
		mockResp    *sts.AssumeRoleWithWebIdentityOutput
		mockErr     error
		wantErr     error
	}{
		{
			name:        "successful exchange",
			token:       "valid-token",
			roleArn:     "arn:aws:iam::123456789012:role/TestRole",
			sessionName: "test-session",
			duration:    3600,
			mockResp: &sts.AssumeRoleWithWebIdentityOutput{
				Credentials: &types.Credentials{
					AccessKeyId:     strPtr("AKIATEST"),
					SecretAccessKey: strPtr("secret-key"),
					SessionToken:    strPtr("session-token"),
					Expiration:      &expiration,
				},
			},
			mockErr: nil,
			wantErr: nil,
		},
		{
			name:        "empty token",
			token:       "",
			roleArn:     "arn:aws:iam::123456789012:role/TestRole",
			sessionName: "test-session",
			duration:    3600,
			mockResp:    nil,
			mockErr:     nil,
			wantErr:     ErrInvalidToken,
		},
		{
			name:        "empty role ARN",
			token:       "valid-token",
			roleArn:     "",
			sessionName: "test-session",
			duration:    3600,
			mockResp:    nil,
			mockErr:     nil,
			wantErr:     ErrInvalidRoleArn,
		},
		{
			name:        "STS returns no credentials",
			token:       "valid-token",
			roleArn:     "arn:aws:iam::123456789012:role/TestRole",
			sessionName: "test-session",
			duration:    3600,
			mockResp:    &sts.AssumeRoleWithWebIdentityOutput{},
			mockErr:     nil,
			wantErr:     nil, // Not ErrSTSUnavailable, just a wrapped error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockSTSClient{
				response: tt.mockResp,
				err:      tt.mockErr,
			}
			exchanger, err := NewExchangerWithClient(client, "us-east-1")
			if err != nil {
				t.Fatalf("NewExchangerWithClient() error = %v", err)
			}

			creds, err := exchanger.ExchangeToken(ctx, tt.token, tt.roleArn, tt.sessionName, tt.duration)

			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("ExchangeToken() expected error %v, got nil", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) {
					t.Errorf("ExchangeToken() error = %v, want %v", err, tt.wantErr)
				}
				return
			}

			// For "STS returns no credentials" case, we expect an error but not a typed one
			if tt.name == "STS returns no credentials" {
				if err == nil {
					t.Error("ExchangeToken() expected error for nil credentials, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("ExchangeToken() unexpected error = %v", err)
				return
			}

			if creds == nil {
				t.Error("ExchangeToken() returned nil credentials")
				return
			}

			if creds.AccessKeyID != "AKIATEST" {
				t.Errorf("AccessKeyID = %v, want AKIATEST", creds.AccessKeyID)
			}
		})
	}
}

func TestExchanger_mapSTSError(t *testing.T) {
	t.Parallel()

	// Create an exchanger to test the method
	client := &mockSTSClient{}
	exchanger, _ := NewExchangerWithClient(client, "us-east-1")

	tests := []struct {
		name    string
		err     error
		wantErr error
	}{
		{
			name:    "invalid identity token error",
			err:     errors.New("InvalidIdentityToken: token expired"),
			wantErr: ErrInvalidToken,
		},
		{
			name:    "expired token error",
			err:     errors.New("ExpiredTokenException: token expired"),
			wantErr: ErrInvalidToken,
		},
		{
			name:    "invalid token error",
			err:     errors.New("invalid token"),
			wantErr: ErrInvalidToken,
		},
		{
			name:    "access denied error",
			err:     errors.New("AccessDenied: not allowed"),
			wantErr: ErrAccessDenied,
		},
		{
			name:    "not authorized error",
			err:     errors.New("NotAuthorized: user cannot assume role"),
			wantErr: ErrAccessDenied,
		},
		{
			name:    "trust policy error",
			err:     errors.New("trust policy does not allow"),
			wantErr: ErrAccessDenied,
		},
		{
			name:    "service unavailable error",
			err:     errors.New("ServiceUnavailable"),
			wantErr: ErrSTSUnavailable,
		},
		{
			name:    "connection error",
			err:     errors.New("connection refused"),
			wantErr: ErrSTSUnavailable,
		},
		{
			name:    "timeout error",
			err:     errors.New("request timeout"),
			wantErr: ErrSTSUnavailable,
		},
		{
			name:    "unknown error passed through",
			err:     errors.New("random error"),
			wantErr: nil, // Should not be one of our typed errors
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := exchanger.mapSTSError(tt.err)

			if tt.wantErr == nil {
				// For unknown errors, it should be wrapped but not be our typed errors
				if errors.Is(result, ErrInvalidToken) || errors.Is(result, ErrAccessDenied) || errors.Is(result, ErrSTSUnavailable) {
					t.Errorf("mapSTSError() should not return typed error for unknown input, got %v", result)
				}
				return
			}

			if !errors.Is(result, tt.wantErr) {
				t.Errorf("mapSTSError() = %v, want error wrapping %v", result, tt.wantErr)
			}
		})
	}
}

// strPtr is a helper to create string pointers for AWS SDK types.
func strPtr(s string) *string {
	return &s
}
