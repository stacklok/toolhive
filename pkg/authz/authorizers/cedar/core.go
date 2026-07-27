// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cedar provides authorization utilities using Cedar policies.
package cedar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/syncutil"
)

// ConfigType is the configuration type identifier for Cedar authorization.
const ConfigType = "cedarv1"

func init() {
	// Register the Cedar authorizer factory with the authorizers registry.
	authorizers.Register(ConfigType, &Factory{})
}

// Config represents the complete authorization configuration file structure
// for Cedar authorization. This includes the common version/type fields plus
// the Cedar-specific "cedar" field. This maintains backwards compatibility
// with the v1.0 configuration schema.
type Config struct {
	Version string         `json:"version"`
	Type    string         `json:"type"`
	Options *ConfigOptions `json:"cedar"`
}

// ExtractConfig extracts the Cedar configuration from an authorizers.Config.
// This is useful for tests and other code that needs to inspect the Cedar configuration
// after it has been loaded into the generic Config structure.
// To access the Cedar-specific options (policies, entities), use the returned Config's Cedar field.
func ExtractConfig(authzConfig *authorizers.Config) (*Config, error) {
	if authzConfig == nil {
		return nil, fmt.Errorf("config is nil")
	}
	rawConfig := authzConfig.RawConfig()
	if len(rawConfig) == 0 {
		return nil, fmt.Errorf("config has no raw data")
	}

	var config Config
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	if config.Options == nil {
		return nil, fmt.Errorf("cedar config is nil")
	}
	return &config, nil
}

// InjectUpstreamProvider returns a new authorizers.Config that is identical to
// src except that the Cedar options' PrimaryUpstreamProvider field is set to
// providerName. Any existing PrimaryUpstreamProvider value is overwritten; if
// the Cedar config file already contains a non-empty PrimaryUpstreamProvider
// that differs from providerName, the file value is silently replaced. This is
// intentional: the embedded auth server config is the authoritative source of
// the upstream provider name at runtime. This is used by the runner middleware
// when the embedded auth server is active to wire the upstream provider into
// Cedar evaluation.
//
// If src is not a Cedar config, providerName is empty, or src is nil, src is
// returned unchanged with a nil error. This makes the function safe to call
// unconditionally whenever the embedded auth server is active.
func InjectUpstreamProvider(src *authorizers.Config, providerName string) (*authorizers.Config, error) {
	if src == nil || providerName == "" {
		return src, nil
	}

	cedarCfg, err := ExtractConfig(src)
	if err != nil {
		// src is not a Cedar config (e.g. a future HTTP authorizer); treat as a
		// no-op so callers can apply this unconditionally without needing to
		// know the authorizer type ahead of time.
		slog.Debug("skipping upstream provider injection for non-Cedar config",
			"provider", providerName, "type", src.Type)
		return src, nil
	}

	cedarCfg.Options.PrimaryUpstreamProvider = providerName
	return authorizers.NewConfig(cedarCfg)
}

// Factory implements the authorizers.AuthorizerFactory interface for Cedar.
type Factory struct{}

// ConfigKey returns the JSON key for Cedar-specific configuration ("cedar").
func (*Factory) ConfigKey() string { return "cedar" }

// ValidateConfig validates the Cedar-specific configuration.
// It receives the full raw config and extracts the Cedar-specific portion.
func (*Factory) ValidateConfig(rawConfig json.RawMessage) error {
	var config Config
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return fmt.Errorf("failed to parse configuration: %w", err)
	}

	if config.Options == nil {
		return fmt.Errorf("cedar configuration is required (missing 'cedar' field)")
	}

	if len(config.Options.Policies) == 0 {
		return fmt.Errorf("at least one policy is required for Cedar authorization")
	}

	return nil
}

// CreateAuthorizer creates a Cedar Authorizer from the configuration.
// It receives the full raw config and extracts the Cedar-specific portion.
func (*Factory) CreateAuthorizer(rawConfig json.RawMessage, serverName string) (authorizers.Authorizer, error) {
	var config Config
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return nil, fmt.Errorf("failed to parse configuration: %w", err)
	}

	if config.Options == nil {
		return nil, fmt.Errorf("cedar configuration is required (missing 'cedar' field)")
	}

	return NewCedarAuthorizer(*config.Options, serverName)
}

// Common errors for Cedar authorization
var (
	ErrNoPolicies           = errors.New("no policies loaded")
	ErrInvalidPolicy        = errors.New("invalid policy")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrMissingPrincipal     = errors.New("missing principal")
	ErrMissingAction        = errors.New("missing action")
	ErrMissingResource      = errors.New("missing resource")
	ErrFailedToLoadEntities = errors.New("failed to load entities")
)

// ClientIDContextKey is the key used to store client ID in the context.
type ClientIDContextKey struct{}

// claimPrefix namespaces every JWT claim in the Cedar context/principal
// attributes. claimSetPrefix namespaces the synthetic multi-valued-claim Sets.
// They are deliberately disjoint: because every JWT claim is emitted under
// claimPrefix, a token can never produce a claimSetPrefix key, so the synthetic
// Sets cannot be shadowed or spoofed by claim content.
const (
	claimPrefix    = "claim_"
	claimSetPrefix = "claimset_"
)

// Authorizer authorizes MCP operations using Cedar policies.
type Authorizer struct {
	// Cedar policy set
	policySet *cedar.PolicySet
	// Cedar entities
	entities cedar.EntityMap
	// Entity factory for creating entities
	entityFactory *EntityFactory
	// Mutex for thread safety
	mu sync.RWMutex
	// primaryUpstreamProvider names the upstream IDP provider whose access token
	// is the source of JWT claims for Cedar evaluation, aside from the narrow
	// user-profile supplement and the opaque-access-token case described in
	// resolveClaims — both of which read the same provider's id_token.
	// When empty, claims from the token on the original client request are used,
	// which may be a ToolHive-issued token or any other bearer token.
	primaryUpstreamProvider string
	// groupClaimName is the JWT claim key that contains group membership.
	// When empty, the well-known defaults are checked ("groups", "roles", etc.).
	groupClaimName string
	// roleClaimName is the JWT claim key that contains role membership.
	// When empty, no role extraction is performed (backward compatible).
	roleClaimName string
	// serverName is the identity of the MCP server this authorizer is scoped to.
	// Used by downstream enterprise features for server-scoped Cedar policies
	// (e.g. resource in MCP::"<server>"). When empty (standalone Cedar usage
	// with no enterprise controller), the authorizer behaves identically to
	// the unscoped case.
	serverName string
	// claimKeyLog rate-limits the diagnostic log of resolved JWT claim keys
	// so it emits at most once per 30 seconds instead of once per authorization check.
	claimKeyLog *syncutil.AtMost
	// supplementLog rate-limits the warning that the upstream access token lacked
	// profile claims. It is deliberately separate from claimKeyLog: a shared
	// timer would let whichever fires first suppress the other for the rest of
	// the window, hiding the claim-key dump on exactly the deployments that need it.
	supplementLog *syncutil.AtMost
	// missingIDTokenLog rate-limits the warning that the primary provider has no
	// usable stored id_token — meaning either that profile claims cannot be
	// supplemented (JWT access token) or that there is no upstream claim source at
	// all (opaque access token). Separate from supplementLog because the two are
	// mutually exclusive per request and describe opposite conditions.
	missingIDTokenLog *syncutil.AtMost
	// multiValuedClaims lists JWT claim names normalized to a canonical unpadded
	// space-delimited string, plus a companion Cedar Set, before Cedar evaluation.
	// See ConfigOptions.MultiValuedClaims.
	multiValuedClaims []string
}

