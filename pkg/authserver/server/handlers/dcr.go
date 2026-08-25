// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// MaxDCRBodySize is the maximum allowed size for DCR request bodies (64KB).
// This prevents DoS attacks via extremely large payloads while being generous
// enough for legitimate requests with multiple redirect URIs.
//
// It is exported to serve as the single source of truth for the auth-server
// body-size cap: the embedded auth server (pkg/authserver/runner) derives its
// own request-body limit from this constant so the two cannot drift.
const MaxDCRBodySize = 64 * 1024

// RegisterClientHandler handles POST /oauth/register requests.
// It implements RFC 7591 Dynamic Client Registration for public clients with
// loopback redirect URIs, and additionally for confidential clients with
// https non-loopback redirect URIs when AllowConfidentialClientRegistration
// is set.
func (h *Handler) RegisterClientHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Limit request body size to prevent DoS attacks
	req.Body = http.MaxBytesReader(w, req.Body, MaxDCRBodySize)

	// Validate Content-Type header (RFC 7591 requires application/json)
	contentType := req.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		writeDCRError(w, http.StatusBadRequest, &registration.DCRError{
			Error:            registration.DCRErrorInvalidClientMetadata,
			ErrorDescription: "Content-Type must be application/json",
		})
		return
	}

	// Parse request body. oauthproto.ScopeList.UnmarshalJSON handles both
	// RFC 7591 wire formats for "scope" (space-delimited string or JSON
	// array) so we accept either shape transparently here.
	var dcrReq oauthproto.DynamicClientRegistrationRequest
	if err := json.NewDecoder(req.Body).Decode(&dcrReq); err != nil {
		writeDCRError(w, http.StatusBadRequest, &registration.DCRError{
			Error:            registration.DCRErrorInvalidClientMetadata,
			ErrorDescription: "invalid JSON request body",
		})
		return
	}

	// Validate request. h.config.AllowConfidentialClientRegistration gates whether
	// client_secret_basic / client_secret_post registrations are accepted.
	validated, dcrErr := registration.ValidateDCRRequest(&dcrReq, h.config.AllowConfidentialClientRegistration)
	if dcrErr != nil {
		writeDCRError(w, http.StatusBadRequest, dcrErr)
		return
	}

	// Validate requested scopes against server's supported scopes
	scopes, dcrErr := registration.ValidateScopes(dcrReq.Scopes, h.config.ScopesSupported)
	if dcrErr != nil {
		writeDCRError(w, http.StatusBadRequest, dcrErr)
		return
	}

	// Union with the operator-configured scope baseline. RFC 7591 §3.2.1 permits
	// the AS to replace requested client metadata values during registration; we
	// use that to expand the registered scope set so a client whose DCR request
	// narrowed the scope field can still request the baseline at /oauth/authorize.
	// h.config.BaselineClientScopes is validated at startup to be a subset of
	// ScopesSupported, so the union is guaranteed to be a subset of advertised
	// scopes. Operators should keep the baseline narrow (e.g. openid,
	// offline_access) — every DCR-registered client gains the ability to request
	// these scopes at /oauth/authorize regardless of what they registered with.
	if len(h.config.BaselineClientScopes) > 0 {
		effective := registration.UnionScopes(scopes, h.config.BaselineClientScopes)
		if !slices.Equal(effective, scopes) {
			// Baseline-driven expansion is the intended behavior whenever
			// baseline_client_scopes is configured, so per-registration
			// audit lives at Debug rather than Warn. Operator-visible
			// signal that the baseline is in effect comes from a one-time
			// Info log at server startup (NewAuthorizationServerConfig).
			slog.Debug("DCR registered scope set expanded by baseline_client_scopes",
				"client_name", validated.ClientName,
				"requested", scopes,
				"effective", effective,
				"baseline", h.config.BaselineClientScopes,
			)
			scopes = effective
		}
	}

	// Generate client ID
	clientID := uuid.NewString()

	// A request that declared itself public but whose redirect_uris exactly
	// matches an operator-configured force-confidential entry is registered
	// as confidential anyway (RFC 7591 §3.2.1 permits the server to
	// substitute client metadata). See Config.ForceConfidentialRedirectURIs
	// for the rationale: some MCP clients declare "none" and then refuse to
	// proceed without a client_secret.
	forcedURI, effectiveAuthMethod, dcrErr := resolveForceConfidentialOverride(validated, h.config.ForceConfidentialRedirectURIs)
	if dcrErr != nil {
		writeDCRError(w, http.StatusBadRequest, dcrErr)
		return
	}
	if forcedURI != "" {
		slog.Warn("DCR: forcing confidential client registration for redirect_uri "+
			"that requested token_endpoint_auth_method 'none'",
			"client_id", clientID,
			"redirect_uri", forcedURI,
		)
	}

	fositeClient, clientSecret, err := buildDCRClient(
		clientID, forcedURI != "", effectiveAuthMethod, validated, scopes, h.config.AllowedAudiences)
	if err != nil {
		slog.Error("failed to create client", "error", err)
		writeDCRError(w, http.StatusInternalServerError, &registration.DCRError{
			Error:            "server_error",
			ErrorDescription: "failed to create client",
		})
		return
	}

	// Register client
	if err := h.storage.RegisterClient(ctx, fositeClient); err != nil {
		if errors.Is(err, storage.ErrClientCapacity) {
			slog.Debug("DCR client registration capacity reached", "error", err)
			// Capacity frees up once the oldest DCR-issued entry crosses
			// storage.DefaultMinClientAge, so that's the honest worst-case
			// wait. This uses the default rather than the configured
			// minClientAge because the value isn't currently threaded from
			// storage to this handler; wire it through if that gap matters.
			w.Header().Set("Retry-After", strconv.Itoa(int(storage.DefaultMinClientAge.Seconds())))
			writeDCRError(w, http.StatusServiceUnavailable, &registration.DCRError{
				Error:            "server_error",
				ErrorDescription: "client registration capacity reached; try again later",
			})
			return
		}
		slog.Error("failed to register client", "error", err)
		writeDCRError(w, http.StatusInternalServerError, &registration.DCRError{
			Error:            "server_error",
			ErrorDescription: "failed to register client",
		})
		return
	}

	// Successful DCR registration is a normal operational event, not a
	// long-running operation, so it logs at Debug to stay silent at INFO+.
	// client_id, software_id, token_endpoint_auth_method, and scopes are
	// public client metadata per RFC 7591 and not credentials. If audit
	// signal is desired in future, the right home is a dedicated audit-
	// log emission path rather than promoting this record to INFO.
	//
	// Note: the "issuer" attribute here identifies THIS server (the
	// ToolHive-embedded AS that is performing the registration), not the
	// upstream AS being registered against. That distinction is important
	// when correlating these logs with the resolver's logs in
	// pkg/auth/dcr/resolver.go, which use "issuer" to mean the
	// upstream AS. The two uses live at opposite ends of the DCR flow.
	// No "upstream" attribute is emitted because the /oauth/register
	// endpoint has no upstream concept.
	logAttrs := []any{
		"client_id", clientID,
		"software_id", validated.SoftwareID,
		"token_endpoint_auth_method", effectiveAuthMethod,
		"scopes", scopes,
	}
	if issuer := h.issuer(); issuer != "" {
		logAttrs = append(logAttrs, "issuer", issuer)
	}
	//nolint:gosec // G706: client_id is public metadata per RFC 7591.
	slog.Debug("registered new DCR client", logAttrs...)

	// Build response per RFC 7591 Section 3.2.1.
	// Scopes reflects the scopes actually granted to this client: the
	// client-supplied scope set was validated against ScopesSupported by
	// ValidateScopes above, then (if configured) unioned with
	// BaselineClientScopes — which is itself guaranteed by startup-time
	// validation to be a subset of ScopesSupported. The unioned set is NOT
	// re-validated here. ScopeList.MarshalJSON emits the RFC 7591 §2
	// space-delimited wire form on the way out.
	issuedAt := time.Now().Unix()
	response := oauthproto.DynamicClientRegistrationResponse{
		ClientID:                clientID,
		ClientIDIssuedAt:        issuedAt,
		RedirectURIs:            validated.RedirectURIs,
		ClientName:              validated.ClientName,
		TokenEndpointAuthMethod: effectiveAuthMethod,
		GrantTypes:              validated.GrantTypes,
		ResponseTypes:           validated.ResponseTypes,
		Scopes:                  oauthproto.ScopeList(scopes),
	}
	if clientSecret != "" {
		// client_secret_expires_at is 0 ("does not expire", RFC 7591 §2): the
		// storage TTL on a DCR-issued client is refreshed by RenewClientTTL on
		// every token exchange, so an actively used registration never expires
		// and advertising issued_at+TTL would be false. Advertising a real
		// expiry would also make ToolHive's own DCR client (which acts on this
		// field, pkg/auth/dcr/resolver.go) re-register against a ToolHive auth
		// server every TTL while its existing registration is still live,
		// orphaning the old row — the registration bloat the TTL exists to
		// prevent. An idle registration is still evicted after
		// DefaultDCRClientTTL, but re-registration mints a new client_id either
		// way, so the field cannot usefully describe that case.
		response.ClientSecret = clientSecret
		response.ClientSecretExpiresAt = new(int64) // 0: does not expire
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode DCR response", "error", err)
	}
}

