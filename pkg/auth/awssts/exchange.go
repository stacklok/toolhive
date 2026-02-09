// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package awssts

import (
	"context"
	"fmt"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/stacklok/toolhive/pkg/logger"
)

// STSClient defines the interface for STS operations, enabling mock injection for testing.
type STSClient interface {
	AssumeRoleWithWebIdentity(
		ctx context.Context,
		params *sts.AssumeRoleWithWebIdentityInput,
		optFns ...func(*sts.Options),
	) (*sts.AssumeRoleWithWebIdentityOutput, error)
}

// Exchanger handles STS token exchange operations.
type Exchanger struct {
	client STSClient
}

// NewExchanger creates a new Exchanger with a regional STS client.
func NewExchanger(ctx context.Context, region string) (*Exchanger, error) {
	if region == "" {
		return nil, ErrMissingRegion
	}

	client, err := newRegionalSTSClient(ctx, region)
	if err != nil {
		return nil, err
	}

	return &Exchanger{client: client}, nil
}

// newRegionalSTSClient creates an STS client configured for the specified region.
// The SDK automatically resolves regional STS endpoints for lower latency.
func newRegionalSTSClient(ctx context.Context, region string) (STSClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(aws.AnonymousCredentials{}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return sts.NewFromConfig(cfg), nil
}

// ExchangeToken performs AssumeRoleWithWebIdentity to exchange an identity token
// for temporary AWS credentials.
func (e *Exchanger) ExchangeToken(
	ctx context.Context,
	token, roleArn, sessionName string,
	durationSeconds int32,
) (*aws.Credentials, error) {
	if err := validateInputs(token, roleArn, sessionName, durationSeconds); err != nil {
		return nil, err
	}

	input := &sts.AssumeRoleWithWebIdentityInput{
		RoleArn:          aws.String(roleArn),
		RoleSessionName:  aws.String(sessionName),
		WebIdentityToken: aws.String(token),
		DurationSeconds:  aws.Int32(durationSeconds),
	}

	output, err := e.client.AssumeRoleWithWebIdentity(ctx, input)
	if err != nil {
		logger.Debugf("STS AssumeRoleWithWebIdentity failed: %v", err)
		return nil, ErrSTSExchangeFailed
	}

	if output == nil || output.Credentials == nil {
		return nil, ErrSTSNilCredentials
	}

	return &aws.Credentials{
		AccessKeyID:     aws.ToString(output.Credentials.AccessKeyId),
		SecretAccessKey: aws.ToString(output.Credentials.SecretAccessKey),
		SessionToken:    aws.ToString(output.Credentials.SessionToken),
		Expires:         aws.ToTime(output.Credentials.Expiration),
		CanExpire:       true,
	}, nil
}

// sessionNamePattern validates AWS RoleSessionName values.
// AWS allows: letters (a-z, A-Z), digits (0-9), and the characters _+=,.@-
// See: https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRoleWithWebIdentity.html
var sessionNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_+=,.@-]+$`)

const (
	// minSessionNameLen is the minimum length for an AWS RoleSessionName.
	minSessionNameLen = 2
	// maxSessionNameLen is the maximum length for an AWS RoleSessionName.
	maxSessionNameLen = 64
)

// ValidateSessionName checks that a session name meets AWS RoleSessionName constraints:
// 2-64 characters, only letters, digits, and _+=,.@- are allowed.
func ValidateSessionName(name string) error {
	if len(name) < minSessionNameLen {
		return fmt.Errorf("%w: must be at least %d characters", ErrInvalidSessionName, minSessionNameLen)
	}
	if len(name) > maxSessionNameLen {
		return fmt.Errorf("%w: must be at most %d characters", ErrInvalidSessionName, maxSessionNameLen)
	}
	if !sessionNamePattern.MatchString(name) {
		return fmt.Errorf("%w: contains invalid characters (allowed: letters, digits, _+=,.@-)", ErrInvalidSessionName)
	}
	return nil
}

// validateInputs validates the exchange inputs.
func validateInputs(token, roleArn, sessionName string, durationSeconds int32) error {
	if token == "" {
		return ErrMissingToken
	}

	if err := ValidateRoleArn(roleArn); err != nil {
		return err
	}

	if err := ValidateSessionName(sessionName); err != nil {
		return err
	}

	if durationSeconds < MinSessionDuration {
		return fmt.Errorf("%w: %d is below minimum %d seconds", ErrInvalidSessionDuration, durationSeconds, MinSessionDuration)
	}

	if durationSeconds > MaxSessionDuration {
		return fmt.Errorf("%w: %d exceeds maximum %d seconds", ErrInvalidSessionDuration, durationSeconds, MaxSessionDuration)
	}

	return nil
}