// ConfigOptions represents the Cedar-specific authorization configuration options.
type ConfigOptions struct {
	// Policies is a list of Cedar policy strings
	Policies []string `json:"policies" yaml:"policies"`

	// EntitiesJSON is the JSON string representing Cedar entities
	EntitiesJSON string `json:"entities_json" yaml:"entities_json"`

	// PrimaryUpstreamProvider names the upstream IDP provider whose access
	// token is the primary source of JWT claims for Cedar evaluation.
	// When empty, claims from the ToolHive-issued token are used.
	// Must match an entry in identity.UpstreamTokens (e.g. "default", "github").
	//
	// The profile claims in profileClaimsFromIDToken fall back to the SAME
	// provider's id_token when its access token omits them (many OIDC providers
	// assert `email` only in the id_token), so `principal has claim_email` keeps
	// meaning "this upstream asserted an email". An opaque access token has no
	// claims at all, so that provider's id_token supplies the principal and those
	// same profile claims instead. Every other claim, including all group/role/scope
	// claims, comes from the upstream access token or not at all, and the
	// ToolHive-issued token the client presented is a claim source only for an
	// opaque-access-token provider with no stored id_token, which has no upstream
	// claim source whatsoever. See resolveClaims for the full contract.
	PrimaryUpstreamProvider string `json:"primary_upstream_provider,omitempty" yaml:"primary_upstream_provider,omitempty"`

	// GroupClaimName is the JWT claim key that contains group membership for the
	// principal. When set, it takes priority over the well-known defaults
	// ("groups", "roles", "cognito:groups"). Use this for IDPs that place groups
	// under a URI-style claim (e.g. "https://example.com/groups" in Auth0/Okta).
	// When empty, only the well-known claim names are checked.
	GroupClaimName string `json:"group_claim_name,omitempty" yaml:"group_claim_name,omitempty"`

	// RoleClaimName is the JWT claim key that contains role membership for the
	// principal. When set, the claim is extracted separately from GroupClaimName
	// and both are mapped to the configured group entity type (default "THVGroup").
	// When empty, no role extraction is performed (backward compatible).
	RoleClaimName string `json:"role_claim_name,omitempty" yaml:"role_claim_name,omitempty"`

	// GroupEntityType is the Cedar entity type name used for Client parent UIDs
	// synthesised from JWT group/role claims. Defaults to "THVGroup" when empty,
	// preserving the original behaviour. Must be a valid Cedar identifier — namespaced
	// names (e.g. "Platform::Group") are not yet supported and are rejected at
	// construction. See issue #5072.
	GroupEntityType string `json:"group_entity_type,omitempty" yaml:"group_entity_type,omitempty"`

	// MultiValuedClaims lists JWT claim NAMES (bare, e.g. "scp" or "scope") whose
	// value is exposed to Cedar in two normalized forms, regardless of whether the
	// IdP emits the claim as a space-delimited string (Entra `scp`, Keycloak `scope`)
	// or a JSON array (Okta `scp`):
	//
	//  1. "claim_<name>" (both principal attribute and context) is a space-delimited
	//     string: an array is joined with single spaces; a string value is passed
	//     through VERBATIM — its spacing and order are preserved, not
	//     re-canonicalized. Use this with `like`/`==` string policies.
	//  2. "claimset_<name>" (bare key, both principal attribute and context) is a Cedar
	//     Set of the claim's elements. Use this with `.contains`/`.containsAll`/
	//     `.containsAny` for exact-element matching that cannot be fooled by a
	//     substring (e.g. "Mail.Read" will not match "Mail.ReadWrite"). The "claimset_"
	//     prefix is reserved and never collides with a JWT claim, since all JWT
	//     claims are surfaced under the "claim_" prefix.
	//
	// Only for claims whose ELEMENTS are space-free (OAuth scopes, per RFC 6749 §3.3).
	// Do NOT list group/role claims or any claim whose elements can contain spaces
	// (e.g. group display names) — element boundaries would become ambiguous.
	//
	// Backward compatibility: opt-in. When empty (default), "claim_<name>" is
	// byte-identical to today and no "claimset_<name>" key is added. Listing a claim
	// leaves OTHER claims untouched.
	//
	// Migration hazard: listing a claim that an IdP emits as an ARRAY changes its
	// "claim_<name>" form from a Cedar Set to a String. A pre-existing policy that
	// tested it with `like`/substring — which errored on the Set and so failed
	// closed (deny) — will then match against the joined string and can
	// substring-match (e.g. `context.claim_scp like "*Mail.Read*"` also matches
	// "Mail.ReadWrite"), an unintended grant. Audit existing `like` policies on a
	// claim before listing it, and prefer "claimset_<name>" with `.contains`/
	// `.containsAll`/`.containsAny` for exact-element membership.
	//
	// Note: this unifies claim SHAPE, not NAME — `scp` and `scope` remain distinct
	// claim names, so a policy still references one specific name.
	MultiValuedClaims []string `json:"multi_valued_claims,omitempty" yaml:"multi_valued_claims,omitempty"`
}

// validateGroupEntityType validates a GroupEntityType value. Empty string is
// valid — it means "use the default" and is resolved by NewEntityFactory.
// Non-empty values must:
//   - not contain Cedar's "::" namespace separator (out of scope per #5072), and
//   - parse cleanly as a Cedar identifier when used as an entity type in a
//     synthetic policy. We delegate the identifier-grammar check to cedar-go's
//     policy parser so that future grammar refinements in upstream cedar-go are
//     picked up automatically — this is the source of truth for Cedar identifier
//     validity. Hand-rolling the grammar (reserved words, ANYIDENT regex,
//     __cedar prefix) duplicates rules cedar-go already enforces.
func validateGroupEntityType(s string) error {
	if s == "" {
		return nil
	}

	// Check for namespace separator first: namespaced types are out of scope.
	// This must run before the cedar-go round-trip because the Cedar parser
	// accepts "Foo::Bar" as a valid namespaced type, but we reject it for
	// project-specific reasons.
	if strings.Contains(s, "::") {
		return fmt.Errorf("group_entity_type %q contains \"::\": namespaced entity types are not yet supported", s)
	}

	// Round-trip through cedar-go's policy parser. If the synthesized policy
	// text fails to parse, the type name violates Cedar's identifier grammar
	// (reserved word, invalid character, leading digit, etc.).
	synth := fmt.Sprintf(`permit(principal in %s::"x", action, resource);`, s)
	var p cedar.Policy
	if err := p.UnmarshalCedar([]byte(synth)); err != nil {
		return fmt.Errorf("group_entity_type %q is not a valid Cedar identifier: %w", s, err)
	}
	return nil
}