// resolveForceConfidentialOverride checks whether validated's redirect_uris
// match an operator-configured force-confidential entry and the request
// declared (or defaulted to) "none". It returns the matched URI ("" if no
// override applies) and the auth method that should actually be used to
// build and report the client.
//
// The override is reported as client_secret_post, never client_secret_basic:
// the Python MCP SDK these clients are typically built on constrains
// token_endpoint_auth_method to ["none", "client_secret_post"], and
// client_secret_basic is rejected there.
//
// A matched override upgrades the WHOLE registration to confidential, so the
// entire redirect_uris list — not just the matched entry — must pass the
// same https non-loopback policy the ordinary confidential path enforces
// (validateAuthMethod). Without this, a request mixing a matched https URI
// with an unrelated loopback URI would mint a secret for a client that also
// has a loopback callback, which the ordinary path already rejects. The
// registration is rejected rather than silently downgraded to public: a
// silent downgrade would hand back a public registration with no secret,
// reproducing the exact confusing failure this override exists to fix.
func resolveForceConfidentialOverride(
	validated *oauthproto.DynamicClientRegistrationRequest, forceConfidentialURIs []string,
) (forcedURI, effectiveAuthMethod string, dcrErr *registration.DCRError) {
	effectiveAuthMethod = validated.TokenEndpointAuthMethod
	if effectiveAuthMethod != oauthproto.TokenEndpointAuthMethodNone {
		return "", effectiveAuthMethod, nil
	}
	forcedURI = matchForceConfidentialRedirectURI(validated.RedirectURIs, forceConfidentialURIs)
	if forcedURI == "" {
		return "", effectiveAuthMethod, nil
	}
	effectiveAuthMethod = oauthproto.TokenEndpointAuthMethodClientSecretPost
	if dcrErr := registration.ValidateConfidentialRedirectURIs(validated.RedirectURIs, effectiveAuthMethod); dcrErr != nil {
		return "", "", dcrErr
	}
	return forcedURI, effectiveAuthMethod, nil
}

