// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import "context"

// vmHandle abstracts the operations on a go-microvm VM so that tests can
// inject a fake. *microvm.VM satisfies this interface natively.
type vmHandle interface {
	Stop(ctx context.Context) error
	Remove(ctx context.Context) error
	DataDir() string
	Name() string
}
