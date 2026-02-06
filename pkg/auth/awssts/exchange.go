// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package awssts

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
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
//
// It uses regional endpoints (https://sts.{region}.amazonaws.com) for lower latency.
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

// newRegionalSTSClient creates an STS client configured for the regional endpoint.
// Using regional endpoints (https://sts.{region}.amazonaws.com) provides lower latency
// compared to the global endpoint.
func newRegionalSTSClient(ctx context.Context, region string) (STSClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	regionalEndpoint := fmt.Sprintf("https://sts.%s.amazonaws.com", region)
	return sts.NewFromConfig(cfg, func(o *sts.Options) {
		o.BaseEndpoint = aws.String(regionalEndpoint)
	}), nil
}

// ExchangeToken performs AssumeRoleWithWebIdentity to exchange an identity token
// for temporary AWS credentials.
func (e *Exchanger) ExchangeToken(
	ctx context.Context,
	token, roleArn, sessionName string,
	durationSeconds int32,
) (*aws.Credentials, error) {
	if err := validateInputs(token, roleArn, durationSeconds); err != nil {
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
		return nil, fmt.Errorf("STS AssumeRoleWithWebIdentity failed: %w", err)
	}

	if output.Credentials == nil {
		return nil, fmt.Errorf("STS returned nil credentials")
	}

	return &aws.Credentials{
		AccessKeyID:     aws.ToString(output.Credentials.AccessKeyId),
		SecretAccessKey: aws.ToString(output.Credentials.SecretAccessKey),
		SessionToken:    aws.ToString(output.Credentials.SessionToken),
		Expires:         aws.ToTime(output.Credentials.Expiration),
		CanExpire:       true,
	}, nil
}

// validateInputs validates the exchange inputs.
func validateInputs(token, roleArn string, durationSeconds int32) error {
	if token == "" {
		return fmt.Errorf("token is required")
	}

	if err := ValidateRoleArn(roleArn); err != nil {
		return err
	}

	if durationSeconds < MinSessionDuration {
		return fmt.Errorf("session duration %d is below minimum %d seconds", durationSeconds, MinSessionDuration)
	}

	if durationSeconds > MaxSessionDuration {
		return fmt.Errorf("session duration %d exceeds maximum %d seconds", durationSeconds, MaxSessionDuration)
	}

	return nil
}
