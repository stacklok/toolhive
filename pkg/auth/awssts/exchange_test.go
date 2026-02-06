// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package awssts

import (
	"context"
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
			wantAnyErr:  true,
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
			name:        "STS returns nil credentials",
			token:       "valid-token",
			roleArn:     "arn:aws:iam::123456789012:role/TestRole",
			sessionName: "test-session",
			duration:    3600,
			mockResp:    &sts.AssumeRoleWithWebIdentityOutput{},
			wantAnyErr:  true,
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
