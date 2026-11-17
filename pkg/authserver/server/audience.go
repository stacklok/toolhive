// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

// ErrInvalidTarget is the RFC 8707 error for invalid or unauthorized resource parameters.
// This error is returned when:
// - The resource URI format is invalid (not absolute, has fragment, wrong scheme)
// - The resource is not in the server's allowed audiences list
var ErrInvalidTarget = &fosite.RFC6749Error{
	ErrorField:       "invalid_target",
	DescriptionField: "The requested resource is invalid, unknown, or malformed.",
	CodeField:        http.StatusBadRequest,
}

// ValidateAudienceURI validates that a resource URI conforms to RFC 8707 requirements.
// According to RFC 8707, a valid resource parameter must be:
// - An absolute URI (has scheme and host)
// - No fragment component
// - Use http or https scheme
func ValidateAudienceURI(resource string) error {
	if resource == "" {
		return nil // Empty resource is valid (means no audience binding requested)
	}

	parsed, err := url.Parse(resource)
	if err != nil {
		return ErrInvalidTarget.WithHintf("Resource parameter is not a valid URI: %s", err.Error())
	}

	// Must be absolute (have a scheme)
	if !parsed.IsAbs() {
		return ErrInvalidTarget.WithHint("Resource must be an absolute URI")
	}

	// Must have a host
	if parsed.Host == "" {
		return ErrInvalidTarget.WithHint("Resource must include a host")
	}

	// Must not have a fragment (RFC 8707 Section 2)
	if parsed.Fragment != "" {
		return ErrInvalidTarget.WithHint("Resource must not contain a fragment")
	}

	// Only allow http or https schemes for security
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ErrInvalidTarget.WithHint("Resource must use http or https scheme")
	}

	return nil
}

// ValidateSubjectIdentifierAudiences validates the runtime sub_id disclosure allowlist.
// Every entry must be an exact member of the independently validated
// allowed-audience list; an empty list disables disclosure.
func ValidateSubjectIdentifierAudiences(subjectAudiences, allowedAudiences []string) error {
	if len(subjectAudiences) == 0 {
		return nil
	}
	for _, audience := range subjectAudiences {
		if !slices.Contains(allowedAudiences, audience) {
			return fmt.Errorf("subject identifier audience %q is not in allowed audiences", audience)
		}
	}
	return nil
}

// ValidateAudienceAllowed checks if the resource is in the allowed audiences list.
// Returns nil if allowed, or ErrInvalidTarget if not.
//
// Security: An empty allowedAudiences list means NO audiences are permitted (secure default).
func ValidateAudienceAllowed(resource string, allowedAudiences []string) error {
	if resource == "" {
		return nil // No resource requested, nothing to validate
	}

	// Secure default: empty allowlist means reject all
	if len(allowedAudiences) == 0 {
		return errResourceNotPermitted(resource)
	}

	// Exact string matching
	if slices.Contains(allowedAudiences, resource) {
		return nil
	}

	return errResourceNotPermitted(resource)
}

// GrantSingleResource grants the one RFC 8707 resource a SPIFFE client asked
// for as the token audience. It refuses the "audience" parameter and any
// request without exactly one resource, and requires the resource to match
// permitted exactly (a sub-path of a permitted resource is refused) and to be
// an allowed audience of the server. Every refusal is ErrInvalidTarget with a
// hint naming the cause.
func GrantSingleResource(requester fosite.AccessRequester, permitted, allowedAudiences []string) error {
	form := requester.GetRequestForm()
	resources := form["resource"]
	switch {
	case len(form["audience"]) != 0:
		return ErrInvalidTarget.WithHint("The audience parameter is not accepted for this client; use the resource parameter.")
	case len(resources) == 0 || (len(resources) == 1 && resources[0] == ""):
		return ErrInvalidTarget.WithHint("Exactly one resource parameter is required.")
	case len(resources) > 1:
		return ErrInvalidTarget.WithHint("Exactly one resource parameter is allowed.")
	}
	resource := resources[0]
	if err := ValidateAudienceURI(resource); err != nil {
		return ErrInvalidTarget.WithHint(
			"The resource parameter must be an absolute http or https URI with a host and no fragment.")
	}
	if !slices.Contains(permitted, resource) {
		return ErrInvalidTarget.WithHint(
			"The requested resource is not permitted for this client; it must exactly match a permitted resource.")
	}
	if err := ValidateAudienceAllowed(resource, allowedAudiences); err != nil {
		return ErrInvalidTarget.WithHint("The requested resource is not an allowed audience of this authorization server.")
	}
	requester.GrantAudience(resource)
	return nil
}

