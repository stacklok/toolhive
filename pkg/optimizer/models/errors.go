// Package models defines domain models for the optimizer.
// It includes structures for MCP servers, tools, and related metadata.
package models

import "errors"

var (
	// ErrRemoteServerMissingURL is returned when a remote server doesn't have a URL
	ErrRemoteServerMissingURL = errors.New("remote servers must have URL")

	// ErrContainerServerMissingPackage is returned when a container server doesn't have a package
	ErrContainerServerMissingPackage = errors.New("container servers must have package")

	// ErrInvalidTokenMetrics is returned when token metrics are inconsistent
	ErrInvalidTokenMetrics = errors.New("invalid token metrics: calculated values don't match")
)
