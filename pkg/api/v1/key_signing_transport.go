// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/server/discovery"
)

// RouterOption configures a v1 router.
type RouterOption func(*routerConfig)

type routerConfig struct {
	keySigningCapability string
}

// WithKeySigningCapability sets the secret capability that authorizes a push
// to name a private key on the API server's filesystem.
func WithKeySigningCapability(capability string) RouterOption {
	return func(c *routerConfig) { c.keySigningCapability = capability }
}

func newRouterConfig(opts []RouterOption) routerConfig {
	var c routerConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// requireKeySigningCapability refuses a push that names a cosign private key
// unless the caller proves it read the owner-protected server discovery file.
//
// SECURITY: the key path is resolved and read by THIS process, so accepting
// one from an arbitrary caller turns the management API into a signing
// oracle — a request can ask the server to sign and publish an artifact with
// any key the server process can read, and the signature it produces is
// indistinguishable from a legitimate release. The default API mode assigns
// every request a synthetic local identity with no credential check, so on a
// non-loopback bind an unauthenticated remote caller reaches this handler.
//
// Listener locality is insufficient authorization: a public reverse proxy can
// forward an untrusted request over loopback or an IPC listener, making its
// backend peer appear local. Instead, the standard CLI obtains an independent
// random capability from the owner-only discovery file. The nonce cannot be
// reused because /health intentionally returns it to every caller.
func requireKeySigningCapability(r *http.Request, expectedCapability, key string) error {
	if key == "" {
		return nil
	}
	suppliedCapability := r.Header.Get(discovery.KeySigningCapabilityHeader)
	if expectedCapability != "" && subtle.ConstantTimeCompare(
		[]byte(suppliedCapability), []byte(expectedCapability),
	) == 1 {
		return nil
	}
	return httperr.WithCode(
		errors.New("key names a cosign private key on the server's filesystem, but this request"+
			" does not have the protected local discovery capability — accepting it would let an"+
			" untrusted caller have the server sign with any key it can read. Use the locally"+
			" discovered ToolHive server, or sign keylessly with identity_token, which carries a"+
			" short-lived scoped credential instead"),
		http.StatusForbidden,
	)
}