// NewCedarAuthorizer creates a new Cedar authorizer.
// serverName is a runtime-injected value (not user-authored config) that
// identifies which MCP server this authorizer is scoped to.
// If a second runtime-injected value is needed, bundle both into a
// RuntimeContext struct to keep the factory interface stable.
func NewCedarAuthorizer(options ConfigOptions, serverName string) (authorizers.Authorizer, error) {
	if err := validateGroupEntityType(options.GroupEntityType); err != nil {
		return nil, err
	}

	authorizer := &Authorizer{
		policySet:               cedar.NewPolicySet(),
		entities:                cedar.EntityMap{},
		entityFactory:           NewEntityFactory(cedar.EntityType(options.GroupEntityType)),
		primaryUpstreamProvider: options.PrimaryUpstreamProvider,
		groupClaimName:          options.GroupClaimName,
		roleClaimName:           options.RoleClaimName,
		serverName:              serverName,
		claimKeyLog:             syncutil.NewAtMost(30 * time.Second),
		supplementLog:           syncutil.NewAtMost(30 * time.Second),
		missingIDTokenLog:       syncutil.NewAtMost(30 * time.Second),
		multiValuedClaims:       options.MultiValuedClaims,
	}

	// Load policies
	if len(options.Policies) == 0 {
		return nil, ErrNoPolicies
	}

	for i, policyStr := range options.Policies {
		var policy cedar.Policy
		if err := policy.UnmarshalCedar([]byte(policyStr)); err != nil {
			return nil, fmt.Errorf("failed to parse policy %d: %w", i, err)
		}

		policyID := cedar.PolicyID(fmt.Sprintf("policy%d", i))
		authorizer.policySet.Add(policyID, &policy)
	}

	// Load entities if provided
	if options.EntitiesJSON != "" {
		if err := json.Unmarshal([]byte(options.EntitiesJSON), &authorizer.entities); err != nil {
			return nil, fmt.Errorf("failed to parse entities JSON: %w", err)
		}

		// Warn once if entities_json contains stale THVGroup entities while
		// GroupEntityType is configured to a different type. Cedar's `in` operator
		// compares entity UIDs by type name, so the pre-loaded THVGroup entities will
		// never match the synthesised parents and any policy referencing them will
		// silently deny every request.
		if options.GroupEntityType != "" && options.GroupEntityType != string(EntityTypeTHVGroup) {
			for uid := range authorizer.entities {
				if uid.Type == EntityTypeTHVGroup {
					slog.Warn("Cedar entities_json contains THVGroup entities but GroupEntityType is set to a different value; "+
						"synthesised group parents will not match these pre-loaded entities and policies that reference them will silently deny",
						"configured_group_entity_type", options.GroupEntityType,
						"stale_entity_uid", uid.String())
					break
				}
			}
		}
	}

	return authorizer, nil
}

// UpdatePolicies updates the Cedar policies.
func (a *Authorizer) UpdatePolicies(policies []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(policies) == 0 {
		return ErrNoPolicies
	}

	newPolicySet := cedar.NewPolicySet()

	for i, policyStr := range policies {
		var policy cedar.Policy
		if err := policy.UnmarshalCedar([]byte(policyStr)); err != nil {
			return fmt.Errorf("failed to parse policy %d: %w", i, err)
		}

		policyID := cedar.PolicyID(fmt.Sprintf("policy%d", i))
		newPolicySet.Add(policyID, &policy)
	}

	a.policySet = newPolicySet
	return nil
}

// UpdateEntities updates the Cedar entities.
func (a *Authorizer) UpdateEntities(entitiesJSON string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var newEntities cedar.EntityMap
	if err := json.Unmarshal([]byte(entitiesJSON), &newEntities); err != nil {
		return fmt.Errorf("failed to parse entities JSON: %w", err)
	}

	a.entities = newEntities
	return nil
}

// AddEntity adds or updates an entity in the authorizer's entity store.
func (a *Authorizer) AddEntity(entity cedar.Entity) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.entities[entity.UID] = entity
}

// RemoveEntity removes an entity from the authorizer's entity store.
func (a *Authorizer) RemoveEntity(uid cedar.EntityUID) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.entities, uid)
}

// GetEntity retrieves an entity from the authorizer's entity store.
func (a *Authorizer) GetEntity(uid cedar.EntityUID) (cedar.Entity, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	entity, found := a.entities[uid]
	return entity, found
}

// GetEntityFactory returns the entity factory associated with this authorizer.
func (a *Authorizer) GetEntityFactory() *EntityFactory {
	return a.entityFactory
}

// IsAuthorized checks if a request is authorized.
// This is the core authorization method that all other authorization methods use.
// It takes:
// - principal: The entity making the request (e.g., "Client::vscode_extension_123")
// - action: The operation being performed (e.g., "Action::call_tool")
// - resource: The object being accessed (e.g., "Tool::weather")
// - context: Additional information about the request
//
// Note: group-based Cedar policies (e.g. "principal in THVGroup::\"eng\"" with the
// default group entity type — see ConfigOptions.GroupEntityType) require that
// group parent entities are included in the entity map. See #4768 for the group
// parent wiring that will set these up via CreatePrincipalEntity.
// - entities: Optional Cedar entity map with attributes
func (a *Authorizer) IsAuthorized(
	principal, action, resource string,
	contextMap map[string]interface{},
	entities ...cedar.EntityMap,
) (bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if principal == "" {
		return false, ErrMissingPrincipal
	}

	if action == "" {
		return false, ErrMissingAction
	}

	if resource == "" {
		return false, ErrMissingResource
	}

	// Parse principal, action, and resource
	principalType, principalID, err := parseCedarEntityID(principal)
	if err != nil {
		return false, err
	}

	actionType, actionID, err := parseCedarEntityID(action)
	if err != nil {
		return false, err
	}

	resourceType, resourceID, err := parseCedarEntityID(resource)
	if err != nil {
		return false, err
	}

	// Create context record
	contextRecord := convertMapToCedarRecord(contextMap)

	// Create Cedar request
	req := cedar.Request{
		Principal: cedar.NewEntityUID(cedar.EntityType(principalType), cedar.String(principalID)),
		Action:    cedar.NewEntityUID(cedar.EntityType(actionType), cedar.String(actionID)),
		Resource:  cedar.NewEntityUID(cedar.EntityType(resourceType), cedar.String(resourceID)),
		Context:   contextRecord,
	}

	// Use the provided entities if available, otherwise use the default entities
	entityMap := a.entities
	if len(entities) > 0 && entities[0] != nil {
		// Merge the request entities with the default entities
		// This allows policies to reference both the request-specific entities
		// and any global entities defined in the authorizer
		mergedEntities := make(cedar.EntityMap)
		for k, v := range a.entities {
			mergedEntities[k] = v
		}
		for k, v := range entities[0] {
			mergedEntities[k] = v
		}

		entityMap = mergedEntities
	}

	// Debug logging for authorization
	slog.Debug("cedar authorization check",
		"principal", req.Principal, "action", req.Action, "resource", req.Resource)
	slog.Debug("cedar context", "context", req.Context)

	// Check authorization
	decision, diagnostic := cedar.Authorize(a.policySet, entityMap, req)

	// Log the decision
	slog.Debug("cedar decision", "decision", decision, "diagnostic", diagnostic)

	// Cedar's Authorize returns a Decision and a Diagnostic
	// Check if the Diagnostic contains any errors
	if len(diagnostic.Errors) > 0 {
		return false, fmt.Errorf("authorization error: %v", diagnostic.Errors)
	}
	return decision == cedar.Allow, nil
}