// ResourceScopedClient is implemented by clients (SPIFFE clients) whose RFC 8707
// resource allowlist is their only target list; their GetAudience is nil. A
// client that does not implement this interface falls back to GetAudience,
// preserving behavior for clients (e.g. delegate clients) that only ever had
// one list. Token exchange also uses it to select the single-resource rule.
type ResourceScopedClient interface {
	Resources() []string
}

// ClientResources returns the resources client may request: its own resource
// allowlist if it keeps one, otherwise its audiences.
func ClientResources(client fosite.Client) []string {
	if rc, ok := client.(ResourceScopedClient); ok {
		return rc.Resources()
	}
	return client.GetAudience()
}

// ValidateClientAudience checks that resource is one the client itself may
// request. It applies to every client, including DCR and CIMD clients, and
// compares resource to the client's resources with match, which callers take
// from the server's fosite configuration (GetAudienceStrategy). That is the
// strategy the token exchange path uses and the one fosite applies to the
// client's audiences at the authorization code callback and in the refresh
// grant, so the three cannot drift apart if the configured strategy changes.
//
// For a client that keeps no separate resource list, the callback checks the
// same audiences this check does, so a request accepted here does not fail the
// callback's audience check later, and one refused here fails with
// invalid_target before the user logs in upstream. A SPIFFE client is checked
// against its resource list here but has no audiences at the callback; such
// clients have no response types and never reach /authorize, so the two never
// meet today.
//
// With fosite's default path-prefix strategy, scheme and host are compared
// exactly, and the client's path must equal the resource path or be a
// segment-boundary prefix of it. ValidateAudienceAllowed, which runs first,
// still requires the resource to be an exact member of the server's list.
func ValidateClientAudience(resource string, client fosite.Client, match fosite.AudienceMatchingStrategy) error {
	if resource == "" {
		return nil
	}
	if err := match(ClientResources(client), []string{resource}); err != nil {
		return errResourceNotPermitted(resource)
	}
	return nil
}

// ValidateIDJAGClientAudience is ValidateClientAudience for ID-JAG redemption,
// with one exception: a DCR or CIMD client is not narrowed. Its audience is a
// copy of the server's allowed audiences taken at registration, not a
// per-client policy, so checking it adds nothing while the copy is current
// and wrongly refuses a newly added audience once it is stale (DCR rows are
// never refreshed). The resource is still required to be an exact member of
// the server's list.
//
// Every other client is checked, including a client that has no audiences and
// no resource allowlist: such a client may request nothing. Clients with a
// resource allowlist, such as SPIFFE clients, are checked against it.
func ValidateIDJAGClientAudience(resource string, client fosite.Client, match fosite.AudienceMatchingStrategy) error {
	if _, scoped := client.(ResourceScopedClient); !scoped && registration.DCRIssued(client) {
		return nil
	}
	return ValidateClientAudience(resource, client, match)
}

// errResourceNotPermitted is the single refusal both the server-level and the
// client-level audience checks return. /authorize delivers it in the redirect
// to a caller that has not authenticated, so two different hints would tell
// that caller which of the two policies refused a resource, and so whether the
// resource is on the server's list or only outside this client's audiences.
func errResourceNotPermitted(resource string) error {
	return ErrInvalidTarget.WithHintf("The client is not permitted to request resource %q", resource)
}

// ErrAudienceNotPermitted is the refusal for an "audience" request parameter that
// is outside the client's audiences. RFC 8693 §2.2.2 uses invalid_target for a
// refused resource or audience alike. Like errResourceNotPermitted, the hint does
// not say which list refused the audience.
func ErrAudienceNotPermitted(audience string) error {
	return ErrInvalidTarget.WithHintf("The client is not permitted to request audience %q", audience)
}
