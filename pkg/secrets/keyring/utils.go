// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package keyring

import (
	"crypto/rand"
	"fmt"
	"time"
)

// GenerateUniqueTestKey creates a unique key name used for keyring availability checks.
// It combines a timestamp and random bytes to prevent naming collisions
// when multiple checks run concurrently.
func GenerateUniqueTestKey() string {
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		return fmt.Sprintf("toolhive-keyring-test-%d", time.Now().UnixNano())
	}

	return fmt.Sprintf("toolhive-keyring-test-%d-%x", time.Now().UnixNano(), randomBytes)
}
