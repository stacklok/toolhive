// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// openapi-normalize produces the stable, SDK-facing ToolHive OpenAPI contract.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

var requiredFields = map[string][]string{
	"pkg_api_v1.bulkClientRequest":     {"names"},
	"pkg_api_v1.createClientRequest":   {"name"},
	"pkg_api_v1.createGroupRequest":    {"name"},
	"pkg_api_v1.createSecretRequest":   {"key", "value"},
	"pkg_api_v1.setupSecretsRequest":   {"provider_type"},
	"pkg_api_v1.buildPluginRequest":    {"path"},
	"pkg_api_v1.buildSkillRequest":     {"path"},
	"pkg_api_v1.installPluginRequest":  {"name"},
	"pkg_api_v1.installSkillRequest":   {"name"},
	"pkg_api_v1.pushPluginRequest":     {"reference"},
	"pkg_api_v1.pushSkillRequest":      {"reference"},
	"pkg_api_v1.updateSecretRequest":   {"value"},
	"pkg_api_v1.validatePluginRequest": {"path"},
	"pkg_api_v1.validateSkillRequest":  {"path"},
}

var propertyGoNames = map[string]map[string]string{
	"v0.ServerMeta": {
		"io.modelcontextprotocol.registry/publisher-provided": "PublisherProvided",
	},
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: openapi-normalize <input.json|yaml> <output.json> <output.yaml>")
		os.Exit(2)
	}
	input, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail(err)
	}
	document, err := decodeDocument(input, filepath.Ext(os.Args[1]))
	if err != nil {
		fail(err)
	}
	if err := normalize(document); err != nil {
		fail(err)
	}
	jsonOutput, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		fail(err)
	}
	jsonOutput = append(jsonOutput, '\n')
	yamlOutput, err := yaml.Marshal(document)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(os.Args[2], jsonOutput, 0o600); err != nil {
		fail(err)
	}
	if err := os.WriteFile(os.Args[3], yamlOutput, 0o600); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func decodeDocument(input []byte, extension string) (map[string]any, error) {
	var document map[string]any
	if extension == ".yaml" || extension == ".yml" {
		if err := yaml.Unmarshal(input, &document); err != nil {
			return nil, fmt.Errorf("decode YAML: %w", err)
		}
	} else if err := json.Unmarshal(input, &document); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	if document == nil {
		return nil, fmt.Errorf("OpenAPI document must be an object")
	}
	return document, nil
}

func normalize(document map[string]any) error {
	components, ok := object(document["components"])
	if !ok {
		return fmt.Errorf("OpenAPI document has no components object")
	}
	schemas, ok := object(components["schemas"])
	if !ok {
		return fmt.Errorf("OpenAPI document has no schemas object")
	}

	renames, err := schemaRenames(schemas)
	if err != nil {
		return err
	}
	normalizedSchemas := make(map[string]any, len(schemas))
	for oldName, schema := range schemas {
		normalizedSchemas[renames[oldName]] = schema
	}
	components["schemas"] = normalizedSchemas
	if err := rewriteReferences(document, renames); err != nil {
		return err
	}
	if err := applyRequiredFields(normalizedSchemas, renames); err != nil {
		return err
	}
	if err := applyPropertyGoNames(normalizedSchemas, renames); err != nil {
		return err
	}
	if err := validateSchemaNames(normalizedSchemas); err != nil {
		return err
	}
	return normalizeOperations(document)
}

