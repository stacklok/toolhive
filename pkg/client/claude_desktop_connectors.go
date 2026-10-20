// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/fileutils"
)

// Claude Desktop in third-party mode takes remote MCP servers ("connectors")
// only from the managedMcpServers key of its applied configuration. ToolHive
// writes them into the same configLibrary document "thv llm setup --client
// claude-desktop" owns: Desktop applies exactly one document at a time, so a
// separate document would displace the LLM gateway configuration. Desktop
// connects to each server directly and runs the server's OAuth sign-in itself,
// so no local proxy is involved.
//
// Re-running "thv llm setup" keeps these entries when it rewrites the document.
// "thv llm teardown" deletes the document along with any entries still in it
// and logs a warning naming them, since Desktop ignores them without the
// inference settings.

const claudeDesktopManagedMCPServersKey = "managedMcpServers"

var (
	// ErrConnectorsUnsupported is returned for a client that cannot be
	// configured with remote MCP connectors.
	ErrConnectorsUnsupported = errors.New("client does not support connectors")
	// ErrConnectorsNotSetUp is returned when Claude Desktop has no
	// ToolHive-owned configuration document to add connectors to.
	ErrConnectorsNotSetUp = errors.New(
		"no ToolHive configuration found for Claude Desktop; run \"thv llm setup --client claude-desktop\" first")
	// ErrConnectorNotFound is returned when removing a connector that does not exist.
	ErrConnectorNotFound = errors.New("connector not found")
)

// Connector is a remote MCP server configured directly in a client.
type Connector struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// AddConnector adds a remote MCP server to the client, or updates the URL of
// an existing connector with the same name, keeping its other fields. The
// client signs in to the server itself (OAuth), so nothing is proxied through
// ToolHive. A same-name entry that is not a remote server is an error.
//
// The write succeeds but logs a warning when Claude Desktop will not load the
// connector: when a managed-preferences (MDM) profile overrides the local
// configuration, or when the user has applied a different saved configuration.
func (cm *ClientManager) AddConnector(clientType ClientApp, name, serverURL string) error {
	if err := validateConnector(name, serverURL); err != nil {
		return err
	}
	applied, err := cm.updateConnectors(clientType, func(servers []any) ([]any, error) {
		for _, s := range servers {
			m, ok := s.(map[string]any)
			if !ok || m["name"] != name {
				continue
			}
			if !isRemoteServer(m) {
				return nil, fmt.Errorf("a server named %q that is not a remote connector already exists", name)
			}
			m["url"] = serverURL
			return servers, nil
		}
		return append(servers, map[string]any{
			"name":      name,
			"transport": "http",
			"url":       serverURL,
			"oauth":     true,
		}), nil
	})
	if err != nil {
		return err
	}
	if cm.IsManaged(clientType) {
		slog.Warn("a managed configuration overrides Claude Desktop's local configuration, "+
			"so it will not load this connector; add it to the managed configuration instead",
			"connector", name)
	}
	if !applied {
		slog.Warn("Claude Desktop is using a different saved configuration, so it will not load this connector "+
			"until \""+claudeDesktopManagedEntryName+"\" is applied again", "connector", name)
	}
	return nil
}