// resolveClaims determines which JWT claims to use for Cedar policy evaluation.
//
// When primaryUpstreamProvider is empty (the default), the claims of the token on
// the original client request are used as-is. That may be a ToolHive-issued token
// or any other bearer token.
//
// When primaryUpstreamProvider is set (the embedded auth server path), the claim
// source is that provider's upstream credentials — and only that provider's. Its
// access token is primary; the profile claims listed in profileClaimsFromIDToken
// fall back to the same provider's id_token when the access token does not carry
// them, because many OIDC providers assert `email` only in the id_token and the
// access token alone was therefore a deny-all trap (#5916).
//
// Both tokens belong to the same upstream provider and session: the auth
// middleware splits a single credential bundle into Identity.UpstreamTokens and
// Identity.UpstreamIDTokens (see TokenValidator.Middleware in pkg/auth/token.go),
// so a given key denotes the same provider and session in both maps. That is what
// makes a supplemented claim carry the same provenance as one read from the access
// token: `principal has claim_email` means "this upstream asserted an email". (The
// two maps' key SETS may differ — a provider can have an access token but no
// id_token, which is why the fallback is conditional. That is not a case of one key
// meaning two different identities.)
//
// The ToolHive-issued token the client presented is deliberately NOT a claim
// source here, even though it mirrors a name/email — in a multi-upstream chain
// those belong to the first configured upstream, which need not be the pinned
// provider, so using them could attribute one IdP's email to another. The single
// exception is the last-resort case at the end of this comment, where the pinned
// provider offers no claim source at all.
//
// When the pinned provider's access token is OPAQUE rather than JWT-shaped
// (Google's ya29.*, GitHub's gho_*) it carries no claims to read, so the same
// provider's id_token stands in as the claim carrier: it supplies the Cedar
// principal (`sub`) plus the profileClaimsFromIDToken it asserts, and nothing
// else. This keeps the principal on the upstream subject and the profile claims
// attributed to the pinned provider — what the two branches used to disagree
// about (#6048). `sub` is not "supplemented" here in the sense the bullets below
// forbid: it is the subject of the only claim source there is, for the same
// provider and session, and no access-token `sub` exists to be overridden.
//
// A consequence worth stating: the claims the ToolHive-issued token carries in its
// own right — `client_id` (see session.New in pkg/authserver/server/session) plus
// fosite's `iss`/`aud`/`exp`/`iat`/`jti` — are no longer visible to policies on
// this path, so a rule gating on `claim_client_id` stops matching. That removes an
// asymmetry rather than creating one: the JWT branch never had those either, since
// neither the upstream access token nor its id_token carries this auth server's
// client_id, and any `client_id` an upstream access token does assert is the auth
// server's own registration at that upstream, not the calling MCP client. The loss
// also fails toward deny, since `principal has claim_client_id` becomes false.
//
// A pinned provider whose access token is opaque AND that has no usable stored
// id_token has no upstream claim source at all. That is a pure OAuth 2.0 upstream
// (pkg/authserver/upstream/oauth2.go) never asked for the `openid` scope, whose
// identity is resolved from a userinfo endpoint and never lands in an id_token;
// an OIDC upstream always has one. For that configuration ALONE the claims of the
// token on the original client request are used, exactly as they were before
// #6048 — so the Cedar principal is ToolHive's internal user ID and the profile
// claims are the auth server's mirror of the FIRST configured upstream. Failing
// closed instead would deny every request on those deployments with nothing an
// operator could configure to recover, which is the deny-all trap #5916 was; the
// weaker attribution is the lesser harm, and a rate-limited WARN names the
// provider so the condition is diagnosable from logs alone.
//
// The supplement is restricted to profileClaimsFromIDToken rather than merging
// the two token bodies. Read this before widening it:
//
//   - An id_token's registered claims (`iss`, `aud`, `exp`, `nonce`, `at_hash`,
//     `azp`) describe that token, not the user. Merging them would overwrite the
//     access token's own `aud`/`exp` with values that mean something different.
//   - Authorization-bearing claims (`groups`, `roles`, `scope`) are left alone for
//     now. Taking them from this provider's id_token would be provenance-correct —
//     unlike taking them from the ToolHive-issued token, which must never happen —
//     but it would newly grant requests that deny today, so it belongs in its own
//     change rather than riding along with a deny-all fix.
//   - `sub` is excluded because it becomes the Cedar principal entity ID rather
//     than an attribute, and Cedar has no `has`-style guard in the principal
//     position. An access token without `sub` violates RFC 9068; failing closed
//     with ErrMissingPrincipal beats silently choosing a principal. An opaque
//     access token is a different case, not an exception to this one: it asserts
//     nothing, so there is no access-token `sub` to override.
//
// An access-token value always wins, so a claim the access token does assert can
// never be shadowed. A claim absent from both tokens stays absent, so `has`-guarded
// policies still fail closed.
//
// The id_token is used without checking `exp`, deliberately: it is read as a record
// of what the upstream asserted at login, not presented as a credential, and
// rejecting it once expired would flip a policy from permit to deny mid-session.
// See Identity.UpstreamIDTokens for that contract and its two consumers.
func (a *Authorizer) resolveClaims(identity *auth.Identity) (jwt.MapClaims, error) {
	requestClaims := jwt.MapClaims(identity.Claims)

	if a.primaryUpstreamProvider == "" {
		// Default path: use claims from the original client request's token.
		a.logClaimKeys("token", requestClaims)
		return requestClaims, nil
	}

	// Embedded auth server path: the upstream IDP token is the primary claim source.
	upstreamToken, tokenFound := identity.UpstreamTokens[a.primaryUpstreamProvider]
	if !tokenFound || upstreamToken == "" {
		// The upstream token must be present if the authorizer is configured to use it.
		// Missing token means the session has no upstream credential; deny.
		return nil, fmt.Errorf("upstream token for provider %q not found in identity",
			a.primaryUpstreamProvider)
	}

	upstreamClaims, err := parseUpstreamJWTClaims(upstreamToken)
	if err != nil {
		// Distinguish "not JWT-shaped" (opaque OAuth 2.0 access token —
		// Google's ya29.*, GitHub's gho_*, etc.) from "JWT-shaped but
		// malformed/tampered" (a JWT with three segments that fails to
		// parse). Only fall back for the former; preserve the deny for
		// the latter so a tampered upstream JWT cannot bypass policy.
		//
		// Policies that reference upstream-only claims (groups, hd, custom
		// namespaced claims) see those attributes as absent on this branch and
		// must be authored defensively (`principal has claim_groups && ...`).
		if !looksLikeJWT(upstreamToken) {
			return a.claimsForOpaqueUpstreamToken(identity, requestClaims), nil
		}
		return nil, fmt.Errorf("failed to parse upstream token for provider %q: %w",
			a.primaryUpstreamProvider, err)
	}

	merged := a.supplementFromIDToken(identity, upstreamClaims)
	a.logClaimKeys("upstream", merged)
	return merged, nil
}

// errNoUpstreamIDToken reports that no id_token is stored for the pinned provider,
// so that "nothing to read" and "stored but unparsable" reach one log line.
var errNoUpstreamIDToken = errors.New("no ID token stored for provider")

