// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package initbin

import _ "embed"

// Binary contains the embedded thv-vm-init binary.
//
//go:embed thv-vm-init
var Binary []byte
