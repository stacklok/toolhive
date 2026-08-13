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
)

// SPIFFEAuthenticationMethod identifies the credential type permitted for a
// SPIFFE workload. Methods are explicit so introducing another credential type
// cannot silently broaden a policy.
type SPIFFEAuthenticationMethod string

// SPIFFEBundleSourceType identifies the selected trust-bundle source.
type SPIFFEBundleSourceType string

const (
	// SPIFFEBundleSourceTypeEndpoint selects a HTTPS SPIFFE Bundle Endpoint.
	SPIFFEBundleSourceTypeEndpoint SPIFFEBundleSourceType = "bundle_endpoint"
	// SPIFFEBundleSourceTypeWorkloadAPI selects the local SPIFFE Workload API.
	SPIFFEBundleSourceTypeWorkloadAPI SPIFFEBundleSourceType = "workload_api"
)

// SPIFFETrustDomainRunConfig declares one SPIFFE trust domain. Credential and
// bundle loading are deliberately outside this configuration step.
type SPIFFETrustDomainRunConfig struct {
	// Name uniquely identifies this declaration and is referenced by
	// InboundGrants.SPIFFEClientAuth entries.
	Name string `json:"name" yaml:"name"`

	// TrustDomain is the SPIFFE trust domain accepted by this declaration.
	TrustDomain string `json:"trust_domain" yaml:"trust_domain"`

	// Methods explicitly enables the supported credential types for this trust
	// domain. No authentication method is enabled when the list is empty.
	Methods []SPIFFEAuthenticationMethod `json:"methods" yaml:"methods"`

	// BundleSource declares exactly one future bundle source. It is not fetched
	// or validated as a bundle by this step.
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
}

// SPIFFEWorkloadAPIBundleSourceRunConfig selects the local SPIFFE Workload API.
// It deliberately has no endpoint payload; loading and deployment details are
// deferred to the bundle-loading implementation.
type SPIFFEWorkloadAPIBundleSourceRunConfig struct{}

// InboundGrantsRunConfig enables inbound grants for separately declared trust
// roots. Its SPIFFE entries are kept alongside other inbound grant purposes so
// SPIFFE is not a parallel trust path.
type InboundGrantsRunConfig struct {
	SPIFFEClientAuth []SPIFFEClientAuthRunConfig `json:"spiffe_client_auth,omitempty" yaml:"spiffe_client_auth,omitempty"`
}

// InboundGrantsConfig is the runtime form of InboundGrantsRunConfig.
type InboundGrantsConfig = InboundGrantsRunConfig

// SPIFFEClientAuthRunConfig associates one SPIFFE principal pattern from a
// declared trust domain with an explicit OAuth client identity and permissions.
type SPIFFEClientAuthRunConfig struct {
	// TrustDomainRef identifies the SPIFFE trust-domain declaration governing
	// this association policy.
	TrustDomainRef string `json:"trust_domain_ref" yaml:"trust_domain_ref"`

	// Principal is a concrete SPIFFE ID or a terminal /* pattern within the
	// declared trust domain.
	Principal string `json:"principal" yaml:"principal"`

	// ClientID is the explicit OAuth client_id. It is never derived from a
	// SPIFFE ID.
	ClientID string `json:"client_id" yaml:"client_id"`

	Methods []SPIFFEAuthenticationMethod `json:"methods" yaml:"methods"`

	// Resources are RFC 8707 resource indicators and remain distinct from
	// token audiences.
	Resources []string `json:"resources" yaml:"resources"`

	// Audiences are token audiences and are not inferred from Resources.
	Audiences []string `json:"audiences" yaml:"audiences"`

	// Scopes are OAuth scopes granted to this association. They must be a
	// subset of the server's effective supported scopes.
	Scopes []string `json:"scopes" yaml:"scopes"`

	GrantTypes    []string                      `json:"grant_types" yaml:"grant_types"`
	TokenExchange *SPIFFETokenExchangeRunConfig `json:"token_exchange,omitempty" yaml:"token_exchange,omitempty"`
}

