package sigstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	containerdigest "github.com/opencontainers/go-digest"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/stacklok/toolhive/pkg/logger"
)

func bundleFromGHAttestationEndpoint(
	ctx context.Context, ghCli GitHubClient, imageRef string,
) ([]sigstoreBundle, error) {
	// TODO: extract the owner and the checksumref from the image name
	var owner, checksumref string

	// Get the attestation reply from the GitHub attestation endpoint
	attestationReply, err := getAttestationReply(ctx, ghCli, owner, checksumref)
	if err != nil {
		return nil, fmt.Errorf("error getting attestation reply: %w", err)
	}

	var bundles []sigstoreBundle
	// Loop through all available attestations and extract the bundle and the certificate identity information
	for _, att := range attestationReply.Attestations {
		protobufBundle, err := unmarhsalAttestationReply(&att)
		if err != nil {
			logger.Error("error unmarshalling attestation reply")
			continue
		}

		digest, err := getDigestFromVersion(checksumref)
		if err != nil {
			logger.Error("error getting digest from version")
			continue
		}

		// Store the bundle and the certificate identity we extracted from the attestation
		bundles = append(bundles, sigstoreBundle{
			bundle:      protobufBundle,
			digestBytes: digest,
			digestAlgo:  containerdigest.Canonical.String(),
		})
	}

	// There's no available provenance information about this image if we failed to find valid bundles from the attestations list
	if len(bundles) == 0 {
		return nil, ErrProvenanceNotFoundOrIncomplete
	}

	// Return the bundles
	return bundles, nil
}

func getAttestationReply(
	ctx context.Context,
	ghCli GitHubClient,
	owner, checksumref string) (*AttestationReply, error) {
	if ghCli == nil {
		return nil, fmt.Errorf("no github client available")
	}

	url := fmt.Sprintf("orgs/%s/attestations/%s", owner, checksumref)
	req, err := ghCli.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	resp, err := ghCli.Do(ctx, req)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrProvenanceNotFoundOrIncomplete, err.Error())
		}
		return nil, fmt.Errorf("error doing request: %w", err)
	}
	defer resp.Body.Close()

	lr := io.LimitReader(resp.Body, MaxAttestationsBytesLimit)
	var attestationReply AttestationReply
	if err := json.NewDecoder(lr).Decode(&attestationReply); err != nil {
		return nil, fmt.Errorf("error decoding response: %w", err)
	}

	return &attestationReply, nil
}

func unmarhsalAttestationReply(attestation *Attestation) (*bundle.Bundle, error) {
	var pbBundle protobundle.Bundle
	if err := protojson.Unmarshal(attestation.Bundle, &pbBundle); err != nil {
		return nil, fmt.Errorf("error unmarshaling attestation: %w", err)
	}

	protobufBundle, err := bundle.NewBundle(&pbBundle)
	if err != nil {
		return nil, fmt.Errorf("error creating protobuf bundle: %w", err)
	}

	return protobufBundle, nil
}

func getDigestFromVersion(version string) ([]byte, error) {
	algoPrefix := containerdigest.Canonical.String() + ":"
	if !strings.HasPrefix(version, algoPrefix) {
		// TODO: support other digest algorithms?
		return nil, fmt.Errorf("expected digest to start with %s", algoPrefix)
	}

	stringDigest := strings.TrimPrefix(version, algoPrefix)
	if err := containerdigest.Canonical.Validate(stringDigest); err != nil {
		return nil, fmt.Errorf("error validating digest: %w", err)
	}

	digest, err := hex.DecodeString(stringDigest)
	if err != nil {
		return nil, fmt.Errorf("error decoding digest: %w", err)
	}

	return digest, nil
}
