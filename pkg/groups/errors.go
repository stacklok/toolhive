package groups

import "errors"

var (
	// ErrGroupAlreadyExists is returned when a group already exists
	ErrGroupAlreadyExists = errors.New("group already exists")

	// ErrGroupNotFound is returned when a group is not found
	ErrGroupNotFound = errors.New("group not found")

	// ErrInvalidGroupName is returned when an invalid argument is provided
	ErrInvalidGroupName = errors.New("invalid group name")
)