// claimsForOpaqueUpstreamToken returns the claim set to evaluate when the pinned
// provider's access token is opaque, so it carries no claims of its own.
//
// The same provider's id_token stands in as the claim carrier. Only its `sub` and
// the profileClaimsFromIDToken it asserts are admitted: `sub` because the Cedar
// principal has to come from somewhere and this is the pinned provider's own
// subject, and the profile claims through the same allowlist that governs the
// JWT-access-token path, so both paths admit exactly the same claim names and
// widening the allowlist (see #6049 for group claims) widens them together. The
// id_token's remaining claims stay out: an `iss`/`aud`/`nonce`/`at_hash` describes
// the token rather than the user, and admitting the rest here but not on the JWT
// path would recreate the very inconsistency this resolves.
//
// `exp` is not checked, as elsewhere in this file — see Identity.UpstreamIDTokens.
//
// requestClaims (the ToolHive-issued token's claims) is the last resort for a
// provider with no usable id_token, which is a pure OAuth 2.0 upstream; see
// resolveClaims for why that keeps working rather than failing closed.
func (a *Authorizer) claimsForOpaqueUpstreamToken(identity *auth.Identity, requestClaims jwt.MapClaims) jwt.MapClaims {
	idToken := identity.UpstreamIDTokens[a.primaryUpstreamProvider] // nil map safe in Go

	idTokenClaims, err := func() (jwt.MapClaims, error) {
		if idToken == "" {
			return nil, errNoUpstreamIDToken
		}
		return parseUpstreamJWTClaims(idToken)
	}()
	if err != nil {
		// Shares missingIDTokenLog with supplementFromIDToken: both report that the
		// provider has no usable id_token, and a request takes only one of the two
		// paths, so neither starves the other.
		a.missingIDTokenLog.Do(func() {
			slog.Warn("no usable upstream ID token for provider with an opaque access token; "+
				"falling back to the claims of the ToolHive-issued token, so the Cedar principal is "+
				"ToolHive's internal user ID rather than the upstream subject",
				"provider", a.primaryUpstreamProvider,
				"error", err)
		})
		a.logClaimKeys("token-fallback", requestClaims)
		return requestClaims
	}

	claims := make(jwt.MapClaims, len(profileClaimsFromIDToken)+1)
	// An id_token without `sub` violates OIDC Core 1.0 §2, and leaving the key
	// absent makes AuthorizeWithJWTClaims fail closed with ErrMissingPrincipal
	// rather than pick a principal from the ToolHive-issued token.
	if sub, ok := idTokenClaims[oidcSubjectClaim]; ok {
		claims[oidcSubjectClaim] = sub
	}
	for _, name := range profileClaimsFromIDToken {
		if v, ok := idTokenClaims[name]; ok {
			claims[name] = v
		}
	}

	a.logClaimKeys("upstream-id-token", claims)
	return claims
}

// supplementFromIDToken returns upstreamClaims with the profileClaimsFromIDToken
// it lacks filled in from the primary provider's stored id_token, logging what it
// did. upstreamClaims is not mutated.
//
// A provider with no stored id_token (an OAuth 2.0 upstream that was never asked
// for `openid`, so nothing was captured at login) yields no supplement: the claims
// stay absent and `has`-guarded policies deny. OIDC upstreams always have one —
// the provider rejects a login without it (upstream/oidc.go) and a refresh that
// omits a rotated id_token carries the original forward (upstreamtoken/service.go),
// so it does not vanish mid-session.
func (a *Authorizer) supplementFromIDToken(identity *auth.Identity, upstreamClaims jwt.MapClaims) jwt.MapClaims {
	idToken := identity.UpstreamIDTokens[a.primaryUpstreamProvider] // nil map safe in Go
	if idToken == "" {
		a.missingIDTokenLog.Do(func() {
			slog.Warn("no upstream ID token stored for provider; policies referencing profile claims "+
				"the access token omits will deny",
				"provider", a.primaryUpstreamProvider,
				"profile_claims", profileClaimsFromIDToken)
		})
		return upstreamClaims
	}

	idTokenClaims, err := parseUpstreamJWTClaims(idToken)
	if err != nil {
		// Do not fail the request: the access token parsed, so policies that only
		// reference its claims must keep working. An unparsable id_token means no
		// supplement, which denies exactly the policies that needed it.
		a.missingIDTokenLog.Do(func() {
			slog.Warn("upstream ID token for provider is not a parsable JWT; not supplementing profile claims",
				"provider", a.primaryUpstreamProvider,
				"error", err)
		})
		return upstreamClaims
	}

	merged, filled := supplementProfileClaims(upstreamClaims, idTokenClaims)
	if len(filled) > 0 {
		// Rate-limited on its own timer, not claimKeyLog: sharing one would let
		// this Warn consume the window and permanently starve the claim-key dump
		// in resolveClaims, which is what you want when debugging a denying policy.
		//
		// Claim keys only — never claim values, which hold user PII.
		a.supplementLog.Do(func() {
			slog.Warn("upstream access token lacks profile claims; using the upstream ID token's values "+
				"for Cedar evaluation",
				"provider", a.primaryUpstreamProvider,
				"claims", filled)
		})
	}
	return merged
}

// OIDC Core 1.0 claim names — `sub` from §2, the profile claims from §5.1 —
// declared here rather than borrowed from another package. What this code reads is
// an upstream provider's id_token, so these are wire names fixed by the OIDC spec,
// not names ToolHive chooses. Anchoring them to a ToolHive constant would invert
// the dependency: renaming that constant would silently change which claim Cedar
// reads out of a third party's token. Three literals also do not justify pulling
// an authorization-server dependency tree into the authorizer.
//
// oidcSubjectClaim is deliberately NOT in profileClaimsFromIDToken: it is never
// supplemented into a claim set an access token already produced. It is read from
// the id_token only when the access token is opaque and the id_token is therefore
// the whole claim source — see claimsForOpaqueUpstreamToken.
const (
	oidcSubjectClaim = "sub"
	oidcNameClaim    = "name"
	oidcEmailClaim   = "email"
)

// profileClaimsFromIDToken are the OIDC Core §5.1 profile claims that may be read
// from the primary provider's id_token when its access token omits them. They
// coincide with the two claims the embedded auth server extracts from that same
// id_token: pkg/authserver/upstream/oidc.go reads them by their OIDC names, and the
// callback handler then passes them to session.New
// (pkg/authserver/server/handlers/callback.go). Both sides key off the wire names
// independently — a shared reliance on the spec, not a coupling. If the auth server
// ever mirrors more, adding it here is a separate, deliberate decision.
//
// Widening this list widens what can satisfy a policy — see resolveClaims first.
var profileClaimsFromIDToken = []string{oidcNameClaim, oidcEmailClaim}

// supplementProfileClaims returns a fresh claim set holding every access-token
// claim plus, for each name in profileClaimsFromIDToken that the access token does
// not carry, the id_token's value for that name. It also returns the sorted list of
// names actually supplied by the id_token, for logging.
//
// Neither input is mutated and the result aliases neither, so the caller may hand
// it to code that adds synthetic keys. Absent claims are never fabricated: a name
// missing from both tokens is missing from the result, which keeps `has`-guarded
// policies failing closed.
func supplementProfileClaims(accessTokenClaims, idTokenClaims jwt.MapClaims) (merged jwt.MapClaims, filled []string) {
	merged = make(jwt.MapClaims, len(accessTokenClaims)+len(profileClaimsFromIDToken))
	for k, v := range accessTokenClaims {
		merged[k] = v
	}

	for _, name := range profileClaimsFromIDToken {
		if _, ok := merged[name]; ok {
			// The access token asserted this claim; it always wins.
			continue
		}
		v, ok := idTokenClaims[name]
		if !ok {
			continue
		}
		merged[name] = v
		filled = append(filled, name)
	}

	// Sorted for a canonical, easily-greppable log line — profileClaimsFromIDToken
	// is ordered for readability (name, email), not alphabetically.
	slices.Sort(filled)
	return merged, filled
}

