// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"errors"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
)

var (
	// ErrNotFound is returned when a requested resource does not exist.
	ErrNotFound = httperr.WithCode(
		errors.New("resource not found"),
		http.StatusNotFound,
	)

	// ErrAlreadyExists is returned when a resource already exists.
	ErrAlreadyExists = httperr.WithCode(
		errors.New("resource already exists"),
		http.StatusConflict,
	)
)
