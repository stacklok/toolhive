// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	// SPIFFEAuthenticationMethodX509 authenticates a workload with an X.509-SVID.
	SPIFFEAuthenticationMethodX509 SPIFFEAuthenticationMethod = "spiffe_x509"
	// SPIFFEAuthenticationMethodJWT authenticates a workload with a JWT-SVID.
	SPIFFEAuthenticationMethodJWT SPIFFEAuthenticationMethod = "spiffe_jwt"

	// SPIFFEGrantTypeTokenExchange is the only SPIFFE client grant supported by
	// this configuration surface.
	SPIFFEGrantTypeTokenExchange = oauthproto.GrantTypeTokenExchange

	// SPIFFEBundleSourceTypeEndpoint selects a HTTPS SPIFFE Bundle Endpoint.
	SPIFFEBundleSourceTypeEndpoint SPIFFEBundleSourceType = "bundle_endpoint"
	// SPIFFEBundleSourceTypeWorkloadAPI selects the local SPIFFE Workload API.
	SPIFFEBundleSourceTypeWorkloadAPI SPIFFEBundleSourceType = "workload_api"

	// SPIFFEBundleEndpointProfileHTTPSWeb authenticates the bundle endpoint's
	// TLS connection with a Web PKI certificate (the SPIFFE Bundle Endpoint
	// "https_web" profile).
	SPIFFEBundleEndpointProfileHTTPSWeb SPIFFEBundleEndpointProfile = "https_web"
	// SPIFFEBundleEndpointProfileHTTPSSPIFFE authenticates the bundle
	// endpoint's TLS connection with an X.509-SVID trusted by a separately
	// distributed root (the SPIFFE Bundle Endpoint "https_spiffe" profile).
	SPIFFEBundleEndpointProfileHTTPSSPIFFE SPIFFEBundleEndpointProfile = "https_spiffe"
)

// SPIFFEAuthenticationMethod identifies the credential type permitted for a
// SPIFFE workload. Methods are explicit so introducing another credential type
// cannot silently broaden a policy.
type SPIFFEAuthenticationMethod string

// SPIFFEBundleSourceType identifies the selected trust-bundle source.
type SPIFFEBundleSourceType string

// SPIFFEBundleEndpointProfile identifies how a SPIFFE Bundle Endpoint's TLS
// connection is authenticated, per the SPIFFE Federation specification.
type SPIFFEBundleEndpointProfile string

// SPIFFETrustDomainRunConfig declares one SPIFFE trust domain. Credential
// authentication is deliberately outside this configuration step.
type SPIFFETrustDomainRunConfig struct {
	// Name uniquely identifies this declaration and is referenced by
	// InboundGrants.SPIFFEClientAuth entries.
	Name string `json:"name" yaml:"name"`

	// TrustDomain is the SPIFFE trust domain accepted by this declaration.
	TrustDomain string `json:"trust_domain" yaml:"trust_domain"`

	// Methods explicitly enables the supported credential types for this trust
	// domain. No authentication method is enabled when the list is empty.
	Methods []SPIFFEAuthenticationMethod `json:"methods" yaml:"methods"`

	// BundleSource declares exactly one future trust-bundle source. It is
	// validated for shape only; fetching or loading a bundle from it is a
	// later step.
	BundleSource SPIFFEBundleSourceRunConfig `json:"bundle_source" yaml:"bundle_source"`
}