// SPIFFETokenExchangeRunConfig enables token exchange for a single policy.
// Its presence must agree with GrantTypes; it does not perform an exchange.
type SPIFFETokenExchangeRunConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// SPIFFEAuthorizationPolicy is the immutable authorization policy selected by a
// validated SPIFFE association. Resources and audiences remain separate.
type SPIFFEAuthorizationPolicy struct {
	grantTypes    []string
	scopes        []string
	resources     []string
	audiences     []string
	tokenExchange bool
}

// GrantTypes returns a copy of the permitted OAuth grant types.
func (p SPIFFEAuthorizationPolicy) GrantTypes() []string { return slices.Clone(p.grantTypes) }

// Scopes returns a copy of the permitted OAuth scopes.
func (p SPIFFEAuthorizationPolicy) Scopes() []string { return slices.Clone(p.scopes) }

// Resources returns a copy of the permitted RFC 8707 resource indicators.
func (p SPIFFEAuthorizationPolicy) Resources() []string { return slices.Clone(p.resources) }

// Audiences returns a copy of the permitted RFC 8693 audiences.
func (p SPIFFEAuthorizationPolicy) Audiences() []string { return slices.Clone(p.audiences) }

// TokenExchangeEnabled reports whether RFC 8693 token exchange is permitted.
func (p SPIFFEAuthorizationPolicy) TokenExchangeEnabled() bool { return p.tokenExchange }

// NormalizedSPIFFEPrincipal is an immutable, validated identity selected from a
// SPIFFE association. It represents policy only; it does not authenticate a
// credential or enable an authentication method.
type NormalizedSPIFFEPrincipal struct {
	clientID      string
	spiffeID      string
	trustDomain   string
	authMethod    SPIFFEAuthenticationMethod
	authorization SPIFFEAuthorizationPolicy
}

// ClientID returns the configured OAuth client ID.
func (p NormalizedSPIFFEPrincipal) ClientID() string { return p.clientID }

// SPIFFEID returns the canonical concrete SPIFFE ID.
func (p NormalizedSPIFFEPrincipal) SPIFFEID() string { return p.spiffeID }

// TrustDomain returns the canonical SPIFFE trust domain.
func (p NormalizedSPIFFEPrincipal) TrustDomain() string { return p.trustDomain }

// AuthenticationMethod returns the selected credential-method discriminator.
func (p NormalizedSPIFFEPrincipal) AuthenticationMethod() SPIFFEAuthenticationMethod {
	return p.authMethod
}

// AuthorizationPolicy returns a defensive copy of the selected policy.
func (p NormalizedSPIFFEPrincipal) AuthorizationPolicy() SPIFFEAuthorizationPolicy {
	return SPIFFEAuthorizationPolicy{
		grantTypes:    slices.Clone(p.authorization.grantTypes),
		scopes:        slices.Clone(p.authorization.scopes),
		resources:     slices.Clone(p.authorization.resources),
		audiences:     slices.Clone(p.authorization.audiences),
		tokenExchange: p.authorization.tokenExchange,
	}
}

// NormalizeSPIFFEClientAuth validates and normalizes configured SPIFFE
// associations. It emits one normalized principal for every explicitly enabled
// method, without accepting a credential or advertising authentication.
func NormalizeSPIFFEClientAuth(
	trustDomains []SPIFFETrustDomainRunConfig,
	inboundGrants *InboundGrantsRunConfig,
	scopesSupported []string,
	allowedAudiences []string,
) ([]NormalizedSPIFFEPrincipal, error) {
	if err := ValidateSPIFFETrust(trustDomains, inboundGrants, scopesSupported, allowedAudiences); err != nil {
		return nil, err
	}
	if inboundGrants == nil {
		return nil, nil
	}
	domains, err := validateSPIFFETrustDomains(trustDomains)
	if err != nil {
		return nil, err
	}
	principals := make([]NormalizedSPIFFEPrincipal, 0, len(inboundGrants.SPIFFEClientAuth)*2)
	for _, association := range inboundGrants.SPIFFEClientAuth {
		id, err := parseSPIFFEID(strings.TrimSuffix(association.Principal, "/*"))
		if err != nil {
			return nil, err
		}
		for _, method := range association.Methods {
			principals = append(principals, NormalizedSPIFFEPrincipal{
				clientID:    association.ClientID,
				spiffeID:    id.String(),
				trustDomain: domains[association.TrustDomainRef].trustDomain.String(),
				authMethod:  method,
				authorization: SPIFFEAuthorizationPolicy{
					grantTypes:    slices.Clone(association.GrantTypes),
					scopes:        slices.Clone(association.Scopes),
					resources:     slices.Clone(association.Resources),
					audiences:     slices.Clone(association.Audiences),
					tokenExchange: association.TokenExchange != nil && association.TokenExchange.Enabled,
				},
			})
		}
	}
	return principals, nil
}

