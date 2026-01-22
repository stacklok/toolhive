// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package handlers provides HTTP handlers for the OAuth 2.0 authorization server endpoints.
//
// This package implements the HTTP layer for the authorization server, including:
//   - OIDC Discovery endpoint (/.well-known/openid-configuration)
//   - JWKS endpoint (/.well-known/jwks.json)
//   - OAuth endpoints (authorize, token, callback, register) - to be implemented
//
// The Handler struct coordinates all handlers and provides route registration methods
// for integrating with standard Go HTTP servers.
package handlers
