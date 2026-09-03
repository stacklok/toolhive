// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var (
	aiPluginInfoScope       string
	aiPluginInfoFormat      string
	aiPluginInfoProjectRoot string
)

var aiPluginInfoCmd = &cobra.Command{
	Use:               "info [plugin-name]",
	Short:             "Show details of an AI-tool plugin",
	Long:              `Display detailed information about a plugin, including metadata, version, and installation status.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAIPluginNames,
	PreRunE: chainPreRunE(
		validateAIPluginScope(&aiPluginInfoScope),
		ValidateFormat(&aiPluginInfoFormat),
	),
	RunE: aiPluginInfoCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginInfoCmd)

	aiPluginInfoCmd.Flags().StringVar(&aiPluginInfoScope, "scope", "", "Filter by scope (user, project)")
	AddFormatFlag(aiPluginInfoCmd, &aiPluginInfoFormat)
	aiPluginInfoCmd.Flags().StringVar(&aiPluginInfoProjectRoot, "project-root", "", "Project root path for project-scoped plugins")
}

func aiPluginInfoCmdFunc(cmd *cobra.Command, args []string) error {
	c := newAIPluginClient(cmd.Context())

	projectRoot, err := absProjectRoot(aiPluginInfoProjectRoot)
	if err != nil {
		return err
	}

	info, err := c.Info(cmd.Context(), plugins.InfoOptions{
		Name:        args[0],
		Scope:       plugins.Scope(aiPluginInfoScope),
		ProjectRoot: projectRoot,
	})
	if err != nil {
		return formatAIPluginError("get plugin info", err)
	}

	switch aiPluginInfoFormat {
	case FormatJSON:
		data, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
	default:
		printAIPluginInfoText(info)
	}

	return nil
}

func printAIPluginInfoText(info *plugins.PluginInfo) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	_, _ = fmt.Fprintf(w, "Name:\t%s\n", info.Metadata.Name)
	_, _ = fmt.Fprintf(w, "Version:\t%s\n", info.Metadata.Version)
	switch {
	case info.Provenance != nil && info.Provenance.Provisional:
		_, _ = fmt.Fprintf(w, "Signed by:\t%s (provisional)\n", info.Provenance.SignerIdentity)
		_, _ = fmt.Fprintf(w, "Cert issuer:\t%s\n", info.Provenance.CertIssuer)
	case info.Provenance != nil:
		_, _ = fmt.Fprintf(w, "Signed by:\t%s\n", info.Provenance.SignerIdentity)
		_, _ = fmt.Fprintf(w, "Cert issuer:\t%s\n", info.Provenance.CertIssuer)
	case info.Unsigned:
		_, _ = fmt.Fprintf(w, "Signed by:\t(unsigned — explicit exception)\n")
	case info.TrustUnrecorded:
		_, _ = fmt.Fprintf(w, "Signed by:\t(trust unrecorded — run 'thv ai-plugin sync')\n")
	}
	_, _ = fmt.Fprintf(w, "Description:\t%s\n", info.Metadata.Description)

	if s := info.InstalledPlugin; s != nil {
		_, _ = fmt.Fprintf(w, "Scope:\t%s\n", s.Scope)
		_, _ = fmt.Fprintf(w, "Status:\t%s\n", s.Status)
		_, _ = fmt.Fprintf(w, "Reference:\t%s\n", s.Reference)
		_, _ = fmt.Fprintf(w, "Installed At:\t%s\n", s.InstalledAt.Format("2006-01-02 15:04:05"))
		if len(s.Clients) > 0 {
			_, _ = fmt.Fprintf(w, "Clients:\t%s\n", strings.Join(s.Clients, ", "))
		}
		if len(s.Components) > 0 {
			managed, declared := splitComponentInventory(s.Components)
			if len(managed) > 0 {
				_, _ = fmt.Fprintf(w, "Components:\t%s\n", formatComponentInventory(managed))
			}
			if len(declared) > 0 {
				_, _ = fmt.Fprintf(w, "Declared (not managed by ToolHive):\t%s\n", formatComponentInventory(declared))
			}
		}
	}

	if len(info.UnmaterializedComponents) > 0 {
		_, _ = fmt.Fprintln(w, "\nNot activated on this client (declared by the plugin, but the client does not load them):")
		for _, clientType := range slices.Sorted(maps.Keys(info.UnmaterializedComponents)) {
			types := info.UnmaterializedComponents[clientType]
			labels := make([]string, 0, len(types))
			for _, t := range types {
				labels = append(labels, string(t))
			}
			_, _ = fmt.Fprintf(w, "  %s:\t%s\n", clientType, strings.Join(labels, ", "))
		}
	}
	if len(info.ProjectScopeDegradedClients) > 0 {
		degraded := append([]string(nil), info.ProjectScopeDegradedClients...)
		sort.Strings(degraded)
		_, _ = fmt.Fprintf(w, "\nInstalled at user scope instead of project scope (client cannot scope per-project):\t%s\n",
			strings.Join(degraded, ", "))
	}

	_ = w.Flush()
}

// formatComponentInventory renders a ComponentInventory (map[string]int) as a
// sorted, space-separated "key=count" sequence for deterministic output.
func formatComponentInventory(inv plugins.ComponentInventory) string {
	keys := slices.Sorted(maps.Keys(inv))
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, inv[k]))
	}
	return strings.Join(parts, "  ")
}

// splitComponentInventory separates managed components (commands, agents,
// skills, hooks) from declared-but-unmanaged components (mcpServers,
// lspServers). MCP/LSP servers are declared in the plugin manifest but are
// not materialized or lifecycle-managed by ToolHive.
func splitComponentInventory(inv plugins.ComponentInventory) (managed, declared plugins.ComponentInventory) {
	managed = plugins.ComponentInventory{}
	declared = plugins.ComponentInventory{}
	for k, v := range inv {
		switch k {
		case string(plugins.ComponentMCP), string(plugins.ComponentLSP):
			declared[k] = v
		default:
			managed[k] = v
		}
	}
	return managed, declared
}
