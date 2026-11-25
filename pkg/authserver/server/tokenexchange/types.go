// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import "context"

// SubjectTokenValidator validates subject tokens presented during RFC 8693 token exchange.
type SubjectTokenValidator interface {
	Validate(ctx context.Context, rawToken string) (*ValidatedClaims, error)
}