// looksLikeJWT returns true when the token has the three-segment shape of a
// JOSE-compact-serialized JWT (`header.payload.signature`). It does not
// validate the contents; the parser handles that.
func looksLikeJWT(tokenStr string) bool {
	return strings.Count(tokenStr, ".") == 2
}

// logClaimKeys emits a rate-limited DEBUG log listing the JWT claim keys
// available for Cedar policy evaluation.
func (a *Authorizer) logClaimKeys(source string, claims jwt.MapClaims) {
	a.claimKeyLog.Do(func() {
		keys := make([]string, 0, len(claims))
		for k := range claims {
			keys = append(keys, k)
		}
		slog.Debug("Resolved JWT claim keys for Cedar evaluation",
			"source", source,
			"keys", keys)
	})
}

// parseUpstreamJWTClaims parses JWT claims from an upstream access token without
// verifying the signature. The token was already validated by the upstream IDP
// during the OAuth 2.0 code exchange; we only need its claims for Cedar evaluation.
// Returns an error if the token is not a parseable JWT (e.g. opaque token).
func parseUpstreamJWTClaims(tokenStr string) (jwt.MapClaims, error) {
	parser := jwt.NewParser()
	token, _, err := parser.ParseUnverified(tokenStr, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("upstream token is not a parseable JWT: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("upstream token has unexpected claims type")
	}
	return claims, nil
}

// extractClientIDFromClaims extracts the client ID from JWT claims.
// By default, it uses the "sub" (subject) claim as the client ID.
// This can be customized based on your JWT token structure.
func extractClientIDFromClaims(claims jwt.MapClaims) (string, bool) {
	// Use the GetSubject method to safely extract the "sub" claim
	sub, err := claims.GetSubject()
	if err != nil || sub == "" {
		return "", false
	}

	return sub, true
}

// preprocessClaims adds a "claim_" prefix to all claim keys.
// This makes it clear which values are from the JWT claims.
func preprocessClaims(claims jwt.MapClaims) map[string]interface{} {
	preprocessed := make(map[string]interface{})
	for k, v := range claims {
		claimKey := claimPrefix + k
		preprocessed[claimKey] = v
	}
	return preprocessed
}

// normalizeMultiValuedClaims returns claims with every claim named in names
// and present in claims rewritten to a canonical unpadded space-delimited
// string. This lets a single Cedar `like`/`==` policy match a claim regardless
// of whether the IdP emitted it as a space-delimited string or a JSON array.
// See ConfigOptions.MultiValuedClaims for the full rationale.
//
// When names is empty the input is returned directly (no copy); otherwise a
// shallow copy is returned and the input is never mutated.
//
//   - array ([]interface{} or []string): elements joined with single spaces.
//     Non-string elements are stringified with fmt.Sprint rather than
//     dropped; nested array/object elements are skipped with a slog.Debug
//     since they have no scalar string form.
//   - string: passthrough, completely unchanged (not trimmed).
//   - empty array, or an array with no usable elements: "" (empty string —
//     present but empty).
//   - claim absent from claims, or names empty: left untouched (no key
//     fabricated).
//
// Only top-level claim keys are matched; there is no dot-notation support.
// claims is not mutated.
func normalizeMultiValuedClaims(claims jwt.MapClaims, names []string) jwt.MapClaims {
	if len(names) == 0 {
		// Nothing to normalize. Return the input directly — the sole caller
		// (preprocessClaims) only reads it, so no defensive copy is needed and
		// the opt-in-empty path pays no allocation.
		return claims
	}

	out := make(jwt.MapClaims, len(claims))
	for k, v := range claims {
		out[k] = v
	}

	for _, name := range names {
		v, ok := claims[name]
		if !ok {
			continue
		}

		switch val := v.(type) {
		case string:
			// Verbatim passthrough: out[name] already equals val (out is a copy
			// of claims), so this is intentionally a no-op. The explicit branch
			// documents the string contract and, importantly, keeps a string
			// value out of the default "unrecognized type" log below. This is the
			// one place the string handling diverges from addMultiValuedClaimSets,
			// which collapses whitespace via strings.Fields for the Set form.
			out[name] = val
		case []interface{}:
			out[name] = strings.Join(multiValuedTokens(name, val), " ")
		case []string:
			out[name] = strings.Join(val, " ")
		default:
			slog.Debug("multi-valued claim has unrecognized type, leaving unchanged",
				"claim", name, "type", fmt.Sprintf("%T", v))
		}
	}

	return out
}

// addMultiValuedClaimSets adds a bare "claimset_<name>" key to processed for every
// claim named in names and present in raw, holding the claim's elements as a
// []string (converted to a Cedar Set by convertToCedarValueAtDepth). This is
// the companion to normalizeMultiValuedClaims's "claim_<name>" string form:
// where "claim_<name>" supports substring-style `like` matching, "claimset_<name>"
// supports exact-element `.contains`/`.containsAll`/`.containsAny` matching.
// The "claimset_" prefix is reserved and cannot collide with a JWT claim, since all
// JWT claims are "claim_"-prefixed by preprocessClaims.
//
// processed must already have the "claim_" prefix applied (i.e. this must run
// after preprocessClaims) so the added key stays bare. raw is not mutated.
func addMultiValuedClaimSets(processed map[string]interface{}, raw jwt.MapClaims, names []string) {
	for _, name := range names {
		v, ok := raw[name]
		if !ok {
			continue
		}

		// The array path shares multiValuedTokens with normalizeMultiValuedClaims
		// (string-only elements) so the Set and the joined string stay in sync.
		// The string path deliberately differs: normalize passes the string
		// through verbatim, whereas here strings.Fields splits it into exact
		// elements (collapsing irregular whitespace) — the two cannot share one
		// coercion for that reason.
		switch val := v.(type) {
		case string:
			processed[claimSetPrefix+name] = strings.Fields(val)
		case []interface{}:
			processed[claimSetPrefix+name] = multiValuedTokens(name, val)
		case []string:
			processed[claimSetPrefix+name] = slices.Clone(val)
		default:
			slog.Debug("multi-valued claim has unrecognized type, omitting claimset set",
				"claim", name, "type", fmt.Sprintf("%T", v))
		}
	}
}

// multiValuedTokens extracts the string elements of a []interface{} claim value
// for normalizeMultiValuedClaims and addMultiValuedClaimSets. Only string
// elements are kept: OAuth scope tokens are strings (RFC 6749 §3.3), so a
// non-string element (number, bool, null, or a nested array/object) is
// malformed and is skipped with a slog.Debug rather than stringified. Skipping
// (a) avoids spurious tokens like "<nil>" or "1e+06" polluting both claim
// forms, and (b) keeps the array shape consistent with the string shape, whose
// strings.Fields path likewise yields only real string tokens.
func multiValuedTokens(claimName string, elems []interface{}) []string {
	tokens := make([]string, 0, len(elems))
	for _, elem := range elems {
		s, ok := elem.(string)
		if !ok {
			slog.Debug("multi-valued claim element is not a string, skipping",
				"claim", claimName, "type", fmt.Sprintf("%T", elem))
			continue
		}
		tokens = append(tokens, s)
	}
	return tokens
}

// preprocessArguments adds an "arg_" prefix to all argument keys.
// For complex types, it just notes their presence with an "_present" suffix.
func preprocessArguments(arguments map[string]interface{}) map[string]interface{} {
	if arguments == nil {
		return nil
	}

	preprocessed := make(map[string]interface{})
	for k, v := range arguments {
		argKey := fmt.Sprintf("arg_%s", k)
		switch val := v.(type) {
		case string, bool, int, int64, float64:
			preprocessed[argKey] = val
		default:
			// For complex types, just note their presence
			preprocessed[argKey+"_present"] = true
		}
	}
	return preprocessed
}

// mergeContexts merges multiple context maps into a single map.
// Later maps override earlier maps if there are key conflicts.
func mergeContexts(contextMaps ...map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{})
	for _, ctxMap := range contextMaps {
		if ctxMap == nil {
			continue
		}
		for k, v := range ctxMap {
			merged[k] = v
		}
	}
	return merged
}

