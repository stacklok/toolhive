// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import "fmt"

// WebhookError is the base error type for all webhook-related errors.
//
//nolint:revive // WebhookError is the canonical name; renaming to Error conflicts with Error() method.
type WebhookError struct {
	// WebhookName is the name of the webhook that caused the error.
	WebhookName string
	// Err is the underlying error.
	Err error
}

// Error implements the error interface.
func (e *WebhookError) Error() string {
	return fmt.Sprintf("webhook %q: %v", e.WebhookName, e.Err)
}

// Unwrap returns the underlying error for errors.Is/errors.As support.
func (e *WebhookError) Unwrap() error {
	return e.Err
}

// TimeoutError indicates that a webhook call timed out.
type TimeoutError struct {
	WebhookError
}

// Error implements the error interface.
func (e *TimeoutError) Error() string {
	return fmt.Sprintf("webhook %q: timeout: %v", e.WebhookName, e.Err)
}

// NetworkError indicates a network-level failure when calling a webhook.
type NetworkError struct {
	WebhookError
}

// Error implements the error interface.
func (e *NetworkError) Error() string {
	return fmt.Sprintf("webhook %q: network error: %v", e.WebhookName, e.Err)
}

// InvalidResponseError indicates that a webhook returned an unparsable or invalid response.
type InvalidResponseError struct {
	WebhookError
}

// Error implements the error interface.
func (e *InvalidResponseError) Error() string {
	return fmt.Sprintf("webhook %q: invalid response: %v", e.WebhookName, e.Err)
}

// NewTimeoutError creates a new TimeoutError.
func NewTimeoutError(webhookName string, err error) *TimeoutError {
	return &TimeoutError{
		WebhookError: WebhookError{
			WebhookName: webhookName,
			Err:         err,
		},
	}
}

// NewNetworkError creates a new NetworkError.
func NewNetworkError(webhookName string, err error) *NetworkError {
	return &NetworkError{
		WebhookError: WebhookError{
			WebhookName: webhookName,
			Err:         err,
		},
	}
}

// NewInvalidResponseError creates a new InvalidResponseError.
func NewInvalidResponseError(webhookName string, err error) *InvalidResponseError {
	return &InvalidResponseError{
		WebhookError: WebhookError{
			WebhookName: webhookName,
			Err:         err,
		},
	}
}
