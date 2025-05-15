package sigstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	urllib "net/url"
	"path"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// AuthMethod is a function that takes a pointer to containerAuth and modifies it
type AuthMethod func(auth *containerAuth)

// GitHubClient is the interface for the GitHub client
type GitHubClient interface {
	// NewRequest creates an HTTP request.
	NewRequest(method, url string, body any) (*http.Request, error)
	// Do executes an HTTP request.
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
	// GetCredential returns the credential used to authenticate with GitHub.
	GetCredential() GitHubCredential
}

// GitHubCredential is the interface for credentials used when interacting with GitHub
type GitHubCredential interface {
	GetAsContainerAuthenticator(owner string) authn.Authenticator
}

// Attestation is the attestation from the GitHub attestation endpoint
type Attestation struct {
	Bundle json.RawMessage `json:"bundle"`
}

// AttestationReply is the reply from the GitHub attestation endpoint
type AttestationReply struct {
	Attestations []Attestation `json:"attestations"`
}

var (
	// ErrProvenanceNotFoundOrIncomplete is returned when there's no provenance info (missing .sig or attestation) or
	// has incomplete data
	ErrProvenanceNotFoundOrIncomplete = errors.New("provenance not found or incomplete")

	// MaxAttestationsBytesLimit is the maximum number of bytes we're willing to read from the attestation endpoint
	// We'll limit this to 10mb for now
	MaxAttestationsBytesLimit int64 = 10 * 1024 * 1024
)

const (
	sigstoreBundleMediaType01 = "application/vnd.dev.sigstore.bundle+json;version=0.1"
)

type sigstoreBundle struct {
	bundle      *bundle.Bundle
	digestBytes []byte
	digestAlgo  string
}

type containerAuth struct {
	// Used if GH client is available
	ghClient GitHubClient
	// Used if GH client is not available (any other provider)
	concreteAuthn authn.Authenticator
	// Registry to use
	registry string
}

func (c *containerAuth) getAuthenticator(owner string) authn.Authenticator {
	if c.ghClient != nil {
		return c.ghClient.GetCredential().GetAsContainerAuthenticator(owner)
	}
	if c.concreteAuthn != nil {
		return c.concreteAuthn
	}
	return authn.Anonymous
}

func (c *containerAuth) getRegistry() string {
	return c.registry
}

// getSigstoreBundles returns the sigstore bundles, either through the OCI registry or the GitHub attestation endpoint
func getSigstoreBundles(
	ctx context.Context,
	imageRef string,
) ([]sigstoreBundle, error) {
	// Try to build a bundle from the OCI image reference
	bundles, err := bundleFromOCIImage(ctx, imageRef, authn.Anonymous)
	if err != nil {
		return nil, err
	}
	//if errors.Is(err, ErrProvenanceNotFoundOrIncomplete) && auth.ghClient != nil {
	//	// If we failed to find the signature in the OCI image, try to build a bundle from the GitHub attestation endpoint
	//	return bundleFromGHAttestationEndpoint(ctx, auth.ghClient, imageRef)
	//} else if err != nil {
	//	return nil, fmt.Errorf("error getting bundle from OCI image: %w", err)
	//}
	// We either got an unexpected error or successfully built a bundle from the OCI image
	return bundles, nil
}

// BuildImageRef returns the OCI image reference
func BuildImageRef(registry, owner, artifact, checksum string) string {
	return fmt.Sprintf("%s/%s/%s@%s", registry, owner, artifact, checksum)
}

// NewContainerAuth creates a new containerAuth object
func NewContainerAuth(authOpts ...AuthMethod) *containerAuth {
	auth := containerAuth{
		registry: "ghcr.io",
	}
	for _, opt := range authOpts {
		opt(&auth)
	}
	return &auth
}

// SanitizeInput sanitizes the input parameters (can't be upper-cased)
func SanitizeInput(owner *string) {
	*owner = strings.ToLower(*owner)
}

