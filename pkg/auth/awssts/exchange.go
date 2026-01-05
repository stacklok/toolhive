package awssts

import (
	"context"
	"fmt"
	"strings"

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
	region string
}

// NewExchanger creates a new Exchanger with a real STS client configured for the specified region.
// It uses regional STS endpoints (https://sts.{region}.amazonaws.com) for lower latency.
func NewExchanger(ctx context.Context, region string) (*Exchanger, error) {
	if region == "" {
		return nil, ErrMissingRegion
	}

	// Build regional STS endpoint
	regionalEndpoint := fmt.Sprintf("https://sts.%s.amazonaws.com", region)

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := sts.NewFromConfig(cfg, func(o *sts.Options) {
		o.BaseEndpoint = aws.String(regionalEndpoint)
	})

	return &Exchanger{
		client: client,
		region: region,
	}, nil
}

// NewExchangerWithClient creates a new Exchanger with a provided STS client.
// This is primarily used for testing with mock clients.
func NewExchangerWithClient(client STSClient, region string) (*Exchanger, error) {
	if region == "" {
		return nil, ErrMissingRegion
	}
	if client == nil {
		return nil, fmt.Errorf("STS client is required")
	}

	return &Exchanger{
		client: client,
		region: region,
	}, nil
}

// ExchangeToken performs AssumeRoleWithWebIdentity to exchange an identity token
// for temporary AWS credentials.
func (e *Exchanger) ExchangeToken(
	ctx context.Context,
	token, roleArn, sessionName string,
	durationSeconds int32,
) (*Credentials, error) {
	if err := e.validateInputs(token, roleArn, durationSeconds); err != nil {
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
		return nil, e.mapSTSError(err)
	}

	if output.Credentials == nil {
		return nil, fmt.Errorf("STS returned nil credentials")
	}

	return &Credentials{
		AccessKeyID:     aws.ToString(output.Credentials.AccessKeyId),
		SecretAccessKey: aws.ToString(output.Credentials.SecretAccessKey),
		SessionToken:    aws.ToString(output.Credentials.SessionToken),
		Expiration:      aws.ToTime(output.Credentials.Expiration),
	}, nil
}

// Region returns the configured AWS region.
func (e *Exchanger) Region() string {
	return e.region
}

// validateInputs validates the exchange inputs.
func (*Exchanger) validateInputs(token, roleArn string, durationSeconds int32) error {
	if token == "" {
		return fmt.Errorf("%w: token is empty", ErrInvalidToken)
	}

	if roleArn == "" {
		return fmt.Errorf("%w: role ARN is empty", ErrInvalidRoleArn)
	}

	if !RoleArnPattern.MatchString(roleArn) {
		return fmt.Errorf("%w: %s", ErrInvalidRoleArn, roleArn)
	}

	if durationSeconds < MinSessionDuration {
		return fmt.Errorf("session duration %d is below minimum %d seconds", durationSeconds, MinSessionDuration)
	}

	if durationSeconds > MaxSessionDuration {
		return fmt.Errorf("session duration %d exceeds maximum %d seconds", durationSeconds, MaxSessionDuration)
	}

	return nil
}

// mapSTSError maps STS errors to typed errors.
func (*Exchanger) mapSTSError(err error) error {
	errStr := err.Error()

	// Check for invalid token errors
	if strings.Contains(errStr, "InvalidIdentityToken") ||
		strings.Contains(errStr, "ExpiredTokenException") ||
		strings.Contains(errStr, "invalid") && strings.Contains(errStr, "token") {
		return fmt.Errorf("%w: %s", ErrInvalidToken, err)
	}

	// Check for access denied errors
	if strings.Contains(errStr, "AccessDenied") ||
		strings.Contains(errStr, "NotAuthorized") ||
		strings.Contains(errStr, "trust policy") {
		return fmt.Errorf("%w: %s", ErrAccessDenied, err)
	}

	// Check for service unavailable errors
	if strings.Contains(errStr, "ServiceUnavailable") ||
		strings.Contains(errStr, "ServiceException") ||
		strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "timeout") {
		return fmt.Errorf("%w: %s", ErrSTSUnavailable, err)
	}

	// Return original error wrapped with context
	return fmt.Errorf("STS AssumeRoleWithWebIdentity failed: %w", err)
}