// authorizeToolCall authorizes a tool call operation.
// This method is used when a client tries to call a specific tool.
// It checks if the client is authorized to call the tool with the given context.
// Tool annotations from the context (if present) are included as resource entity
// attributes so Cedar policies can reference them (e.g. resource.readOnlyHint).
func (a *Authorizer) authorizeToolCall(
	ctx context.Context,
	clientID, toolName string,
	claimsMap map[string]interface{},
	attrsMap map[string]interface{},
	groups []string,
) (bool, error) {
	// Extract principal from client ID
	principal := fmt.Sprintf("Client::%s", clientID)

	// Action is to call a tool
	action := "Action::call_tool"

	// Resource is the tool being called
	resource := fmt.Sprintf("Tool::%s", toolName)

	// Read tool annotations from context and include in resource attributes.
	// Annotations are merged first so that the standard attributes ("name",
	// "operation", "feature") always take precedence and cannot be overwritten
	// by annotation keys — intentionally or accidentally.
	annotationAttrs := authorizers.AnnotationsToMap(authorizers.ToolAnnotationsFromContext(ctx))

	// Create attributes for the entities
	attributes := mergeContexts(annotationAttrs, attrsMap, map[string]interface{}{
		"name":      toolName,
		"operation": "call",
		"feature":   "tool",
	})

	// Create Cedar entities
	entities, err := a.entityFactory.CreateEntitiesForRequest(
		principal, action, resource, claimsMap, attributes, groups, a.serverName,
	)
	if err != nil {
		return false, fmt.Errorf("failed to create Cedar entities: %w", err)
	}

	contextMap := mergeContexts(claimsMap, attrsMap)

	// Check authorization with entities
	return a.IsAuthorized(principal, action, resource, contextMap, entities)
}

// authorizePromptGet authorizes a prompt get operation.
// This method is used when a client tries to get a specific prompt.
// It checks if the client is authorized to access the prompt with the given context.
func (a *Authorizer) authorizePromptGet(
	clientID, promptName string,
	claimsMap map[string]interface{},
	attrsMap map[string]interface{},
	groups []string,
) (bool, error) {
	// Extract principal from client ID
	principal := fmt.Sprintf("Client::%s", clientID)

	// Action is to get a prompt
	action := "Action::get_prompt"

	// Resource is the prompt being accessed
	resource := fmt.Sprintf("Prompt::%s", promptName)

	// Create attributes for the entities
	attributes := mergeContexts(map[string]interface{}{
		"name":      promptName,
		"operation": "get",
		"feature":   "prompt",
	}, attrsMap)

	// Create Cedar entities
	entities, err := a.entityFactory.CreateEntitiesForRequest(
		principal, action, resource, claimsMap, attributes, groups, a.serverName,
	)
	if err != nil {
		return false, fmt.Errorf("failed to create Cedar entities: %w", err)
	}

	contextMap := mergeContexts(claimsMap, attrsMap)

	// Check authorization with entities
	return a.IsAuthorized(principal, action, resource, contextMap, entities)
}

// authorizeResourceRead authorizes a resource read operation.
// This method is used when a client tries to read a specific resource.
// It checks if the client is authorized to read the resource.
func (a *Authorizer) authorizeResourceRead(
	clientID, resourceURI string,
	claimsMap map[string]interface{},
	attrsMap map[string]interface{},
	groups []string,
) (bool, error) {
	// Extract principal from client ID
	principal := fmt.Sprintf("Client::%s", clientID)

	// Action is to read a resource
	action := "Action::read_resource"

	// Resource is the resource being accessed
	// Use the URI as the resource ID, but sanitize it for Cedar
	sanitizedURI := sanitizeURIForCedar(resourceURI)
	resource := fmt.Sprintf("Resource::%s", sanitizedURI)

	// Create attributes for the entities
	attributes := mergeContexts(map[string]interface{}{
		"name":      resourceURI,
		"uri":       resourceURI,
		"operation": "read",
		"feature":   "resource",
	}, attrsMap)

	// Create Cedar entities
	entities, err := a.entityFactory.CreateEntitiesForRequest(
		principal, action, resource, claimsMap, attributes, groups, a.serverName,
	)
	if err != nil {
		return false, fmt.Errorf("failed to create Cedar entities: %w", err)
	}

	contextMap := mergeContexts(claimsMap, attrsMap)

	// Check authorization with entities
	return a.IsAuthorized(principal, action, resource, contextMap, entities)
}

// authorizeFeatureList authorizes a list operation for a feature.
// This method is used when a client tries to list available tools, prompts, or resources.
// It checks if the client is authorized to list the specified feature type.
func (a *Authorizer) authorizeFeatureList(
	clientID string,
	feature authorizers.MCPFeature,
	claimsMap map[string]interface{},
	attrsMap map[string]interface{},
	groups []string,
) (bool, error) {
	// Extract principal from client ID
	principal := fmt.Sprintf("Client::%s", clientID)

	// Action is to list a feature
	action := fmt.Sprintf("Action::list_%ss", feature)

	// Resource is the feature type
	resource := fmt.Sprintf("FeatureType::%s", feature)

	// Create attributes for the entities
	attributes := mergeContexts(map[string]interface{}{
		"type":      string(feature),
		"operation": "list",
		"feature":   string(feature),
	}, attrsMap)

	// Create Cedar entities
	entities, err := a.entityFactory.CreateEntitiesForRequest(
		principal, action, resource, claimsMap, attributes, groups, a.serverName,
	)
	if err != nil {
		return false, fmt.Errorf("failed to create Cedar entities: %w", err)
	}

	contextMap := mergeContexts(claimsMap, attrsMap)

	// Check authorization with entities
	return a.IsAuthorized(principal, action, resource, contextMap, entities)
}

// parseCedarEntityID parses a Cedar entity ID in the format "Type::ID".
// It returns the type and ID parts, or an error if the format is invalid.
func parseCedarEntityID(entityID string) (string, string, error) {
	parts := strings.SplitN(entityID, "::", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid entity ID format: %s", entityID)
	}
	return parts[0], parts[1], nil
}

