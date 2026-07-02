// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import "errors"

// ErrAmbiguousSubjectProvider is returned when an xaa strategy's
// SubjectProviderName is empty and more than one upstream is configured, so
// the first-upstream default would be ambiguous.
var ErrAmbiguousSubjectProvider = errors.New(
	"SubjectProviderName must be set explicitly for xaa when more than one upstream is configured")

// DefaultSubjectProviderName defaults strategy's SubjectProviderName to
// providerName for token_exchange, aws_sts, and xaa strategies whose field is
// empty, returning a copy (the caller's strategy is never mutated in place).
// For xaa, if hasMultipleUpstreams is true and SubjectProviderName is empty,
// returns ErrAmbiguousSubjectProvider instead of silently defaulting, since
// sending the wrong subject token to Step A is a security-relevant mistake
// specific to xaa. token_exchange and aws_sts always default silently.
//
// Strategies that are nil, of a different type, missing their per-type
// sub-config, or that already have SubjectProviderName set are returned
// unchanged via the original pointer.
func DefaultSubjectProviderName(
	strategy *BackendAuthStrategy,
	providerName string,
	hasMultipleUpstreams bool,
) (*BackendAuthStrategy, error) {
	if strategy == nil {
		return nil, nil
	}

	switch strategy.Type {
	case StrategyTypeTokenExchange:
		if strategy.TokenExchange == nil || strategy.TokenExchange.SubjectProviderName != "" {
			return strategy, nil
		}
		copied := *strategy
		teCopied := *strategy.TokenExchange
		teCopied.SubjectProviderName = providerName
		copied.TokenExchange = &teCopied
		return &copied, nil

	case StrategyTypeAwsSts:
		if strategy.AwsSts == nil || strategy.AwsSts.SubjectProviderName != "" {
			return strategy, nil
		}
		copied := *strategy
		stsCopied := *strategy.AwsSts
		stsCopied.SubjectProviderName = providerName
		copied.AwsSts = &stsCopied
		return &copied, nil

	case StrategyTypeXAA:
		if strategy.XAA == nil || strategy.XAA.SubjectProviderName != "" {
			return strategy, nil
		}
		if hasMultipleUpstreams {
			return nil, ErrAmbiguousSubjectProvider
		}
		copied := *strategy
		xaaCopied := *strategy.XAA
		xaaCopied.SubjectProviderName = providerName
		copied.XAA = &xaaCopied
		return &copied, nil

	default:
		return strategy, nil
	}
}