// RemoveConnector removes the connector with the given name from the client.
// Entries that are not remote servers are never removed, matching
// ListConnectors.
func (cm *ClientManager) RemoveConnector(clientType ClientApp, name string) error {
	_, err := cm.updateConnectors(clientType, func(servers []any) ([]any, error) {
		kept := make([]any, 0, len(servers))
		for _, s := range servers {
			if m, ok := s.(map[string]any); ok && m["name"] == name && isRemoteServer(m) {
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) == len(servers) {
			return nil, fmt.Errorf("%w: %q", ErrConnectorNotFound, name)
		}
		return kept, nil
	})
	return err
}

// ListConnectors returns the remote MCP servers configured in the client.
// Entries without a name or URL (for example local stdio servers a user added
// by hand) are skipped.
func (cm *ClientManager) ListConnectors(clientType ClientApp) ([]Connector, error) {
	docPath, err := cm.connectorDocPath(clientType)
	if err != nil {
		return nil, err
	}
	doc, err := readClaudeDesktopDoc(docPath)
	if err != nil {
		return nil, err
	}
	servers, err := managedMCPServers(doc, docPath)
	if err != nil {
		return nil, err
	}
	return connectorsFrom(servers), nil
}

// updateConnectors applies fn to the managedMcpServers array of ToolHive's
// Claude Desktop configuration document, under the same lock "thv llm setup"
// and "thv llm teardown" hold, preserving every other key in the document. It
// reports whether that document is the one Claude Desktop currently applies.
func (cm *ClientManager) updateConnectors(clientType ClientApp, fn func([]any) ([]any, error)) (bool, error) {
	appCfg, err := cm.connectorAppConfig(clientType)
	if err != nil {
		return false, err
	}
	metaPath := cm.buildLLMSettingsPath(appCfg)
	// The lock file lives in configLibrary, which only exists once setup has run.
	if !fileExistsAt(filepath.Dir(metaPath)) {
		return false, ErrConnectorsNotSetUp
	}
	var applied bool
	err = fileutils.WithFileLock(metaPath, func() error {
		docPath, isApplied, err := connectorDocPathLocked(metaPath)
		if err != nil {
			return err
		}
		applied = isApplied
		doc, err := readClaudeDesktopDoc(docPath)
		if err != nil {
			return err
		}
		servers, err := managedMCPServers(doc, docPath)
		if err != nil {
			return err
		}
		servers, err = fn(servers)
		if err != nil {
			return err
		}
		if len(servers) == 0 {
			delete(doc, claudeDesktopManagedMCPServersKey)
		} else {
			doc[claudeDesktopManagedMCPServersKey] = servers
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding %s: %w", docPath, err)
		}
		if err := fileutils.AtomicWriteFile(docPath, data, 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", docPath, err)
		}
		return nil
	})
	return applied, err
}

// connectorAppConfig returns the integration config for a client that
// supports connectors. Only Claude Desktop does today.
func (cm *ClientManager) connectorAppConfig(clientType ClientApp) (*clientAppConfig, error) {
	if clientType != ClientApp(ClaudeDesktop) {
		return nil, fmt.Errorf("%w: %q (supported: %s)", ErrConnectorsUnsupported, clientType, ClaudeDesktop)
	}
	appCfg := cm.lookupClientAppConfig(clientType)
	if appCfg == nil {
		return nil, fmt.Errorf("%w: %q", ErrConnectorsUnsupported, clientType)
	}
	return appCfg, nil
}

// connectorDocPath resolves ToolHive's Claude Desktop configuration document
// without taking the lock; used by read-only callers.
func (cm *ClientManager) connectorDocPath(clientType ClientApp) (string, error) {
	appCfg, err := cm.connectorAppConfig(clientType)
	if err != nil {
		return "", err
	}
	docPath, _, err := connectorDocPathLocked(cm.buildLLMSettingsPath(appCfg))
	return docPath, err
}

// connectorDocPathLocked finds the document named by ToolHive's _meta.json
// entry, and reports whether _meta.json's appliedId still selects it. Callers
// that write must hold the metaPath lock.
func connectorDocPathLocked(metaPath string) (string, bool, error) {
	meta, err := readClaudeDesktopMeta(metaPath)
	if err != nil {
		return "", false, err
	}
	id := metaEntryID(meta, claudeDesktopManagedEntryName)
	if id == "" {
		return "", false, ErrConnectorsNotSetUp
	}
	docPath := filepath.Join(filepath.Dir(metaPath), id+".json")
	if !fileExistsAt(docPath) {
		return "", false, ErrConnectorsNotSetUp
	}
	applied, _ := meta["appliedId"].(string)
	return docPath, applied == id, nil
}

// readClaudeDesktopDoc reads a configuration document into a generic map so
// keys ToolHive does not own are preserved on write-back.
func readClaudeDesktopDoc(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is derived from the configLibrary dir
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

// managedMCPServers returns the document's managedMcpServers array. A value of
// the wrong shape is an error rather than treated as empty, so a write never
// overwrites data ToolHive does not understand.
func managedMCPServers(doc map[string]any, path string) ([]any, error) {
	v, ok := doc[claudeDesktopManagedMCPServersKey]
	if !ok || v == nil {
		return nil, nil
	}
	servers, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("parsing %s: %q must be a JSON array", path, claudeDesktopManagedMCPServersKey)
	}
	return servers, nil
}

// connectorsFrom converts managedMcpServers entries to Connectors, skipping
// entries that are not remote servers.
func connectorsFrom(servers []any) []Connector {
	connectors := make([]Connector, 0, len(servers))
	for _, s := range servers {
		m, ok := s.(map[string]any)
		if !ok || !isRemoteServer(m) {
			continue
		}
		connectors = append(connectors, Connector{Name: m["name"].(string), URL: m["url"].(string)})
	}
	return connectors
}

// isRemoteServer reports whether a managedMcpServers entry is a remote server
// with a name, the only kind of entry the connector commands read or change.
func isRemoteServer(m map[string]any) bool {
	name, _ := m["name"].(string)
	serverURL, _ := m["url"].(string)
	return name != "" && serverURL != ""
}

// validateConnector checks the name and URL before anything is written.
// Claude Desktop only connects to remote servers over HTTPS.
func validateConnector(name, serverURL string) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid connector name %q: must be non-empty with no leading or trailing spaces", name)
	}
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("invalid connector URL %q: must be an absolute https URL", serverURL)
	}
	return nil
}

// previousManagedMCPServers returns the managedMcpServers entries of the
// document "thv llm setup" is about to rewrite, so re-running setup keeps them.
// A missing document has none. An unreadable one is still replaced, as before
// connectors existed, with a warning that anything in it is dropped.
func previousManagedMCPServers(path string) []any {
	if !fileExistsAt(path) {
		return nil
	}
	servers, err := func() ([]any, error) {
		doc, err := readClaudeDesktopDoc(path)
		if err != nil {
			return nil, err
		}
		return managedMCPServers(doc, path)
	}()
	if err != nil {
		slog.Warn("could not read connectors from the previous Claude Desktop configuration; "+
			"they will not be carried over", "path", path, "error", err)
		return nil
	}
	return servers
}

// connectorNamesInDoc returns the names of the connectors in a configuration
// document, for the teardown warning. Errors are ignored: the warning is
// best-effort and must not block teardown.
func connectorNamesInDoc(path string) []string {
	doc, err := readClaudeDesktopDoc(path)
	if err != nil {
		return nil
	}
	servers, err := managedMCPServers(doc, path)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(servers))
	for _, c := range connectorsFrom(servers) {
		names = append(names, c.Name)
	}
	return names
}