// buildDCRClient mints a client_secret when effectiveAuthMethod requires one
// and constructs the fosite client for a DCR registration. forced selects the
// plain *fosite.DefaultClient shape (registration.NewConfidentialPlain) used
// for a force-confidential override, rather than the OIDC shape
// registration.New produces for an ordinary confidential registration: fosite
// only enforces token_endpoint_auth_method on clients implementing
// fosite.OpenIDConnectClient, and an overridden client's credential
// presentation (HTTP Basic vs. form body) is unknown in advance, so the
// plain shape — which accepts either — is required there.
//
// Any client_secret field on the incoming request is ignored — secrets are
// always server-generated. The plaintext is returned to the caller exactly
// once; only its SHA-256 hash is stored.
func buildDCRClient(
	clientID string,
	forced bool,
	effectiveAuthMethod string,
	validated *oauthproto.DynamicClientRegistrationRequest,
	scopes, allowedAudiences []string,
) (fositeClient fosite.Client, clientSecret string, err error) {
	if effectiveAuthMethod != oauthproto.TokenEndpointAuthMethodNone {
		clientSecret, err = registration.GenerateClientSecret()
		if err != nil {
			return nil, "", fmt.Errorf("failed to generate client secret: %w", err)
		}
	}

	if forced {
		fositeClient, err = registration.NewConfidentialPlain(registration.Config{
			ID:            clientID,
			Secret:        clientSecret,
			RedirectURIs:  validated.RedirectURIs,
			GrantTypes:    validated.GrantTypes,
			ResponseTypes: validated.ResponseTypes,
			Scopes:        scopes,
			Audience:      allowedAudiences,
		})
	} else {
		fositeClient, err = registration.New(registration.Config{
			ID:                      clientID,
			Secret:                  clientSecret,
			RedirectURIs:            validated.RedirectURIs,
			TokenEndpointAuthMethod: validated.TokenEndpointAuthMethod,
			GrantTypes:              validated.GrantTypes,
			ResponseTypes:           validated.ResponseTypes,
			Scopes:                  scopes,
			Audience:                allowedAudiences,
		})
	}
	if err != nil {
		return nil, "", err
	}
	return fositeClient, clientSecret, nil
}

// matchForceConfidentialRedirectURI returns the first entry in redirectURIs
// that is an exact match for one of the operator-configured
// forceConfidentialURIs, or "" if none match. Matching is deliberately exact
// (not prefix or scheme-relaxed): an attacker who registers with someone
// else's callback URI is issued a secret for a client whose authorization
// codes are delivered to that someone else's redirect endpoint, so exact
// matching does not hand out a usable credential for another client.
func matchForceConfidentialRedirectURI(redirectURIs, forceConfidentialURIs []string) string {
	for _, uri := range redirectURIs {
		if slices.Contains(forceConfidentialURIs, uri) {
			return uri
		}
	}
	return ""
}

// writeDCRError writes a DCR error response per RFC 7591 Section 3.2.2.
func writeDCRError(w http.ResponseWriter, statusCode int, dcrErr *registration.DCRError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	// Encoding errors are not recoverable (headers already written), log for diagnostics
	if err := json.NewEncoder(w).Encode(dcrErr); err != nil {
		slog.Debug("failed to encode DCR error response", "error", err)
	}
}
