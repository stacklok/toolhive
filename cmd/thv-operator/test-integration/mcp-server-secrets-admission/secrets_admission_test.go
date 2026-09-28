// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Admission-level regression tests for MCPServer spec.secrets list-map keys
// (#6686): the secrets list is x-kubernetes-list-type: map keyed on
// (name, key), so two entries referencing different keys of the SAME Secret
// must be admitted, while an exact (name, key) duplicate must still be
// rejected. The Go-level Validate tests cannot see this — it is enforced by
// the API server against the generated CRDs, which this suite installs from
// deploy/charts/operator-crds/files/crds: the exact files the chart ships.
package secretsadmission

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

var k8sClient client.Client

func TestMain(m *testing.M) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "..", "deploy", "charts", "operator-crds", "files", "crds"),
		},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		os.Exit(1)
	}

	sch := runtime.NewScheme()
	for _, add := range []func(s *runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		mcpv1alpha1.AddToScheme,
		mcpv1beta1.AddToScheme,
	} {
		if err := add(sch); err != nil {
			fmt.Fprintf(os.Stderr, "registering scheme: %v\n", err)
			os.Exit(1)
		}
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating client: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := testEnv.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stopping envtest: %v\n", err)
	}
	os.Exit(code)
}

// twoKeysOfOneSecret is the #6686 shape: both entries reference the SAME
// Secret via name, distinguishing only by key.
func twoKeysOfOneSecret() []mcpv1beta1.SecretRef {
	return []mcpv1beta1.SecretRef{
		{Name: "shared-secret", Key: "username", TargetEnvName: "MCP_USERNAME"},
		{Name: "shared-secret", Key: "password", TargetEnvName: "MCP_PASSWORD"},
	}
}

// TestMCPServerCRDSecretsListMapKeys pins the installed CRD schema itself, so
// these tests provably run against the chart-shipped files and not some other
// schema source.
func TestMCPServerCRDSecretsListMapKeys(t *testing.T) {
	t.Parallel()

	crd := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, k8sClient.Get(context.Background(),
		client.ObjectKey{Name: "mcpservers.toolhive.stacklok.dev"}, crd))

	for _, ver := range crd.Spec.Versions {
		secrets := ver.Schema.OpenAPIV3Schema.Properties["spec"].Properties["secrets"]
		assert.Equal(t, []string{"name", "key"}, secrets.XListMapKeys,
			"version %s: secrets list-map keys must be (name, key)", ver.Name)
		require.NotNil(t, secrets.XListType)
		assert.Equal(t, "map", *secrets.XListType,
			"version %s: secrets list type must stay map", ver.Name)
	}
}

func TestMCPServerV1Beta1CreateAdmitsTwoKeysOfOneSecret(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "secrets-two-keys-create", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "example/mcp-server:latest",
			Transport: "stdio",
			Secrets:   twoKeysOfOneSecret(),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, srv))

	stored := &mcpv1beta1.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(srv), stored))
	require.Len(t, stored.Spec.Secrets, 2, "both entries must be stored, not merged")
}

func TestMCPServerV1Beta1ApplyAdmitsTwoKeysOfOneSecret(t *testing.T) {
	t.Parallel()

	// The exact failing path from #6686: kubectl apply --server-side.
	ctx := context.Background()
	srv := &mcpv1beta1.MCPServer{
		TypeMeta:   metav1.TypeMeta{APIVersion: "toolhive.stacklok.dev/v1beta1", Kind: "MCPServer"},
		ObjectMeta: metav1.ObjectMeta{Name: "secrets-two-keys-apply", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "example/mcp-server:latest",
			Transport: "stdio",
			Secrets:   twoKeysOfOneSecret(),
		},
	}
	require.NoError(t, k8sClient.Patch(ctx, srv,
		//nolint:staticcheck // SA1019: typed server-side apply needs the patch
		// constant; this repo generates no ApplyConfigurations for the new
		// Client.Apply API.
		client.Apply, client.ForceOwnership, client.FieldOwner("secrets-admission-test")))
}

func TestMCPServerV1Alpha1ApplyAdmitsTwoKeysOfOneSecret(t *testing.T) {
	t.Parallel()

	// v1alpha1.MCPServerSpec is v1beta1.MCPServerSpec (api/v1alpha1/types.go),
	// so both served versions carry the (name, key) merge keys.
	ctx := context.Background()
	srv := &mcpv1alpha1.MCPServer{
		TypeMeta:   metav1.TypeMeta{APIVersion: "toolhive.stacklok.dev/v1alpha1", Kind: "MCPServer"},
		ObjectMeta: metav1.ObjectMeta{Name: "secrets-two-keys-apply-v1alpha1", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "example/mcp-server:latest",
			Transport: "stdio",
			Secrets:   twoKeysOfOneSecret(),
		},
	}
	require.NoError(t, k8sClient.Patch(ctx, srv,
		//nolint:staticcheck // SA1019: typed server-side apply needs the patch
		// constant; this repo generates no ApplyConfigurations for the new
		// Client.Apply API.
		client.Apply, client.ForceOwnership, client.FieldOwner("secrets-admission-test")))
}

func TestMCPServerStillRejectsDuplicateNameAndKey(t *testing.T) {
	t.Parallel()

	// Negative control: an exact (name, key) duplicate must stay invalid —
	// this is what proves the merge keys are (name, key) rather than the
	// list having lost map semantics entirely.
	duplicate := []mcpv1beta1.SecretRef{
		{Name: "shared-secret", Key: "username", TargetEnvName: "MCP_USERNAME"},
		{Name: "shared-secret", Key: "username", TargetEnvName: "MCP_USERNAME"},
	}

	t.Run("create", func(t *testing.T) {
		t.Parallel()

		srv := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "secrets-duplicate-create", Namespace: "default"},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "example/mcp-server:latest", Transport: "stdio", Secrets: duplicate,
			},
		}
		err := k8sClient.Create(context.Background(), srv)
		require.Error(t, err)
		t.Logf("create rejection: %v", err)
		assert.Contains(t, err.Error(), "secrets")
	})

	t.Run("server-side apply", func(t *testing.T) {
		t.Parallel()

		srv := &mcpv1beta1.MCPServer{
			TypeMeta:   metav1.TypeMeta{APIVersion: "toolhive.stacklok.dev/v1beta1", Kind: "MCPServer"},
			ObjectMeta: metav1.ObjectMeta{Name: "secrets-duplicate-apply", Namespace: "default"},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "example/mcp-server:latest", Transport: "stdio", Secrets: duplicate,
			},
		}
		err := k8sClient.Patch(context.Background(), srv,
			//nolint:staticcheck // SA1019: typed server-side apply needs the patch
			// constant; this repo generates no ApplyConfigurations for the new
			// Client.Apply API.
			client.Apply, client.ForceOwnership, client.FieldOwner("secrets-admission-test"))
		require.Error(t, err)
		t.Logf("apply rejection: %v", err)
		assert.Contains(t, err.Error(), "secrets")
	})
}
