// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestNormalizeJSONAndYAMLDeterministically(t *testing.T) {
	t.Parallel()
	input, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "server", "swagger.json"))
	require.NoError(t, err)

	for name, testCase := range map[string]struct {
		input     []byte
		extension string
	}{
		"JSON": {input: input, extension: ".json"},
		"YAML": {input: toYAML(t, input), extension: ".yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			firstJSON, firstYAML := normalizeAndSerialize(t, testCase.input, testCase.extension)
			secondJSON, secondYAML := normalizeAndSerialize(t, testCase.input, testCase.extension)
			require.Equal(t, firstJSON, secondJSON)
			require.Equal(t, firstYAML, secondYAML)

			for outputName, output := range map[string]struct {
				content   []byte
				extension string
			}{
				"JSON": {content: firstJSON, extension: ".json"},
				"YAML": {content: firstYAML, extension: ".yaml"},
			} {
				t.Run(outputName, func(t *testing.T) {
					document, err := decodeDocument(output.content, output.extension)
					require.NoError(t, err)
					assertNormalizedDocument(t, document)
					require.NoError(t, normalize(document), "normalized output must remain semantically valid")
				})
			}
		})
	}
}

func normalizeAndSerialize(t *testing.T, input []byte, extension string) ([]byte, []byte) {
	t.Helper()
	document, err := decodeDocument(input, extension)
	require.NoError(t, err)
	require.NoError(t, normalize(document))

	jsonOutput, err := json.MarshalIndent(document, "", "  ")
	require.NoError(t, err)
	jsonOutput = append(jsonOutput, '\n')
	yamlOutput, err := yaml.Marshal(document)
	require.NoError(t, err)
	return jsonOutput, yamlOutput
}

func assertNormalizedDocument(t *testing.T, document map[string]any) {
	t.Helper()
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	require.Contains(t, schemas, "CreateSecretRequest")
	publisherProvided := schemas["V0ServerMeta"].(map[string]any)["properties"].(map[string]any)["io.modelcontextprotocol.registry/publisher-provided"].(map[string]any)
	require.Equal(t, "PublisherProvided", publisherProvided["x-ogen-name"], "wire property name must remain unchanged")
	require.Equal(t, map[string]any{"name": "PublisherProvided"}, schemas["V0ServerMeta"].(map[string]any)["x-ogen-properties"].(map[string]any)["io.modelcontextprotocol.registry/publisher-provided"])
	required, ok := stringsSlice(schemas["UpdateSecretRequest"].(map[string]any)["required"])
	require.True(t, ok)
	require.Equal(t, []string{"value"}, required)

	paths := document["paths"].(map[string]any)
	operation := paths["/api/openapi.json"].(map[string]any)["get"].(map[string]any)
	require.Equal(t, "GetOpenAPISpecification", operation["operationId"])
	response := operation["responses"].(map[string]any)["200"].(map[string]any)
	require.Equal(t, true, response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["additionalProperties"])

	registerClient := paths["/api/v1beta/clients"].(map[string]any)["post"].(map[string]any)
	content := registerClient["requestBody"].(map[string]any)["content"].(map[string]any)
	require.Len(t, content, 1, "all applicable request-body media types must be normalized")
	media := content["application/json"].(map[string]any)
	require.Equal(t, map[string]any{
		"$ref":        "#/components/schemas/CreateClientRequest",
		"description": "Client to register",
		"summary":     "client",
	}, media["schema"])

	validateReferences(t, document, schemas)
}

func validateReferences(t *testing.T, value any, schemas map[string]any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		if reference, ok := value["$ref"].(string); ok {
			const prefix = "#/components/schemas/"
			if schemaName, isSchemaReference := strings.CutPrefix(reference, prefix); isSchemaReference {
				require.Contains(t, schemas, schemaName, "schema reference must resolve")
			}
		}
		for _, child := range value {
			validateReferences(t, child, schemas)
		}
	case []any:
		for _, child := range value {
			validateReferences(t, child, schemas)
		}
	}
}

