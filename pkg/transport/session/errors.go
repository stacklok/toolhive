package session

import (
	"errors"
	"net/http"

	thverrors "github.com/stacklok/toolhive/pkg/errors"
)

// Common session errors
var (
	// ErrSessionDisconnected is returned when trying to send to a disconnected session
	ErrSessionDisconnected = thverrors.WithCode(
		errors.New("session is disconnected"),
		http.StatusServiceUnavailable,
	)

	// ErrMessageChannelFull is returned when the message channel is full
	ErrMessageChannelFull = thverrors.WithCode(
		errors.New("message channel is full"),
		http.StatusServiceUnavailable,
	)

	// ErrSessionNotFound is returned when a session cannot be found
	ErrSessionNotFound = thverrors.WithCode(
		errors.New("session not found"),
		http.StatusNotFound,
	)

	// ErrSessionAlreadyExists is returned when trying to create a session with an existing ID
	ErrSessionAlreadyExists = thverrors.WithCode(
		errors.New("session already exists"),
		http.StatusConflict,
	)

	// ErrInvalidSessionType is returned when an invalid session type is provided
	ErrInvalidSessionType = thverrors.WithCode(
		errors.New("invalid session type"),
		http.StatusBadRequest,
	)
)
