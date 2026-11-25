// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import "errors"

// ErrAmbiguousSubjectProvider is returned when an xaa or obo strategy's subject
// provider name is empty and more than one upstream is configured, so the
// first-upstream default would be ambiguous.
var ErrAmbiguousSubjectProvider = errors.New(
	"the subject provider name must be set explicitly for xaa and obo when more than one upstream is configured")

// DefaultSubjectProviderName defaults strategy's subject provider name to
// providerName for token_exchange, aws_sts, xaa, and obo strategies whose field
// is empty (SubjectProviderName, or SubjectTokenProviderName for obo),
// returning a deep copy with the field defaulted; the caller's strategy is
// never mutated. Without the default, a strategy behind an embedded auth server
// would use the ToolHive-issued session token as its subject, which the
// upstream token endpoint rejects.
//
// All four strategies share the same wrong-subject risk: picking the wrong
// upstream token is equally security-relevant for each. Only xaa and obo
// hard-error on ambiguity (when hasMultipleUpstreams is true and the field is
// empty, they return ErrAmbiguousSubjectProvider instead of defaulting),
// because neither has existing deployments to break, so there is no
// compatibility cost. token_exchange and aws_sts keep defaulting silently,
// since hard-erroring those would be a breaking change requiring a
// deprecation path (see issue #5697).
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
		return defaultUnlessAmbiguous(strategy, hasMultipleUpstreams, func(c *BackendAuthStrategy) {
			c.XAA.SubjectProviderName = providerName
		})

	case StrategyTypeOBO:
		if strategy.OBO == nil || strategy.OBO.SubjectTokenProviderName != "" {
			return strategy, nil
		}
		return defaultUnlessAmbiguous(strategy, hasMultipleUpstreams, func(c *BackendAuthStrategy) {
			c.OBO.SubjectTokenProviderName = providerName
		})

	default:
		return strategy, nil
	}
}

// defaultUnlessAmbiguous returns a deep copy of strategy with set applied, or
// ErrAmbiguousSubjectProvider when more than one upstream is configured. It
// serves the strategies that hard-error on ambiguity (xaa and obo).
func defaultUnlessAmbiguous(
	strategy *BackendAuthStrategy,
	hasMultipleUpstreams bool,
	set func(*BackendAuthStrategy),
) (*BackendAuthStrategy, error) {
	if hasMultipleUpstreams {
		return nil, ErrAmbiguousSubjectProvider
	}
	copied := strategy.DeepCopy()
	set(copied)
	return copied, nil
}
