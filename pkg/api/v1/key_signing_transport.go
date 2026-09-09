// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"errors"
	"net"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
)

// localTransport records that this router is served over an IPC transport
// (UNIX socket or Windows named pipe) rather than TCP. Set from the API
// server's own listener configuration, which is the only authoritative
// source: an IPC peer has no host:port RemoteAddr to inspect.
type localTransport bool

// RouterOption configures a v1 router.
type RouterOption func(*routerConfig)

type routerConfig struct {
	localTransport bool
}

// WithLocalTransport declares that the router is served over an IPC
// transport, which only processes on this machine can open.
func WithLocalTransport(local bool) RouterOption {
	return func(c *routerConfig) { c.localTransport = local }
}

func newRouterConfig(opts []RouterOption) routerConfig {
	var c routerConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// requireLocalKeySigning refuses a push that names a cosign private key
// unless the caller is on this machine.
//
// SECURITY: the key path is resolved and read by THIS process, so accepting
// one from an arbitrary caller turns the management API into a signing
// oracle — a request can ask the server to sign and publish an artifact with
// any key the server process can read, and the signature it produces is
// indistinguishable from a legitimate release. The default API mode assigns
// every request a synthetic local identity with no credential check, so on a
// non-loopback bind an unauthenticated remote caller reaches this handler.
//
// A key is a local-workstation and CI credential, so a local-only rule costs
// nothing that was reliably usable anyway: the path has to exist on the
// server's filesystem, which a remote caller cannot arrange. Callers that
// need a remote server to sign should use keyless signing, whose credential
// is a short-lived scoped token rather than a key with no expiry.
//
// Locality is decided by the listener, not by anything in the request: an
// IPC transport is local by construction (and its socket permissions bound
// who can connect), and a TCP peer must be loopback. RemoteAddr is the
// kernel's view of the peer rather than a header, so it cannot be forged by
// the client — but it is only consulted for TCP, where it has that meaning.
// Anything else fails closed.
func requireLocalKeySigning(r *http.Request, local localTransport, key string) error {
	if key == "" {
		return nil
	}
	if bool(local) || callerIsLoopback(r) {
		return nil
	}
	return httperr.WithCode(
		errors.New("key names a cosign private key on the server's filesystem, and this request did"+
			" not come from this machine — accepting it would let a remote caller have the server"+
			" sign with any key it can read. Run the push where the key lives, or sign keylessly"+
			" with identity_token, which carries a short-lived scoped credential instead"),
		http.StatusForbidden,
	)
}

// callerIsLoopback reports whether a TCP request came from this machine.
// A RemoteAddr that is not a host:port pair means the peer did not arrive
// over TCP, which this function cannot judge — it returns false so the
// caller falls back to the listener's own configuration.
func callerIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
