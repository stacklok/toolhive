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
// empty, returning a deep copy with the field defaulted; the caller's
// strategy is never mutated.
//
// All three strategies share the same wrong-subject risk: picking the wrong
// upstream token via SubjectProviderName is equally security-relevant for
// each. Only xaa hard-errors on ambiguity (when hasMultipleUpstreams is true
// and SubjectProviderName is empty, it returns ErrAmbiguousSubjectProvider
// instead of defaulting), because xaa has no existing deployments to break,
// so there is no compatibility cost. token_exchange and aws_sts keep
// defaulting silently, since hard-erroring those would be a breaking change
// requiring a deprecation path (see issue #5697).
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
		copied := strategy.DeepCopy()
		copied.TokenExchange.SubjectProviderName = providerName
		return copied, nil

	case StrategyTypeAwsSts:
		if strategy.AwsSts == nil || strategy.AwsSts.SubjectProviderName != "" {
			return strategy, nil
		}
		copied := strategy.DeepCopy()
		copied.AwsSts.SubjectProviderName = providerName
		return copied, nil

	case StrategyTypeXAA:
		if strategy.XAA == nil || strategy.XAA.SubjectProviderName != "" {
			return strategy, nil
		}
		if hasMultipleUpstreams {
			return nil, ErrAmbiguousSubjectProvider
		}
		copied := strategy.DeepCopy()
		copied.XAA.SubjectProviderName = providerName
		return copied, nil

	default:
		return strategy, nil
	}
}