// SPIFFEBundleSourceRunConfig is a discriminated bundle-source declaration.
// Type determines which, and only which, source payload may be set.
type SPIFFEBundleSourceRunConfig struct {
	Type SPIFFEBundleSourceType `json:"type" yaml:"type"`

	Endpoint    *SPIFFEBundleEndpointSourceRunConfig    `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	WorkloadAPI *SPIFFEWorkloadAPIBundleSourceRunConfig `json:"workload_api,omitempty" yaml:"workload_api,omitempty"`
}

// SPIFFEBundleEndpointSourceRunConfig declares a HTTPS SPIFFE Bundle Endpoint.
type SPIFFEBundleEndpointSourceRunConfig struct {
	URL string `json:"url" yaml:"url"`

	// Profile selects how the endpoint's TLS connection is authenticated:
	// SPIFFEBundleEndpointProfileHTTPSWeb (Web PKI) or
	// SPIFFEBundleEndpointProfileHTTPSSPIFFE (a separately distributed
	// X.509-SVID root). Required, since the future bundle loader cannot
	// otherwise know which trust anchor to use for the initial connection.
	Profile SPIFFEBundleEndpointProfile `json:"profile" yaml:"profile"`
}

// SPIFFEWorkloadAPIBundleSourceRunConfig selects the local SPIFFE Workload API.
// It deliberately has no payload; loading and deployment details are deferred
// to the bundle-loading implementation.
type SPIFFEWorkloadAPIBundleSourceRunConfig struct{}

// InboundGrantsRunConfig declares canonical inbound grant configuration for
// separately declared trust roots. SPIFFE client authentication entries live
// here, alongside other inbound grant purposes, so SPIFFE is not a parallel
// trust path.
type InboundGrantsRunConfig struct {
	// SPIFFEClientAuth associates SPIFFE principal patterns with explicit OAuth
	// client identities and permissions. See SPIFFEClientAuthRunConfig.
	SPIFFEClientAuth []SPIFFEClientAuthRunConfig `json:"spiffe_client_auth,omitempty" yaml:"spiffe_client_auth,omitempty"`
}

// SPIFFEClientAuthRunConfig associates one SPIFFE principal pattern from a
// declared trust domain with an explicit OAuth client identity and permissions.
type SPIFFEClientAuthRunConfig struct {
	// TrustDomainRef identifies the SPIFFE trust-domain declaration governing
	// this association policy.
	TrustDomainRef string `json:"trust_domain_ref" yaml:"trust_domain_ref"`

	// PrincipalPattern is a concrete SPIFFE ID or a terminal /* pattern within
	// the declared trust domain.
	PrincipalPattern string `json:"principal_pattern" yaml:"principal_pattern"`

	// ClientID is the explicit OAuth client_id. It is never derived from a
	// SPIFFE ID.
	ClientID string `json:"client_id" yaml:"client_id"`

	Methods []SPIFFEAuthenticationMethod `json:"methods" yaml:"methods"`

	// Resources are RFC 8707 resource indicators this association may
	// request. Must be a subset of the server's allowed_audiences allowlist
	// (RunConfig.AllowedAudiences) — the same RFC 8707 resource-URI list
	// DelegateClientRunConfig.Audiences is validated against. Distinct from
	// Audiences: a resource permission does not imply the same value is also
	// a permitted token audience, or vice versa.
	Resources []string `json:"resources,omitempty" yaml:"resources,omitempty"`

	// Audiences are RFC 8693 token audiences this association may request.
	// This is an independent request dimension from Resources: it is not
	// bounded by allowed_audiences (which is an RFC 8707 resource-URI list)
	// and may contain non-URI logical audience identifiers.
	Audiences []string `json:"audiences" yaml:"audiences"`

	// Scopes are OAuth scopes granted to this association. They must be a
	// subset of the server's effective supported scopes.
	Scopes []string `json:"scopes" yaml:"scopes"`

	// GrantTypes are the OAuth grant types this association may use. Client
	// authentication does not by itself confer any grant.
	GrantTypes []string `json:"grant_types" yaml:"grant_types"`
}

// SPIFFEAuthorizationPolicy is the immutable authorization policy selected by a
// validated SPIFFE association.
type SPIFFEAuthorizationPolicy struct {
	grantTypes []string
	scopes     []string
	resources  []string
	audiences  []string
}

// GrantTypes returns a copy of the permitted OAuth grant types.
func (p SPIFFEAuthorizationPolicy) GrantTypes() []string { return slices.Clone(p.grantTypes) }

// Scopes returns a copy of the permitted OAuth scopes.
func (p SPIFFEAuthorizationPolicy) Scopes() []string { return slices.Clone(p.scopes) }

// Resources returns a copy of the permitted RFC 8707 resource indicators.
func (p SPIFFEAuthorizationPolicy) Resources() []string { return slices.Clone(p.resources) }

// Audiences returns a copy of the permitted RFC 8693 token audiences.
func (p SPIFFEAuthorizationPolicy) Audiences() []string { return slices.Clone(p.audiences) }

// matchSPIFFEPrincipalPattern reports whether principal matches pattern. A
// terminal /* matches descendants only at a path-segment boundary: /agent/*
// matches /agent/one but not /agent or /agent-two.
func matchSPIFFEPrincipalPattern(pattern, principal string) bool {
	normalizedPattern, err := normalizeSPIFFEPrincipal(pattern, true)
	if err != nil {
		return false
	}
	normalizedPrincipal, err := normalizeSPIFFEPrincipal(principal, false)
	if err != nil {
		return false
	}
	if !strings.HasSuffix(normalizedPattern, "/*") {
		return normalizedPattern == normalizedPrincipal
	}
	return strings.HasPrefix(normalizedPrincipal, strings.TrimSuffix(normalizedPattern, "*"))
}

// NewSPIFFETrustConfig validates and normalizes SPIFFE trust declarations into
// an immutable runtime model. It does not load trust bundles or authenticate
// credentials. The zero value SPIFFETrustConfig{} is also valid and denotes no
// SPIFFE trust domains or associations configured — the same value this
// constructor returns for empty input — so callers do not need to avoid
// constructing it directly.
func NewSPIFFETrustConfig(
	trustDomains []SPIFFETrustDomainRunConfig,
	inboundGrants *InboundGrantsRunConfig,
	scopesSupported []string,
	allowedAudiences []string,
) (*SPIFFETrustConfig, error) {
	trustDomainByName, err := validateSPIFFETrust(trustDomains, inboundGrants, scopesSupported, allowedAudiences)
	if err != nil {
		return nil, err
	}
	associations := inboundAssociations(inboundGrants)
	config := &SPIFFETrustConfig{
		associations: make([]SPIFFEClientAuthConfig, 0, len(associations)),
		trustDomains: make(map[string]SPIFFETrustDomain, len(trustDomainByName)),
	}
	for _, domain := range trustDomains {
		validated := trustDomainByName[domain.Name]
		config.trustDomains[domain.Name] = SPIFFETrustDomain{
			trustDomain:  validated.trustDomain.String(),
			methods:      sortedSPIFFEMethods(validated.methods),
			bundleSource: normalizeSPIFFEBundleSource(domain.BundleSource),
		}
	}
	for _, association := range associations {
		principal, err := normalizeSPIFFEPrincipal(association.PrincipalPattern, true)
		if err != nil {
			return nil, err
		}
		config.associations = append(config.associations, SPIFFEClientAuthConfig{
			trustDomainRef: association.TrustDomainRef,
			principal:      principal,
			clientID:       association.ClientID,
			methods:        slices.Clone(association.Methods),
			authorization: SPIFFEAuthorizationPolicy{
				grantTypes: slices.Clone(association.GrantTypes),
				scopes:     slices.Clone(association.Scopes),
				resources:  slices.Clone(association.Resources),
				audiences:  slices.Clone(association.Audiences),
			},
		})
	}
	return config, nil
}

// ValidateSPIFFETrust validates SPIFFE trust declarations without fetching
// bundles or validating credentials.
func ValidateSPIFFETrust(
	trustDomains []SPIFFETrustDomainRunConfig,
	inboundGrants *InboundGrantsRunConfig,
	scopesSupported []string,
	allowedAudiences []string,
) error {
	_, err := validateSPIFFETrust(trustDomains, inboundGrants, scopesSupported, allowedAudiences)
	return err
}

// SPIFFETrustDomain is the immutable normalized form of a declared SPIFFE
// trust domain: its canonical trust domain string, enabled credential
// methods, and bundle-source declaration. Future X.509/JWT-SVID validators
// look this up by declaration name instead of re-parsing the raw RunConfig,
// so the canonical trust domain and enabled methods have exactly one
// authoritative source.
type SPIFFETrustDomain struct {
	trustDomain  string
	methods      []SPIFFEAuthenticationMethod
	bundleSource SPIFFEBundleSourceConfig
}

// TrustDomain returns the canonical SPIFFE trust domain string.
func (d SPIFFETrustDomain) TrustDomain() string { return d.trustDomain }

// Methods returns a copy of the credential methods this trust domain enables.
func (d SPIFFETrustDomain) Methods() []SPIFFEAuthenticationMethod { return slices.Clone(d.methods) }

// BundleSource returns the selected, immutable bundle-source declaration.
func (d SPIFFETrustDomain) BundleSource() SPIFFEBundleSourceConfig { return d.bundleSource }

// SPIFFEBundleSourceConfig is the normalized bundle-source discriminator.
type SPIFFEBundleSourceConfig struct {
	sourceType SPIFFEBundleSourceType
	endpoint   string
	profile    SPIFFEBundleEndpointProfile
}

// Type returns the selected bundle-source type.
func (c SPIFFEBundleSourceConfig) Type() SPIFFEBundleSourceType { return c.sourceType }

// Endpoint returns the configured Bundle Endpoint URL, or an empty string for
// a Workload API source.
func (c SPIFFEBundleSourceConfig) Endpoint() string { return c.endpoint }

// Profile returns the configured Bundle Endpoint authentication profile, or
// an empty string for a Workload API source.
func (c SPIFFEBundleSourceConfig) Profile() SPIFFEBundleEndpointProfile { return c.profile }

func normalizeSPIFFEBundleSource(source SPIFFEBundleSourceRunConfig) SPIFFEBundleSourceConfig {
	if source.Endpoint == nil {
		return SPIFFEBundleSourceConfig{sourceType: source.Type}
	}
	return SPIFFEBundleSourceConfig{
		sourceType: source.Type,
		endpoint:   source.Endpoint.URL,
		profile:    source.Endpoint.Profile,
	}
}

// SPIFFETrustConfig is the immutable normalized SPIFFE trust model used at
// runtime. Its zero value is valid and denotes no SPIFFE trust domains or
// associations configured — external packages may safely construct
// SPIFFETrustConfig{} directly.
type SPIFFETrustConfig struct {
	associations []SPIFFEClientAuthConfig
	trustDomains map[string]SPIFFETrustDomain
}

// TrustDomain returns the normalized trust-domain record declared under name,
// and whether a declaration by that name exists.
func (c *SPIFFETrustConfig) TrustDomain(name string) (SPIFFETrustDomain, bool) {
	if c == nil {
		return SPIFFETrustDomain{}, false
	}
	domain, ok := c.trustDomains[name]
	return domain, ok
}

// Associations returns a defensive copy of the normalized association policies.
func (c *SPIFFETrustConfig) Associations() []SPIFFEClientAuthConfig {
	if c == nil {
		return nil
	}
	associations := make([]SPIFFEClientAuthConfig, len(c.associations))
	for i, association := range c.associations {
		associations[i] = association.clone()
	}
	return associations
}

// SPIFFEClientAuthConfig is an immutable normalized association and policy.
type SPIFFEClientAuthConfig struct {
	trustDomainRef string
	principal      string
	clientID       string
	methods        []SPIFFEAuthenticationMethod
	authorization  SPIFFEAuthorizationPolicy
}

// TrustDomainRef returns the configured trust-domain declaration name.
func (c SPIFFEClientAuthConfig) TrustDomainRef() string { return c.trustDomainRef }

// Principal returns the canonical SPIFFE ID or terminal wildcard policy pattern.
func (c SPIFFEClientAuthConfig) Principal() string { return c.principal }

// ClientID returns the configured OAuth client ID.
func (c SPIFFEClientAuthConfig) ClientID() string { return c.clientID }

// Methods returns a copy of permitted authentication methods.
func (c SPIFFEClientAuthConfig) Methods() []SPIFFEAuthenticationMethod {
	return slices.Clone(c.methods)
}

// AuthorizationPolicy returns a defensive copy of the association policy.
func (c SPIFFEClientAuthConfig) AuthorizationPolicy() SPIFFEAuthorizationPolicy {
	return SPIFFEAuthorizationPolicy{
		grantTypes: slices.Clone(c.authorization.grantTypes),
		scopes:     slices.Clone(c.authorization.scopes),
		resources:  slices.Clone(c.authorization.resources),
		audiences:  slices.Clone(c.authorization.audiences),
	}
}

func (c SPIFFEClientAuthConfig) clone() SPIFFEClientAuthConfig {
	c.methods = slices.Clone(c.methods)
	c.authorization = c.AuthorizationPolicy()
	return c
}

// inboundAssociations returns the configured SPIFFE client-auth entries, or
// nil when inboundGrants is nil.
func inboundAssociations(inboundGrants *InboundGrantsRunConfig) []SPIFFEClientAuthRunConfig {
	if inboundGrants == nil {
		return nil
	}
	return inboundGrants.SPIFFEClientAuth
}

// validateSPIFFETrust validates SPIFFE trust declarations and their inbound
// associations without fetching bundles or validating credentials. It fails
// closed on ambiguity because later authentication must not depend on
// configuration order. It returns the validated trust-domain records so
// NewSPIFFETrustConfig can retain them without re-parsing.
func validateSPIFFETrust(
	trustDomains []SPIFFETrustDomainRunConfig,
	inboundGrants *InboundGrantsRunConfig,
	scopesSupported []string,
	allowedResources []string,
) (map[string]validatedSPIFFETrustDomain, error) {
	trustDomainByName, err := validateSPIFFETrustDomains(trustDomains)
	if err != nil {
		return nil, err
	}
	associations := inboundAssociations(inboundGrants)
	if len(associations) == 0 {
		if len(trustDomains) != 0 {
			return nil, fmt.Errorf("inbound_grants.spiffe_client_auth is required when spiffe_trust_domains is configured")
		}
		return trustDomainByName, nil
	}
	if err := validateSPIFFEClientAuth(
		associations, trustDomainByName, scopesSupported, allowedResources,
	); err != nil {
		return nil, err
	}
	return trustDomainByName, nil
}

// sortedSPIFFEMethods returns the enabled methods in a stable order, since
// methods is stored as a set during validation.
func sortedSPIFFEMethods(methods map[SPIFFEAuthenticationMethod]struct{}) []SPIFFEAuthenticationMethod {
	sorted := make([]SPIFFEAuthenticationMethod, 0, len(methods))
	for _, method := range []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT} {
		if _, ok := methods[method]; ok {
			sorted = append(sorted, method)
		}
	}
	return sorted
}

type validatedSPIFFETrustDomain struct {
	trustDomain spiffeid.TrustDomain
	methods     map[SPIFFEAuthenticationMethod]struct{}
}

func validateSPIFFETrustDomains(domains []SPIFFETrustDomainRunConfig) (map[string]validatedSPIFFETrustDomain, error) {
	byName := make(map[string]validatedSPIFFETrustDomain, len(domains))
	byTrustDomain := make(map[string]string, len(domains))
	for i, domain := range domains {
		if domain.Name == "" {
			return nil, fmt.Errorf("spiffe_trust_domains[%d]: name is required", i)
		}
		if _, exists := byName[domain.Name]; exists {
			return nil, fmt.Errorf("spiffe_trust_domains[%d]: duplicate name %q", i, domain.Name)
		}
		trustDomain, err := parseTrustDomain(domain.TrustDomain)
		if err != nil {
			return nil, fmt.Errorf("spiffe_trust_domains[%d]: %w", i, err)
		}
		canonicalTrustDomain := trustDomain.String()
		if existingName, exists := byTrustDomain[canonicalTrustDomain]; exists {
			return nil, fmt.Errorf(
				"spiffe_trust_domains[%d]: duplicate trust domain %q already declared as %q",
				i, canonicalTrustDomain, existingName,
			)
		}
		methods, err := validateMethods(domain.Methods, fmt.Sprintf("spiffe_trust_domains[%d].methods", i))
		if err != nil {
			return nil, err
		}
		if err := validateSPIFFEBundleSource(domain.BundleSource, i); err != nil {
			return nil, err
		}
		byName[domain.Name] = validatedSPIFFETrustDomain{trustDomain: trustDomain, methods: methods}
		byTrustDomain[canonicalTrustDomain] = domain.Name
	}
	return byName, nil
}

func validateSPIFFEBundleSource(source SPIFFEBundleSourceRunConfig, index int) error {
	switch source.Type {
	case SPIFFEBundleSourceTypeEndpoint:
		if source.Endpoint == nil || source.WorkloadAPI != nil {
			return fmt.Errorf("spiffe_trust_domains[%d].bundle_source: type %q requires endpoint only", index, source.Type)
		}
		return validateSPIFFEBundleEndpoint(*source.Endpoint, index)
	case SPIFFEBundleSourceTypeWorkloadAPI:
		if source.WorkloadAPI == nil || source.Endpoint != nil {
			return fmt.Errorf("spiffe_trust_domains[%d].bundle_source: type %q requires workload_api only", index, source.Type)
		}
		return nil
	default:
		return fmt.Errorf("spiffe_trust_domains[%d].bundle_source.type: unknown source type %q", index, source.Type)
	}
}

func validateSPIFFEBundleEndpoint(endpoint SPIFFEBundleEndpointSourceRunConfig, index int) error {
	endpointURL := endpoint.URL
	u, err := url.ParseRequestURI(endpointURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf(
			"spiffe_trust_domains[%d].bundle_source.endpoint.url must be an absolute HTTPS URL with a valid authority",
			index,
		)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.Contains(endpointURL, "?") || strings.Contains(endpointURL, "#") ||
		net.ParseIP(u.Hostname()) != nil || networking.IsLoopbackHost(u.Hostname()) {
		return fmt.Errorf(
			"spiffe_trust_domains[%d].bundle_source.endpoint.url must not contain credentials, query, "+
				"fragment, an IP-literal host, or a loopback host",
			index,
		)
	}
	switch endpoint.Profile {
	case SPIFFEBundleEndpointProfileHTTPSWeb, SPIFFEBundleEndpointProfileHTTPSSPIFFE:
		return nil
	default:
		return fmt.Errorf(
			"spiffe_trust_domains[%d].bundle_source.endpoint.profile must be %q or %q",
			index, SPIFFEBundleEndpointProfileHTTPSWeb, SPIFFEBundleEndpointProfileHTTPSSPIFFE,
		)
	}
}

func validateSPIFFEClientAuth(
	entries []SPIFFEClientAuthRunConfig,
	trustDomainByName map[string]validatedSPIFFETrustDomain,
	scopesSupported []string,
	allowedResources []string,
) error {
	effectiveScopes := func() []string {
		if len(scopesSupported) == 0 {
			return registration.DefaultScopes
		}
		return scopesSupported
	}()
	seenPrincipals := make([]string, 0, len(entries))
	clientIDs := make(map[string]struct{}, len(entries))
	referencedTrustDomains := make(map[string]struct{}, len(entries))
	for i, entry := range entries {
		trustDomain, ok := trustDomainByName[entry.TrustDomainRef]
		if entry.TrustDomainRef == "" {
			return fmt.Errorf("inbound_grants.spiffe_client_auth[%d]: trust_domain_ref is required", i)
		}
		if !ok {
			return fmt.Errorf("inbound_grants.spiffe_client_auth[%d]: unknown trust_domain_ref %q", i, entry.TrustDomainRef)
		}
		principal, err := validateSPIFFEClientAssociation(entry, i, trustDomain, effectiveScopes, allowedResources)
		if err != nil {
			return err
		}
		if _, exists := clientIDs[entry.ClientID]; exists {
			return fmt.Errorf("inbound_grants.spiffe_client_auth[%d]: duplicate client_id %q", i, entry.ClientID)
		}
		if overlapsSPIFFEPatterns(seenPrincipals, principal) {
			return fmt.Errorf(
				"inbound_grants.spiffe_client_auth[%d]: "+
					"principal_pattern %q overlaps an existing policy",
				i,
				entry.PrincipalPattern,
			)
		}
		seenPrincipals = append(seenPrincipals, principal)
		clientIDs[entry.ClientID] = struct{}{}
		referencedTrustDomains[entry.TrustDomainRef] = struct{}{}
	}
	for name := range trustDomainByName {
		if _, ok := referencedTrustDomains[name]; !ok {
			return fmt.Errorf("spiffe_trust_domains: trust domain %q is not referenced by spiffe_client_auth", name)
		}
	}
	return nil
}

func validateSPIFFEClientAssociation(
	entry SPIFFEClientAuthRunConfig,
	index int,
	trustDomain validatedSPIFFETrustDomain,
	effectiveScopes []string,
	allowedResources []string,
) (string, error) {
	principal, err := normalizeSPIFFEPrincipal(entry.PrincipalPattern, true)
	if err != nil {
		return "", fmt.Errorf("inbound_grants.spiffe_client_auth[%d].principal_pattern: %w", index, err)
	}
	principalID, err := parseSPIFFEID(strings.TrimSuffix(principal, "/*"))
	if err != nil {
		return "", fmt.Errorf("inbound_grants.spiffe_client_auth[%d].principal_pattern: %w", index, err)
	}
	if !principalID.MemberOf(trustDomain.trustDomain) {
		return "", fmt.Errorf(
			"inbound_grants.spiffe_client_auth[%d]: principal trust domain does not match %q",
			index,
			trustDomain.trustDomain,
		)
	}
	if entry.ClientID == "" {
		return "", fmt.Errorf("inbound_grants.spiffe_client_auth[%d]: client_id is required", index)
	}
	if err := storage.ValidateRegisterableClientID(entry.ClientID); err != nil {
		return "", fmt.Errorf("inbound_grants.spiffe_client_auth[%d]: client_id: %w", index, err)
	}
	if oauthproto.IsClientIDMetadataDocumentURL(entry.ClientID) {
		return "", fmt.Errorf(
			"inbound_grants.spiffe_client_auth[%d]: client_id must not be a client metadata document URL "+
				"(reserved for CIMD-resolved clients): %q",
			index, entry.ClientID,
		)
	}
	fieldPrefix := fmt.Sprintf("inbound_grants.spiffe_client_auth[%d]", index)
	methods, err := validateMethods(entry.Methods, fieldPrefix+".methods")
	if err != nil {
		return "", err
	}
	for method := range methods {
		if _, enabled := trustDomain.methods[method]; !enabled {
			return "", fmt.Errorf("%s.methods: method %q is not enabled by trust domain %q", fieldPrefix, method, entry.TrustDomainRef)
		}
	}
	if err := validateSPIFFEClientAssociationPermissions(entry, index, fieldPrefix, effectiveScopes, allowedResources); err != nil {
		return "", err
	}
	return principal, nil
}

// validateSPIFFEClientAssociationPermissions validates the resources,
// audiences, scopes, and grant types an association is permitted to request.
// Resources (RFC 8707) and audiences (RFC 8693) are independent request
// dimensions: permission in one must never imply permission in the other, so
// only resources are bounded by the server's allowed_audiences allowlist.
func validateSPIFFEClientAssociationPermissions(
	entry SPIFFEClientAuthRunConfig,
	index int,
	fieldPrefix string,
	effectiveScopes []string,
	allowedResources []string,
) error {
	if err := validateSPIFFEResources(entry.Resources, fieldPrefix+".resources", allowedResources); err != nil {
		return err
	}
	if err := validateDistinctNonEmpty(entry.Audiences, fieldPrefix+".audiences"); err != nil {
		return err
	}
	if err := validateDistinctNonEmpty(entry.Scopes, fieldPrefix+".scopes"); err != nil {
		return err
	}
	if err := registration.ValidateScopeSubset(entry.Scopes, effectiveScopes, fieldPrefix+".scopes"); err != nil {
		return err
	}
	return validateSPIFFEGrants(entry.GrantTypes, index)
}

func parseTrustDomain(trustDomain string) (spiffeid.TrustDomain, error) {
	if trustDomain == "" {
		return spiffeid.TrustDomain{}, fmt.Errorf("trust_domain is required")
	}
	parsed, err := spiffeid.TrustDomainFromString(trustDomain)
	if err != nil {
		return spiffeid.TrustDomain{}, fmt.Errorf("must be a valid SPIFFE trust domain: %w", err)
	}
	return parsed, nil
}

func normalizeSPIFFEPrincipal(principal string, allowPattern bool) (string, error) {
	if principal == "" {
		return "", fmt.Errorf("is required")
	}
	wildcard := strings.HasSuffix(principal, "/*")
	if strings.Contains(principal, "*") && (!allowPattern || !wildcard || strings.Count(principal, "*") != 1) {
		return "", fmt.Errorf("must use only a terminal /* wildcard")
	}
	base := strings.TrimSuffix(principal, "/*")
	id, err := parseSPIFFEID(base)
	if err != nil {
		return "", err
	}
	if id.Path() == "" && !wildcard {
		return "", fmt.Errorf("must include a path or terminal /* wildcard")
	}
	if wildcard {
		return id.String() + "/*", nil
	}
	return id.String(), nil
}

func parseSPIFFEID(principal string) (spiffeid.ID, error) {
	id, err := spiffeid.FromString(principal)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("must be a SPIFFE ID: %w", err)
	}
	return id, nil
}

func validateMethods(methods []SPIFFEAuthenticationMethod, field string) (map[SPIFFEAuthenticationMethod]struct{}, error) {
	if len(methods) == 0 {
		return nil, fmt.Errorf("%s is required", field)
	}
	seen := make(map[SPIFFEAuthenticationMethod]struct{}, len(methods))
	for _, method := range methods {
		if method != SPIFFEAuthenticationMethodX509 && method != SPIFFEAuthenticationMethodJWT {
			return nil, fmt.Errorf("%s: unknown method %q", field, method)
		}
		if _, exists := seen[method]; exists {
			return nil, fmt.Errorf("%s: duplicate method %q", field, method)
		}
		seen[method] = struct{}{}
	}
	return seen, nil
}

// validateSPIFFEResources validates the RFC 8707 resource indicators an
// association may request: each must be a syntactically valid absolute
// HTTP(S) URI and a member of the server's allowed_audiences allowlist, the
// same RFC 8707 resource-URI list DelegateClientRunConfig.Audiences is
// validated against (see validateDelegateClients in config.go).
func validateSPIFFEResources(values []string, field string, allowedResources []string) error {
	if len(values) == 0 {
		return nil
	}
	if err := validateResourceIndicators(values, field); err != nil {
		return err
	}
	for _, value := range values {
		if !slices.Contains(allowedResources, value) {
			return fmt.Errorf("%s: resource %q is not allowed by allowed_audiences", field, value)
		}
	}
	return nil
}

func validateResourceIndicators(values []string, field string) error {
	if len(values) == 0 {
		return nil
	}
	if err := validateDistinctNonEmpty(values, field); err != nil {
		return err
	}
	for _, value := range values {
		u, err := url.ParseRequestURI(value)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
			u.Fragment != "" || strings.Contains(value, "#") {
			return fmt.Errorf("%s: resource indicator %q must be an absolute HTTP(S) URI without a fragment", field, value)
		}
	}
	return nil
}

func validateDistinctNonEmpty(values []string, field string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s is required", field)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("%s must not contain an empty value", field)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s: duplicate value %q", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateSPIFFEGrants(grants []string, index int) error {
	if len(grants) != 1 || grants[0] != SPIFFEGrantTypeTokenExchange {
		return fmt.Errorf(
			"inbound_grants.spiffe_client_auth[%d].grant_types must be exactly [%q]",
			index, SPIFFEGrantTypeTokenExchange,
		)
	}
	return nil
}

func overlapsSPIFFEPatterns(patterns []string, principal string) bool {
	for _, pattern := range patterns {
		if spiffePatternsOverlap(pattern, principal) {
			return true
		}
	}
	return false
}

func spiffePatternsOverlap(first, second string) bool {
	firstWildcard := strings.HasSuffix(first, "/*")
	secondWildcard := strings.HasSuffix(second, "/*")
	if !firstWildcard && !secondWildcard {
		return first == second
	}
	if firstWildcard && secondWildcard {
		return strings.HasPrefix(strings.TrimSuffix(first, "*"), strings.TrimSuffix(second, "*")) ||
			strings.HasPrefix(strings.TrimSuffix(second, "*"), strings.TrimSuffix(first, "*"))
	}
	if firstWildcard {
		return matchSPIFFEPrincipalPattern(first, second)
	}
	return matchSPIFFEPrincipalPattern(second, first)
}
