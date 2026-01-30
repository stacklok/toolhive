// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package groups

import (
	"errors"
	"net/http"

	thverrors "github.com/stacklok/toolhive/pkg/errors"
)

var (
	// ErrGroupAlreadyExists is returned when a group already exists
	ErrGroupAlreadyExists = thverrors.WithCode(
		errors.New("group already exists"),
		http.StatusConflict,
	)

	// ErrGroupNotFound is returned when a group is not found
	ErrGroupNotFound = thverrors.WithCode(
		errors.New("group not found"),
		http.StatusNotFound,
	)

	// ErrInvalidGroupName is returned when an invalid argument is provided
	ErrInvalidGroupName = thverrors.WithCode(
		errors.New("invalid group name"),
		http.StatusBadRequest,
	)
)