func schemaRenames(schemas map[string]any) (map[string]string, error) {
	keys := make([]string, 0, len(schemas))
	for key := range schemas {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	renames := make(map[string]string, len(keys))
	used := make(map[string]string, len(keys))
	for _, key := range keys {
		name := publicSchemaName(key)
		if previous, exists := used[name]; exists {
			return nil, fmt.Errorf("schema name collision: %q and %q both normalize to %q", previous, key, name)
		}
		used[name] = key
		renames[key] = name
	}
	return renames, nil
}

func publicSchemaName(name string) string {
	if strings.HasPrefix(name, "pkg_api_v1.") {
		return exportName(strings.TrimPrefix(name, "pkg_api_v1."))
	}
	if strings.HasPrefix(name, "authserver.") {
		return "Authentication" + exportName(strings.TrimPrefix(name, "authserver."))
	}

	name = strings.TrimPrefix(name, "github_com_stacklok_toolhive_pkg_")
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == '_' })
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(canonicalSchemaWord(part))
	}
	return builder.String()
}

func canonicalSchemaWord(value string) string {
	if canonical, ok := canonicalSchemaWords[strings.ToLower(value)]; ok {
		return canonical
	}
	return exportName(value)
}

var canonicalSchemaWords = map[string]string{
	"api":           "API",
	"authserver":    "Authentication",
	"awssts":        "AWSSTS",
	"authz":         "Authorization",
	"cimd":          "CIMD",
	"dcr":           "DCR",
	"jwt":           "JWT",
	"oidc":          "OIDC",
	"oauth2":        "OAuth2",
	"spiffe":        "SPIFFE",
	"tls":           "TLS",
	"tokenexchange": "TokenExchange",
	"types":         "",
	"upstreamswap":  "UpstreamSwap",
}

func exportName(value string) string {
	if value == "" {
		return "Schema"
	}
	first := true
	return strings.Map(func(r rune) rune {
		if first {
			first = false
			return unicode.ToUpper(r)
		}
		return r
	}, value)
}

func validateSchemaNames(schemas map[string]any) error {
	for name := range schemas {
		if name == "Schema" || strings.ContainsAny(name, "._") ||
			strings.HasPrefix(name, "PkgApiV1") || strings.HasPrefix(name, "GithubComStacklokToolhive") ||
			strings.HasPrefix(name, "Authserver") || strings.HasPrefix(name, "Types") {
			return fmt.Errorf("schema name %q contains a source-name artifact", name)
		}
	}
	return nil
}