func getSigstoreOptions(sigstoreTUFRepoURL string) (*tuf.Options, []verify.VerifierOption, error) {
	// Default the sigstoreTUFRepoURL to the sigstore public trusted root repo if not provided
	if sigstoreTUFRepoURL == "" {
		sigstoreTUFRepoURL = TrustedRootSigstorePublicGoodInstance
	}

	// Get the Sigstore TUF client options
	tufOpts, err := getTUFOptions(sigstoreTUFRepoURL)
	if err != nil {
		return nil, nil, err
	}

	// Get the Sigstore verifier options
	opts, err := verifierOptions(sigstoreTUFRepoURL)
	if err != nil {
		return nil, nil, err
	}

	// All good
	return tufOpts, opts, nil
}

func getTUFOptions(sigstoreTUFRepoURL string) (*tuf.Options, error) {
	// Default the TUF options
	tufOpts := tuf.DefaultOptions()
	tufOpts.DisableLocalCache = true

	// Set the repository base URL, fix the scheme if not provided
	tufURL, err := urllib.Parse(sigstoreTUFRepoURL)
	if err != nil {
		return nil, fmt.Errorf("error parsing sigstore TUF repo URL: %w", err)
	}
	if tufURL.Scheme == "" {
		tufURL.Scheme = "https"
	}
	tufOpts.RepositoryBaseURL = tufURL.String()

	// sigstore-go has a copy of the root.json for the public sigstore instance embedded. Nothing to do.
	if sigstoreTUFRepoURL != TrustedRootSigstorePublicGoodInstance {
		// Look up and set the embedded root.json for the given TUF repository
		rootJson, err := embeddedRootJson(sigstoreTUFRepoURL)
		if err != nil {
			return nil, fmt.Errorf("error getting embedded root.json for %s: %w", sigstoreTUFRepoURL, err)
		}
		tufOpts.Root = rootJson
	}

	// All good
	return tufOpts, nil
}

func embeddedRootJson(tufRootURL string) ([]byte, error) {
	embeddedRootPath := path.Join("tufroots", tufRootURL, rootTUFPath)
	return embeddedTufRoots.ReadFile(embeddedRootPath)
}

func verifierOptions(trustedRoot string) ([]verify.VerifierOption, error) {
	switch trustedRoot {
	case TrustedRootSigstorePublicGoodInstance:
		return []verify.VerifierOption{
			verify.WithSignedCertificateTimestamps(1),
			verify.WithTransparencyLog(1),
			verify.WithObserverTimestamps(1),
		}, nil
	case TrustedRootSigstoreGitHub:
		return []verify.VerifierOption{
			verify.WithObserverTimestamps(1),
		}, nil
	}
	return nil, fmt.Errorf("unknown trusted root: %s", trustedRoot)
}

// Result is the result of the verification
type Result struct {
	IsSigned   bool `json:"is_signed"`
	IsVerified bool `json:"is_verified"`
	verify.VerificationResult
}

// getVerifiedResults verifies the artifact using the bundles against the configured sigstore instance
// and returns the extracted metadata that we need for ingestion
func getVerifiedResults(
	_ context.Context,
	sev *verify.SignedEntityVerifier,
	bundles []sigstoreBundle,
) []Result {
	var results []Result

	// Verify each bundle we've constructed
	for _, b := range bundles {
		// Create a new verification result - IsVerified and IsSigned flags are set explicitly for better visibility.
		// At this point, we managed to extract a bundle, so we can set the IsSigned flag to true
		// This doesn't mean the bundle is verified though, just that it exists
		res := Result{
			IsSigned:   true,
			IsVerified: false,
		}

		// Verify the artifact using the bundle
		// Note that we verify the identity in the next step (evaluation) where we check it against what was set by the
		// user in their Minder profile (e.g., repository, cert. issuer, etc.)
		verificationResult, err := sev.Verify(b.bundle, verify.NewPolicy(
			verify.WithArtifactDigest(b.digestAlgo, b.digestBytes),
			verify.WithoutIdentitiesUnsafe(),
		))
		if err != nil {
			// The bundle we provided failed verification
			// Log the error and continue to the next bundle, this one is considered signed but not verified
			//logger.Err(err).Msg("error verifying bundle")
			results = append(results, res)
			continue
		}

		// We've successfully verified and extracted the artifact provenance information
		res.IsVerified = true
		res.VerificationResult = *verificationResult
		results = append(results, res)
	}
	// Return the results
	return results
}
