package session

import "errors"

// Common session errors
var (
	// ErrSessionDisconnected is returned when trying to send to a disconnected session
	ErrSessionDisconnected = errors.New("session is disconnected")

	// ErrMessageChannelFull is returned when the message channel is full
	ErrMessageChannelFull = errors.New("message channel is full")

	// ErrSessionNotFound is returned when a session cannot be found
	ErrSessionNotFound = errors.New("session not found")

	// ErrSessionAlreadyExists is returned when trying to create a session with an existing ID
	ErrSessionAlreadyExists = errors.New("session already exists")

	// ErrInvalidSessionType is returned when an invalid session type is provided
	ErrInvalidSessionType = errors.New("invalid session type")
)
