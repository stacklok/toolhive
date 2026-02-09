// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package awssts

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		wantAnyErr  bool
	}{
		{
			name:        "successful exchange",
			token:       "valid-token",
			roleArn:     "arn:aws:iam::123456789012:role/TestRole",
			sessionName: "test-session",
			duration:    3600,
			mockResp: &sts.AssumeRoleWithWebIdentityOutput{
				Credentials: &types.Credentials{
					AccessKeyId:     aws.String("AKIATEST"),
					SecretAccessKey: aws.String("secret-key"),
					SessionToken:    aws.String("session-token"),
					Expiration:      &expiration,
				},
			},
		},
		{
			name:        "empty token",
			token:       "",
			roleArn:     "arn:aws:iam::123456789012:role/TestRole",
			sessionName: "test-session",
			duration:    3600,
			wantErr:     ErrMissingToken,
		},
		{
			name:        "empty role ARN",
			token:       "valid-token",
			roleArn:     "",
			sessionName: "test-session",
			duration:    3600,
			wantErr:     ErrInvalidRoleArn,
		},
		{
			name:        "session name too short",
			token:       "valid-token",
			roleArn:     "arn:aws:iam::123456789012:role/TestRole",
			sessionName: "x",
			duration:    3600,
			wantErr:     ErrInvalidSessionName,
		},
		{
			name:        "session name with invalid characters",
			token:       "valid-token",
			roleArn:     "arn:aws:iam::123456789012:role/TestRole",
			sessionName: "auth0|user123",
			duration:    3600,
			wantErr:     ErrInvalidSessionName,
		},
		{
			name:        "STS returns nil credentials",
			token:       "valid-token",
			roleArn:     "arn:aws:iam::123456789012:role/TestRole",
			sessionName: "test-session",
			duration:    3600,
			mockResp:    &sts.AssumeRoleWithWebIdentityOutput{},
			wantErr:     ErrSTSNilCredentials,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &mockSTSClient{
				response: tt.mockResp,
				err:      tt.mockErr,
			}
			exchanger := &Exchanger{client: client}

			creds, err := exchanger.ExchangeToken(ctx, tt.token, tt.roleArn, tt.sessionName, tt.duration)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}

			if tt.wantAnyErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, creds)
			assert.Equal(t, "AKIATEST", creds.AccessKeyID)
		})
	}
}

func TestValidateSessionName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid simple", input: "test-session", wantErr: false},
		{name: "valid with allowed specials", input: "user@domain_+=,.@-", wantErr: false},
		{name: "valid minimum length", input: "ab", wantErr: false},
		{name: "valid 64 chars", input: strings.Repeat("a", 64), wantErr: false},
		{name: "too short", input: "x", wantErr: true},
		{name: "empty", input: "", wantErr: true},
		{name: "too long", input: strings.Repeat("a", 65), wantErr: true},
		{name: "pipe char", input: "auth0|user", wantErr: true},
		{name: "space", input: "has space", wantErr: true},
		{name: "slash", input: "path/name", wantErr: true},
		{name: "colon", input: "a:b", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateSessionName(tt.input)
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrInvalidSessionName)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
