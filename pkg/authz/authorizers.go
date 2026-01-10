package authz

// This file imports all authorizer implementations to ensure their init()
// functions are called and they register themselves with the authorizers registry.
//
// When adding a new authorizer implementation, add a blank import here.

import (
	// Import Cedar authorizer to register it
	_ "github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
)