func TestSchemaNormalizationGuards(t *testing.T) {
	t.Parallel()
	_, err := schemaRenames(map[string]any{"pkg_api_v1.value": map[string]any{}, "Value": map[string]any{}})
	require.ErrorContains(t, err, "schema name collision")

	document := map[string]any{"$ref": "#/components/schemas/pkg_api_v1.value"}
	require.NoError(t, rewriteReferences(document, map[string]string{"pkg_api_v1.value": "Value"}))
	require.Equal(t, "#/components/schemas/Value", document["$ref"])
	require.Error(t, rewriteReferences(map[string]any{"$ref": "#/components/schemas/missing"}, map[string]string{}))

	require.Error(t, validateSchemaNames(map[string]any{"GithubComStacklokToolhiveRequest": map[string]any{}}))
	require.Error(t, validateSchemaNames(map[string]any{"AuthserverRunConfig": map[string]any{}}))
	require.Error(t, validateSchemaNames(map[string]any{"TypesMiddlewareConfig": map[string]any{}}))
	require.NoError(t, validateSchemaNames(map[string]any{"Request": map[string]any{}}))

	for source, want := range map[string]string{
		"authserver.RunConfig":                                "AuthenticationRunConfig",
		"github_com_stacklok_toolhive_pkg_auth_awssts.Config": "AuthAWSSTSConfig",
		"github_com_stacklok_toolhive_pkg_authserver_server_tokenexchange.JWTBearerSubjectBinding": "AuthenticationServerTokenExchangeJWTBearerSubjectBinding",
		"tokenexchange.JWTBearerGrantPolicy": "TokenExchangeJWTBearerGrantPolicy",
		"types.ToolRateLimitConfig":          "ToolRateLimitConfig",
	} {
		require.Equal(t, want, publicSchemaName(source))
	}
}

func TestNormalizerRejectsStaleMappingsAndRequiredFields(t *testing.T) {
	t.Parallel()
	require.ErrorContains(t, verifyOperationMappings(map[string]bool{"GET /health": true}, map[string]string{"GET /missing": "Missing"}), "stale")
	require.NoError(t, verifyOperationMappings(map[string]bool{"GET /health": true}, map[string]string{"GET /health": "GetHealth"}))

	schemas := map[string]any{"UpdateSecretRequest": map[string]any{"properties": map[string]any{"value": map[string]any{"type": "string"}}}}
	require.NoError(t, applyRequiredFieldsFor(schemas, map[string]string{"pkg_api_v1.updateSecretRequest": "UpdateSecretRequest"}, map[string][]string{"pkg_api_v1.updateSecretRequest": {"value"}}))
	require.Equal(t, []string{"value"}, schemas["UpdateSecretRequest"].(map[string]any)["required"])
	require.Error(t, applyRequiredFieldsFor(map[string]any{}, map[string]string{}, map[string][]string{"pkg_api_v1.updateSecretRequest": {"value"}}))
	require.Error(t, applyRequiredFieldsFor(map[string]any{"UpdateSecretRequest": map[string]any{"properties": map[string]any{}}}, map[string]string{"pkg_api_v1.updateSecretRequest": "UpdateSecretRequest"}, map[string][]string{"pkg_api_v1.updateSecretRequest": {"value"}}))

	publisherProvided := map[string]any{"V0ServerMeta": map[string]any{"properties": map[string]any{"io.modelcontextprotocol.registry/publisher-provided": map[string]any{"type": "object"}}}}
	require.NoError(t, applyPropertyGoNames(publisherProvided, map[string]string{"v0.ServerMeta": "V0ServerMeta"}))
	property := publisherProvided["V0ServerMeta"].(map[string]any)["properties"].(map[string]any)["io.modelcontextprotocol.registry/publisher-provided"].(map[string]any)
	require.Equal(t, "PublisherProvided", property["x-ogen-name"])
	require.Equal(t, map[string]any{"name": "PublisherProvided"}, publisherProvided["V0ServerMeta"].(map[string]any)["x-ogen-properties"].(map[string]any)["io.modelcontextprotocol.registry/publisher-provided"])
	require.Error(t, applyPropertyGoNames(map[string]any{}, map[string]string{}))
}

func TestNormalizeRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()
	_, err := decodeDocument([]byte("[]"), ".json")
	require.Error(t, err)
	_, err = decodeDocument([]byte("- value"), ".yaml")
	require.Error(t, err)
}

func toYAML(t *testing.T, input []byte) []byte {
	t.Helper()
	var document map[string]any
	require.NoError(t, json.Unmarshal(input, &document))
	output, err := yaml.Marshal(document)
	require.NoError(t, err)
	return output
}