// sanitizeURIForCedar sanitizes a URI for use in Cedar policies.
// Cedar entity IDs have restrictions on characters, so we need to sanitize the URI.
func sanitizeURIForCedar(uri string) string {
	// Replace characters that are not allowed in Cedar entity IDs
	// This is a simple implementation - you may need to enhance it based on your needs
	replacer := strings.NewReplacer(
		":", "_",
		"/", "_",
		"\\", "_",
		"?", "_",
		"&", "_",
		"=", "_",
		"#", "_",
		" ", "_",
		".", "_",
	)
	return replacer.Replace(uri)
}

// AuthorizeWithJWTClaims demonstrates how to use JWT claims with the Cedar authorization middleware.
// This method:
// 1. Extracts JWT claims from the context
// 2. Extracts the client ID from the claims
// 3. Includes the JWT claims in the Cedar context
// 4. Creates entities with appropriate attributes
// 5. Authorizes the operation using the client ID and claims
func (a *Authorizer) AuthorizeWithJWTClaims(
	ctx context.Context,
	feature authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	resourceID string,
	arguments map[string]interface{},
) (bool, error) {
	// Extract Identity from the context
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return false, ErrMissingPrincipal
	}

	// Resolve the claims source: upstream IDP token or the original request's token.
	resolvedClaims, err := a.resolveClaims(identity)
	if err != nil {
		return false, err
	}

	// Extract client ID from the resolved claims.
	clientID, ok := extractClientIDFromClaims(resolvedClaims)
	if !ok {
		return false, ErrMissingPrincipal
	}

	// Extract groups from the group claim (or well-known defaults) and the
	// role claim, merge, and dedup. Both claim sources map to the configured
	// group entity type (default "THVGroup"). Extraction runs BEFORE
	// preprocessClaims so the raw claim
	// values are available.
	// The identity pointer is not mutated here because Identity MUST NOT be
	// modified after it is placed in the request context (concurrent reads).
	groupClaims := extractGroups(resolvedClaims, a.groupClaimName)
	if groupClaims == nil {
		// Fall back to well-known claim names. This covers two cases:
		// 1. No GroupClaimName configured — backward compatible default.
		// 2. GroupClaimName configured but absent from the token — the
		//    documented contract says the custom name takes *priority*
		//    over defaults, not that it replaces them.
		for _, name := range defaultGroupClaimNames {
			if groupClaims = extractGroups(resolvedClaims, name); groupClaims != nil {
				break
			}
		}
	}
	groups := dedup(append(
		groupClaims,
		extractGroups(resolvedClaims, a.roleClaimName)...,
	))

	// Preprocess claims and arguments. Multi-valued claim normalization runs
	// after group/role extraction (which needs the raw claim shapes) and
	// before the "claim_" prefix is applied.
	normalizedClaims := normalizeMultiValuedClaims(resolvedClaims, a.multiValuedClaims)
	processedClaims := preprocessClaims(normalizedClaims)
	addMultiValuedClaimSets(processedClaims, resolvedClaims, a.multiValuedClaims)
	processedArgs := preprocessArguments(arguments)

	// Authorize based on the feature and operation
	switch {
	case feature == authorizers.MCPFeatureTool && operation == authorizers.MCPOperationCall:
		return a.authorizeToolCall(ctx, clientID, resourceID, processedClaims, processedArgs, groups)

	case feature == authorizers.MCPFeaturePrompt && operation == authorizers.MCPOperationGet:
		return a.authorizePromptGet(clientID, resourceID, processedClaims, processedArgs, groups)

	case feature == authorizers.MCPFeatureResource && operation == authorizers.MCPOperationRead:
		return a.authorizeResourceRead(clientID, resourceID, processedClaims, processedArgs, groups)

	case operation == authorizers.MCPOperationList:
		return a.authorizeFeatureList(clientID, feature, processedClaims, processedArgs, groups)

	default:
		return false, fmt.Errorf("unsupported feature/operation combination: %s/%s", feature, operation)
	}
}

// defaultGroupClaimNames lists common group claim names across popular identity
// providers. They are checked in order; the first non-empty match is returned.
//
// Sources:
//   - "groups"         — Microsoft Entra ID, Okta, Auth0, PingIdentity.
//   - "roles"          — Keycloak (realm_access.roles flattened to top-level).
//   - "cognito:groups" — AWS Cognito user pools.
var defaultGroupClaimNames = []string{"groups", "roles", "cognito:groups"}

// resolveNestedClaim resolves a claim value from JWT claims, supporting both
// top-level keys and dot-separated nested paths.
//
// Resolution order:
//  1. Exact top-level match — handles Auth0 / Okta URL-style claim names
//     (e.g. "https://myapp.example.com/roles") that contain dots but are
//     top-level keys in the JWT.
//  2. Dot-notation traversal — handles Keycloak-style nested claims
//     (e.g. "realm_access.roles" → claims["realm_access"]["roles"]).
//
// Returns nil when the claim is absent or traversal hits a non-map value.
func resolveNestedClaim(claims jwt.MapClaims, path string) interface{} {
	if path == "" {
		return nil
	}

	// 1. Exact top-level match (handles Auth0 URL claims with dots).
	if val, ok := claims[path]; ok {
		return val
	}

	// 2. Dot-notation traversal.
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return nil // single segment already tried above
	}

	var current interface{} = map[string]interface{}(claims)
	for _, segment := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current, ok = m[segment]
		if !ok {
			return nil
		}
	}
	return current
}

// extractGroups extracts group/role names from a specific JWT claim.
// It resolves the claim via resolveNestedClaim (supporting both flat and
// dot-notation paths) and coerces the value to []string.
//
// Return value distinguishes "claim absent" from "claim present but empty"
// so callers can decide whether to fall back to defaults:
//   - nil: claimName is empty, the claim is absent, or the value has an
//     unsupported scalar/object type (e.g. string, number).
//   - non-nil, possibly empty: the claim is an array. Non-string elements
//     are silently dropped, so an array of all non-strings yields an empty
//     slice (not nil). A genuinely empty array (`[]`) also yields an empty
//     slice. Both cases mean "the IdP said this claim exists with no usable
//     group names" and suppress fallback.
func extractGroups(claims jwt.MapClaims, claimName string) []string {
	if claimName == "" {
		return nil
	}

	val := resolveNestedClaim(claims, claimName)
	if val == nil {
		return nil
	}

	switch v := val.(type) {
	case []interface{}:
		groups := make([]string, 0, len(v))
		for _, g := range v {
			if s, ok := g.(string); ok {
				groups = append(groups, s)
			}
		}
		return groups
	case []string:
		return v
	default:
		slog.Warn("group/role claim has unrecognized type, ignoring",
			"claim", claimName, "type", fmt.Sprintf("%T", val))
		return nil
	}
}

// dedup removes duplicate strings while preserving first-occurrence order.
// Returns nil when the input is nil (not an empty slice) so callers can
// distinguish "no groups" from "empty groups".
func dedup(groups []string) []string {
	if groups == nil {
		return nil
	}

	seen := make(map[string]struct{}, len(groups))
	result := make([]string, 0, len(groups))
	for _, g := range groups {
		if _, exists := seen[g]; exists {
			continue
		}
		seen[g] = struct{}{}
		result = append(result, g)
	}
	return result
}