// NormalizeSPIFFEPrincipal parses a concrete SPIFFE ID with go-spiffe. It
// rejects wildcard patterns; use MatchSPIFFEPrincipalPattern for policy
// patterns.
func NormalizeSPIFFEPrincipal(principal string) (string, error) {
	return normalizeSPIFFEPrincipal(principal, false)
}

// MatchSPIFFEPrincipalPattern reports whether principal matches pattern. A
// terminal /* matches descendants only at a path-segment boundary: /agent/*
// matches /agent/one but not /agent or /agent-two.
func MatchSPIFFEPrincipalPattern(pattern, principal string) bool {
	normalizedPattern, err := normalizeSPIFFEPrincipal(pattern, true)
	if err != nil {
		return false
	}
	normalizedPrincipal, err := NormalizeSPIFFEPrincipal(principal)
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
// credentials. Callers must use this constructor; a zero-value model is invalid.
func NewSPIFFETrustConfig(
	trustDomains []SPIFFETrustDomainRunConfig,
	inboundGrants *InboundGrantsRunConfig,
	scopesSupported []string,
	allowedAudiences []string,
) (*SPIFFETrustConfig, error) {
	if err := validateSPIFFETrust(trustDomains, inboundGrants, scopesSupported, allowedAudiences); err != nil {
		return nil, err
	}
	if inboundGrants == nil || len(inboundGrants.SPIFFEClientAuth) == 0 {
		return nil, nil
	}

	validatedDomains, err := validateSPIFFETrustDomains(trustDomains)
	if err != nil {
		return nil, err
	}
	config := &SPIFFETrustConfig{
		domains:      make(map[string]SPIFFETrustDomainConfig, len(trustDomains)),
		associations: make([]SPIFFEClientAuthConfig, 0, len(inboundGrants.SPIFFEClientAuth)),
		validated:    true,
	}
	for _, domain := range trustDomains {
		validatedDomain := validatedDomains[domain.Name]
		config.domains[domain.Name] = SPIFFETrustDomainConfig{
			name:         domain.Name,
			trustDomain:  validatedDomain.trustDomain,
			methods:      slices.Clone(domain.Methods),
			bundleSource: normalizeSPIFFEBundleSource(domain.BundleSource),
		}
	}
	for _, association := range inboundGrants.SPIFFEClientAuth {
		principal, err := normalizeSPIFFEPrincipal(association.Principal, true)
		if err != nil {
			return nil, err
		}
		config.associations = append(config.associations, SPIFFEClientAuthConfig{
			trustDomainRef: association.TrustDomainRef,
			principal:      principal,
			clientID:       association.ClientID,
			methods:        slices.Clone(association.Methods),
			authorization: SPIFFEAuthorizationPolicy{
				grantTypes:    slices.Clone(association.GrantTypes),
				scopes:        slices.Clone(association.Scopes),
				resources:     slices.Clone(association.Resources),
				audiences:     slices.Clone(association.Audiences),
				tokenExchange: association.TokenExchange != nil && association.TokenExchange.Enabled,
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
	_, err := NewSPIFFETrustConfig(trustDomains, inboundGrants, scopesSupported, allowedAudiences)
	return err
}

// SPIFFETrustConfig is the immutable normalized SPIFFE trust model used at
// runtime. It contains parsed trust domains and validated association policy.
type SPIFFETrustConfig struct {
	domains      map[string]SPIFFETrustDomainConfig
	associations []SPIFFEClientAuthConfig
	validated    bool
}

// TrustDomain returns a normalized trust-domain declaration by its configured name.
func (c *SPIFFETrustConfig) TrustDomain(name string) (SPIFFETrustDomainConfig, bool) {
	if c == nil || !c.validated {
		return SPIFFETrustDomainConfig{}, false
	}
	domain, ok := c.domains[name]
	return domain.clone(), ok
}

// Associations returns a defensive copy of the normalized association policies.
func (c *SPIFFETrustConfig) Associations() []SPIFFEClientAuthConfig {
	if c == nil || !c.validated {
		return nil
	}
	associations := make([]SPIFFEClientAuthConfig, len(c.associations))
	for i, association := range c.associations {
		associations[i] = association.clone()
	}
	return associations
}

// SPIFFETrustDomainConfig is a parsed, immutable runtime trust-domain declaration.
type SPIFFETrustDomainConfig struct {
	name         string
	trustDomain  spiffeid.TrustDomain
	methods      []SPIFFEAuthenticationMethod
	bundleSource SPIFFEBundleSourceConfig
}

// Name returns the configured trust-domain declaration name.
func (c SPIFFETrustDomainConfig) Name() string { return c.name }

// TrustDomain returns the canonical SPIFFE trust domain.
func (c SPIFFETrustDomainConfig) TrustDomain() string { return c.trustDomain.String() }

// Methods returns a copy of the explicitly enabled credential methods.
func (c SPIFFETrustDomainConfig) Methods() []SPIFFEAuthenticationMethod {
	return slices.Clone(c.methods)
}

// BundleSource returns the selected, immutable bundle-source declaration.
func (c SPIFFETrustDomainConfig) BundleSource() SPIFFEBundleSourceConfig { return c.bundleSource }

func (c SPIFFETrustDomainConfig) clone() SPIFFETrustDomainConfig {
	c.methods = slices.Clone(c.methods)
	return c
}

// SPIFFEBundleSourceConfig is the normalized bundle-source discriminator.
type SPIFFEBundleSourceConfig struct {
	sourceType SPIFFEBundleSourceType
	endpoint   string
}

// Type returns the selected bundle-source type.
func (c SPIFFEBundleSourceConfig) Type() SPIFFEBundleSourceType { return c.sourceType }

// Endpoint returns the configured Bundle Endpoint URL, or an empty string for
// a Workload API source.
func (c SPIFFEBundleSourceConfig) Endpoint() string { return c.endpoint }

func normalizeSPIFFEBundleSource(source SPIFFEBundleSourceRunConfig) SPIFFEBundleSourceConfig {
	endpoint := ""
	if source.Endpoint != nil {
		endpoint = source.Endpoint.URL
	}
	return SPIFFEBundleSourceConfig{sourceType: source.Type, endpoint: endpoint}
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
		grantTypes:    slices.Clone(c.authorization.grantTypes),
		scopes:        slices.Clone(c.authorization.scopes),
		resources:     slices.Clone(c.authorization.resources),
		audiences:     slices.Clone(c.authorization.audiences),
		tokenExchange: c.authorization.tokenExchange,
	}
}

func (c SPIFFEClientAuthConfig) clone() SPIFFEClientAuthConfig {
	c.methods = slices.Clone(c.methods)
	c.authorization = c.AuthorizationPolicy()
	return c
}

// validateSPIFFETrust validates SPIFFE trust declarations and their inbound
// associations without fetching bundles or validating credentials. It fails
// closed on ambiguity because later authentication must not depend on
// configuration order.
func validateSPIFFETrust(
	trustDomains []SPIFFETrustDomainRunConfig,
	inboundGrants *InboundGrantsRunConfig,
	scopesSupported []string,
	allowedAudiences []string,
) error {
	trustDomainByName, err := validateSPIFFETrustDomains(trustDomains)
	if err != nil {
		return err
	}
	if inboundGrants == nil {
		if len(trustDomains) != 0 {
			return fmt.Errorf("inbound_grants.spiffe_client_auth is required when spiffe_trust_domains is configured")
		}
		return nil
	}
	if len(inboundGrants.SPIFFEClientAuth) == 0 {
		if len(trustDomains) != 0 {
			return fmt.Errorf("inbound_grants.spiffe_client_auth is required when spiffe_trust_domains is configured")
		}
		return nil
	}
	return validateSPIFFEClientAuth(inboundGrants.SPIFFEClientAuth, trustDomainByName, scopesSupported, allowedAudiences)
}

type validatedSPIFFETrustDomain struct {
	trustDomain spiffeid.TrustDomain
	methods     map[SPIFFEAuthenticationMethod]struct{}
}

func validateSPIFFETrustDomains(domains []SPIFFETrustDomainRunConfig) (map[string]validatedSPIFFETrustDomain, error) {
	byName := make(map[string]validatedSPIFFETrustDomain, len(domains))
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
		methods, err := validateMethods(domain.Methods, fmt.Sprintf("spiffe_trust_domains[%d].methods", i))
		if err != nil {
			return nil, err
		}
		if err := validateSPIFFEBundleSource(domain.BundleSource, i); err != nil {
			return nil, err
		}
		byName[domain.Name] = validatedSPIFFETrustDomain{trustDomain: trustDomain, methods: methods}
	}
	return byName, nil
}

func validateSPIFFEBundleSource(source SPIFFEBundleSourceRunConfig, index int) error {
	switch source.Type {
	case SPIFFEBundleSourceTypeEndpoint:
		if source.Endpoint == nil || source.WorkloadAPI != nil {
			return fmt.Errorf("spiffe_trust_domains[%d].bundle_source: type %q requires endpoint only", index, source.Type)
		}
		return validateSPIFFEBundleEndpoint(source.Endpoint.URL, index)
	case SPIFFEBundleSourceTypeWorkloadAPI:
		if source.WorkloadAPI == nil || source.Endpoint != nil {
			return fmt.Errorf("spiffe_trust_domains[%d].bundle_source: type %q requires workload_api only", index, source.Type)
		}
		return nil
	default:
		return fmt.Errorf("spiffe_trust_domains[%d].bundle_source.type: unknown source type %q", index, source.Type)
	}
}

func validateSPIFFEBundleEndpoint(endpointURL string, index int) error {
	u, err := url.ParseRequestURI(endpointURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf(
			"spiffe_trust_domains[%d].bundle_source.endpoint.url must be an absolute HTTPS URL with a valid authority",
			index,
		)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.Contains(endpointURL, "?") || strings.Contains(endpointURL, "#") ||
		net.ParseIP(u.Hostname()) != nil {
		return fmt.Errorf(
			"spiffe_trust_domains[%d].bundle_source.endpoint.url must not contain credentials, query, fragment, or an IP-literal host",
			index,
		)
	}
	return nil
}

func validateSPIFFEClientAuth(
	entries []SPIFFEClientAuthRunConfig,
	trustDomainByName map[string]validatedSPIFFETrustDomain,
	scopesSupported []string,
	allowedAudiences []string,
) error {
	effectiveScopes := scopesSupported
	if len(effectiveScopes) == 0 {
		effectiveScopes = registration.DefaultScopes
	}
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
		principal, err := validateSPIFFEClientAssociation(entry, i, trustDomain, effectiveScopes, allowedAudiences)
		if err != nil {
			return err
		}
		if _, exists := clientIDs[entry.ClientID]; exists {
			return fmt.Errorf("inbound_grants.spiffe_client_auth[%d]: duplicate client_id %q", i, entry.ClientID)
		}
		if overlapsSPIFFEPatterns(seenPrincipals, principal) {
			return fmt.Errorf("inbound_grants.spiffe_client_auth[%d]: principal %q overlaps an existing policy", i, entry.Principal)
		}
		seenPrincipals = append(seenPrincipals, principal)
		clientIDs[entry.ClientID] = struct{}{}
		referencedTrustDomains[entry.TrustDomainRef] = struct{}{}
	}
	for name := range trustDomainByName {
		if _, ok := referencedTrustDomains[name]; !ok {
			return fmt.Errorf("spiffe_trust_domains: trust domain %q is not referenced by inbound_grants.spiffe_client_auth", name)
		}
	}
	return nil
}

func validateSPIFFEClientAssociation(
	entry SPIFFEClientAuthRunConfig,
	index int,
	trustDomain validatedSPIFFETrustDomain,
	effectiveScopes []string,
	allowedAudiences []string,
) (string, error) {
	principal, err := normalizeSPIFFEPrincipal(entry.Principal, true)
	if err != nil {
		return "", fmt.Errorf("inbound_grants.spiffe_client_auth[%d]: principal: %w", index, err)
	}
	principalID, err := parseSPIFFEID(strings.TrimSuffix(principal, "/*"))
	if err != nil {
		return "", fmt.Errorf("inbound_grants.spiffe_client_auth[%d]: principal: %w", index, err)
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
	if err := validateResourceIndicators(entry.Resources, fieldPrefix+".resources"); err != nil {
		return "", err
	}
	for _, resource := range entry.Resources {
		if !slices.Contains(allowedAudiences, resource) {
			return "", fmt.Errorf("%s.resources: resource %q is not allowed by allowed_audiences", fieldPrefix, resource)
		}
	}
	if err := validateDistinctNonEmpty(entry.Audiences, fieldPrefix+".audiences"); err != nil {
		return "", err
	}
	if err := validateDistinctNonEmpty(entry.Scopes, fieldPrefix+".scopes"); err != nil {
		return "", err
	}
	if err := registration.ValidateScopeSubset(entry.Scopes, effectiveScopes, fieldPrefix+".scopes"); err != nil {
		return "", err
	}
	if err := validateSPIFFEGrants(entry.GrantTypes, entry.TokenExchange, index); err != nil {
		return "", err
	}
	return principal, nil
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

func validateResourceIndicators(values []string, field string) error {
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

func validateSPIFFEGrants(grants []string, exchange *SPIFFETokenExchangeRunConfig, index int) error {
	if len(grants) != 1 || grants[0] != SPIFFEGrantTypeTokenExchange {
		return fmt.Errorf("inbound_grants.spiffe_client_auth[%d].grant_types must be exactly [%q]", index, SPIFFEGrantTypeTokenExchange)
	}
	if exchange == nil || !exchange.Enabled {
		return fmt.Errorf("inbound_grants.spiffe_client_auth[%d]: token_exchange must be enabled for token-exchange grant", index)
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
		return MatchSPIFFEPrincipalPattern(first, second)
	}
	return MatchSPIFFEPrincipalPattern(second, first)
}

// CloneSPIFFETrustDomains returns a deep copy. SPIFFE trust-domain declarations
// currently contain only scalar strings, so each explicitly copied value has no
// mutable backing storage. Add a deep copy here when a reference field is added.
func CloneSPIFFETrustDomains(domains []SPIFFETrustDomainRunConfig) []SPIFFETrustDomainRunConfig {
	clone := make([]SPIFFETrustDomainRunConfig, len(domains))
	for i, domain := range domains {
		clone[i] = SPIFFETrustDomainRunConfig{
			Name:        domain.Name,
			TrustDomain: domain.TrustDomain,
			Methods:     slices.Clone(domain.Methods),
			BundleSource: SPIFFEBundleSourceRunConfig{
				Type: domain.BundleSource.Type,
			},
		}
		if domain.BundleSource.Endpoint != nil {
			clone[i].BundleSource.Endpoint = &SPIFFEBundleEndpointSourceRunConfig{URL: domain.BundleSource.Endpoint.URL}
		}
		if domain.BundleSource.WorkloadAPI != nil {
			clone[i].BundleSource.WorkloadAPI = &SPIFFEWorkloadAPIBundleSourceRunConfig{}
		}
	}
	return clone
}

// CloneInboundGrants returns a deep copy that does not share mutable fields
// with the caller.
func CloneInboundGrants(grants *InboundGrantsRunConfig) *InboundGrantsConfig {
	if grants == nil {
		return nil
	}
	clone := *grants
	clone.SPIFFEClientAuth = make([]SPIFFEClientAuthRunConfig, len(grants.SPIFFEClientAuth))
	for i, entry := range grants.SPIFFEClientAuth {
		clone.SPIFFEClientAuth[i] = entry
		clone.SPIFFEClientAuth[i].Methods = append([]SPIFFEAuthenticationMethod(nil), entry.Methods...)
		clone.SPIFFEClientAuth[i].Resources = append([]string(nil), entry.Resources...)
		clone.SPIFFEClientAuth[i].Audiences = append([]string(nil), entry.Audiences...)
		clone.SPIFFEClientAuth[i].Scopes = append([]string(nil), entry.Scopes...)
		clone.SPIFFEClientAuth[i].GrantTypes = append([]string(nil), entry.GrantTypes...)
		if entry.TokenExchange != nil {
			tokenExchange := *entry.TokenExchange
			clone.SPIFFEClientAuth[i].TokenExchange = &tokenExchange
		}
	}
	return &clone
}