func rewriteReferences(value any, renames map[string]string) error {
	switch value := value.(type) {
	case map[string]any:
		if reference, ok := value["$ref"].(string); ok {
			const prefix = "#/components/schemas/"
			if oldName, found := strings.CutPrefix(reference, prefix); found {
				newName, exists := renames[oldName]
				if !exists {
					return fmt.Errorf("reference %q points to a schema that does not exist", reference)
				}
				value["$ref"] = prefix + newName
			}
		}
		for _, child := range value {
			if err := rewriteReferences(child, renames); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := rewriteReferences(child, renames); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyRequiredFields(schemas map[string]any, renames map[string]string) error {
	return applyRequiredFieldsFor(schemas, renames, requiredFields)
}

func applyRequiredFieldsFor(schemas map[string]any, renames map[string]string, fieldsBySchema map[string][]string) error {
	for sourceName, fields := range fieldsBySchema {
		normalizedName, exists := renames[sourceName]
		if !exists {
			normalizedName = publicSchemaName(sourceName)
			if _, normalizedExists := schemas[normalizedName]; !normalizedExists {
				return fmt.Errorf("required-field override references missing schema %q", sourceName)
			}
		}
		schema, ok := object(schemas[normalizedName])
		if !ok {
			return fmt.Errorf("required-field override schema %q is not an object", sourceName)
		}
		properties, ok := object(schema["properties"])
		if !ok {
			return fmt.Errorf("required-field override schema %q has no properties", sourceName)
		}
		existing, validRequired := stringsSlice(schema["required"])
		if schema["required"] != nil && !validRequired {
			return fmt.Errorf("required-field override schema %q has an invalid required field list", sourceName)
		}
		seen := make(map[string]bool, len(existing)+len(fields))
		for _, field := range existing {
			seen[field] = true
		}
		for _, field := range fields {
			if _, exists := properties[field]; !exists {
				return fmt.Errorf("required-field override references missing field %q on schema %q", field, sourceName)
			}
			seen[field] = true
		}
		required := make([]string, 0, len(seen))
		for field := range seen {
			required = append(required, field)
		}
		sort.Strings(required)
		schema["required"] = required
	}
	return nil
}

func applyPropertyGoNames(schemas map[string]any, renames map[string]string) error {
	for sourceName, propertiesByName := range propertyGoNames {
		normalizedName, exists := renames[sourceName]
		if !exists {
			normalizedName = publicSchemaName(sourceName)
		}
		schema, ok := object(schemas[normalizedName])
		if !ok {
			return fmt.Errorf("property name override schema %q is missing or not an object", sourceName)
		}
		properties, ok := object(schema["properties"])
		if !ok {
			return fmt.Errorf("property name override schema %q has no properties", sourceName)
		}
		metadata, exists := object(schema["x-ogen-properties"])
		if schema["x-ogen-properties"] != nil && !exists {
			return fmt.Errorf("property name override schema %q has invalid x-ogen-properties metadata", sourceName)
		}
		if !exists {
			metadata = make(map[string]any, len(propertiesByName))
			schema["x-ogen-properties"] = metadata
		}
		for propertyName, goName := range propertiesByName {
			property, ok := object(properties[propertyName])
			if !ok {
				return fmt.Errorf("property name override references missing property %q on schema %q", propertyName, sourceName)
			}
			property["x-ogen-name"] = goName
			metadata[propertyName] = map[string]any{"name": goName}
		}
	}
	return nil
}

func normalizeOperations(document map[string]any) error {
	paths, ok := object(document["paths"])
	if !ok {
		return fmt.Errorf("OpenAPI document has no paths object")
	}
	seen := make(map[string]string)
	encountered := make(map[string]bool, len(operationIDs))
	for path, pathItem := range paths {
		item, ok := object(pathItem)
		if !ok {
			return fmt.Errorf("path %q is not an object", path)
		}
		for method, operation := range item {
			if !isHTTPMethod(method) {
				continue
			}
			operation, ok := object(operation)
			if !ok {
				return fmt.Errorf("operation %s %s is not an object", strings.ToUpper(method), path)
			}
			operationKey := strings.ToUpper(method) + " " + path
			id, exists := operationIDs[operationKey]
			if !exists {
				return fmt.Errorf("operation override is missing for %s", operationKey)
			}
			if previous, duplicate := seen[id]; duplicate {
				return fmt.Errorf("operation ID %q is shared by %q and %q", id, previous, path)
			}
			seen[id] = path
			encountered[operationKey] = true
			operation["operationId"] = id
			normalizeRequestBodies(operation)
			if path == "/api/openapi.json" && strings.EqualFold(method, "get") {
				if err := normalizeOpenAPIResponse(operation); err != nil {
					return err
				}
			}
		}
	}
	return verifyOperationMappings(encountered, operationIDs)
}

func verifyOperationMappings(encountered map[string]bool, mappings map[string]string) error {
	for operationKey := range mappings {
		if !encountered[operationKey] {
			return fmt.Errorf("operation override is stale for %s", operationKey)
		}
	}
	return nil
}

func normalizeOpenAPIResponse(operation map[string]any) error {
	responses, ok := object(operation["responses"])
	if !ok {
		return fmt.Errorf("OpenAPI specification operation has no responses")
	}
	response, ok := object(responses["200"])
	if !ok {
		return fmt.Errorf("OpenAPI specification operation has no 200 response")
	}
	content, ok := object(response["content"])
	if !ok {
		return fmt.Errorf("OpenAPI specification response has no content")
	}
	media, ok := object(content["application/json"])
	if !ok {
		return fmt.Errorf("OpenAPI specification response has no JSON content")
	}
	media["schema"] = map[string]any{"type": "object", "additionalProperties": true}
	return nil
}

var operationIDs = map[string]string{
	"GET /api/openapi.json": "GetOpenAPISpecification",

	"GET /api/v1beta/clients":                          "ListClients",
	"POST /api/v1beta/clients":                         "RegisterClient",
	"POST /api/v1beta/clients/register":                "RegisterClients",
	"POST /api/v1beta/clients/unregister":              "UnregisterClients",
	"DELETE /api/v1beta/clients/{name}":                "UnregisterClient",
	"DELETE /api/v1beta/clients/{name}/groups/{group}": "RemoveClientFromGroup",
	"GET /api/v1beta/discovery/clients":                "DiscoverClients",

	"GET /api/v1beta/groups":           "ListGroups",
	"POST /api/v1beta/groups":          "CreateGroup",
	"DELETE /api/v1beta/groups/{name}": "DeleteGroup",
	"GET /api/v1beta/groups/{name}":    "GetGroup",

	"GET /api/v1beta/plugins":                 "ListPlugins",
	"POST /api/v1beta/plugins":                "InstallPlugin",
	"POST /api/v1beta/plugins/build":          "BuildPlugin",
	"GET /api/v1beta/plugins/builds":          "ListPluginBuilds",
	"DELETE /api/v1beta/plugins/builds/{tag}": "DeletePluginBuild",
	"GET /api/v1beta/plugins/content":         "GetPluginContent",
	"POST /api/v1beta/plugins/push":           "PushPlugin",
	"POST /api/v1beta/plugins/sync":           "SyncPlugin",
	"POST /api/v1beta/plugins/upgrade":        "UpgradePlugin",
	"POST /api/v1beta/plugins/validate":       "ValidatePlugin",
	"DELETE /api/v1beta/plugins/{name}":       "DeletePlugin",
	"GET /api/v1beta/plugins/{name}":          "GetPlugin",

	"GET /api/v1beta/registry":                 "ListRegistries",
	"POST /api/v1beta/registry":                "AddRegistry",
	"POST /api/v1beta/registry/auth/login":     "LoginRegistry",
	"POST /api/v1beta/registry/auth/logout":    "LogoutRegistry",
	"DELETE /api/v1beta/registry/{name}":       "DeleteRegistry",
	"GET /api/v1beta/registry/{name}":          "GetRegistry",
	"PUT /api/v1beta/registry/{name}":          "UpdateRegistry",
	"POST /api/v1beta/registry/{name}/refresh": "RefreshRegistry",
	"GET /api/v1beta/registry/{name}/servers":  "ListRegistryServers",
	"GET /api/v1beta/registry/{name}/servers/" +
		"{serverName}": "GetRegistryServer",

	"POST /api/v1beta/secrets":                      "ConfigureSecrets",
	"GET /api/v1beta/secrets/default":               "GetDefaultSecretProvider",
	"GET /api/v1beta/secrets/default/keys":          "ListDefaultSecretKeys",
	"POST /api/v1beta/secrets/default/keys":         "CreateDefaultSecret",
	"DELETE /api/v1beta/secrets/default/keys/{key}": "DeleteDefaultSecret",
	"PUT /api/v1beta/secrets/default/keys/{key}":    "UpdateDefaultSecret",

	"GET /api/v1beta/skills":                 "ListSkills",
	"POST /api/v1beta/skills":                "InstallSkill",
	"POST /api/v1beta/skills/build":          "BuildSkill",
	"GET /api/v1beta/skills/builds":          "ListSkillBuilds",
	"DELETE /api/v1beta/skills/builds/{tag}": "DeleteSkillBuild",
	"GET /api/v1beta/skills/content":         "GetSkillContent",
	"POST /api/v1beta/skills/push":           "PushSkill",
	"POST /api/v1beta/skills/sync":           "SyncSkill",
	"POST /api/v1beta/skills/upgrade":        "UpgradeSkill",
	"POST /api/v1beta/skills/validate":       "ValidateSkill",
	"DELETE /api/v1beta/skills/{name}":       "DeleteSkill",
	"GET /api/v1beta/skills/{name}":          "GetSkill",

	"GET /api/v1beta/version":                        "GetVersion",
	"GET /api/v1beta/workloads":                      "ListWorkloads",
	"POST /api/v1beta/workloads":                     "CreateWorkload",
	"POST /api/v1beta/workloads/delete":              "DeleteWorkloads",
	"POST /api/v1beta/workloads/restart":             "RestartWorkloads",
	"POST /api/v1beta/workloads/stop":                "StopWorkloads",
	"GET /api/v1beta/workloads/upgrade-check":        "CheckWorkloadsForUpgrades",
	"DELETE /api/v1beta/workloads/{name}":            "DeleteWorkload",
	"GET /api/v1beta/workloads/{name}":               "GetWorkload",
	"POST /api/v1beta/workloads/{name}/edit":         "UpdateWorkload",
	"GET /api/v1beta/workloads/{name}/export":        "ExportWorkload",
	"GET /api/v1beta/workloads/{name}/logs":          "GetWorkloadLogs",
	"GET /api/v1beta/workloads/{name}/proxy-logs":    "GetWorkloadProxyLogs",
	"POST /api/v1beta/workloads/{name}/restart":      "RestartWorkload",
	"GET /api/v1beta/workloads/{name}/status":        "GetWorkloadStatus",
	"POST /api/v1beta/workloads/{name}/stop":         "StopWorkload",
	"POST /api/v1beta/workloads/{name}/upgrade":      "UpgradeWorkload",
	"GET /api/v1beta/workloads/{name}/upgrade-check": "CheckWorkloadForUpgrade",

	"GET /health": "GetHealth",
	"GET /registry/{registryName}/v0.1/servers": "ListPublicRegistryServers",
	"GET /registry/{registryName}/v0.1/servers/" +
		"{serverName}/versions/latest": "GetLatestPublicRegistryServer",
	"GET /registry/{registryName}/v0.1/x/dev.toolhive/plugins": "ListPublicRegistryPlugins",
	"GET /registry/{registryName}/v0.1/x/dev.toolhive/plugins/" +
		"{namespace}/{pluginName}": "GetPublicRegistryPlugin",
	"GET /registry/{registryName}/v0.1/x/dev.toolhive/skills": "ListPublicRegistrySkills",
	"GET /registry/{registryName}/v0.1/x/dev.toolhive/skills/" +
		"{namespace}/{skillName}": "GetPublicRegistrySkill",
}

func normalizeRequestBodies(operation map[string]any) {
	requestBody, ok := object(operation["requestBody"])
	if !ok {
		return
	}
	content, ok := object(requestBody["content"])
	if !ok {
		return
	}
	for _, mediaType := range content {
		media, ok := object(mediaType)
		if !ok {
			continue
		}
		schema, ok := object(media["schema"])
		if !ok {
			continue
		}
		oneOf, ok := schema["oneOf"].([]any)
		if !ok || len(oneOf) != 2 {
			continue
		}
		first, firstOK := object(oneOf[0])
		second, secondOK := object(oneOf[1])
		if firstOK && secondOK && first["type"] == "object" && second["$ref"] != nil {
			media["schema"] = second
		}
	}
}

func isHTTPMethod(method string) bool {
	switch strings.ToLower(method) {
	case "get", "put", "post", "delete", "patch", "head", "options", "trace":
		return true
	default:
		return false
	}
}

func object(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

func stringsSlice(value any) ([]string, bool) {
	if values, ok := value.([]string); ok {
		return values, true
	}
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		stringValue, ok := value.(string)
		if !ok {
			return nil, false
		}
		result = append(result, stringValue)
	}
	return result, true
}
