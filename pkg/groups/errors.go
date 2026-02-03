// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package groups

import (
	"errors"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
)

var (
	// ErrGroupAlreadyExists is returned when a group already exists
	ErrGroupAlreadyExists = httperr.WithCode(
		errors.New("group already exists"),
		http.StatusConflict,
	)

	// ErrGroupNotFound is returned when a group is not found
	ErrGroupNotFound = httperr.WithCode(
		errors.New("group not found"),
		http.StatusNotFound,
	)

	// ErrInvalidGroupName is returned when an invalid argument is provided
	ErrInvalidGroupName = httperr.WithCode(
		errors.New("invalid group name"),
		http.StatusBadRequest,
	)
)
