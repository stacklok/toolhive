// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var aiPluginBuildsFormat string

var aiPluginBuildsCmd = &cobra.Command{
	Use:   "builds",
	Short: "List locally-built plugin artifacts",
	Long:  `List all locally-built OCI plugin artifacts stored in the local OCI store.`,
	PreRunE: chainPreRunE(
		ValidateFormat(&aiPluginBuildsFormat),
	),
	RunE: aiPluginBuildsCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginBuildsCmd)

	AddFormatFlag(aiPluginBuildsCmd, &aiPluginBuildsFormat)
}

func aiPluginBuildsCmdFunc(cmd *cobra.Command, _ []string) error {
	c := newAIPluginClient(cmd.Context())

	builds, err := c.ListBuilds(cmd.Context())
	if err != nil {
		return formatAIPluginError("list builds", err)
	}

	switch aiPluginBuildsFormat {
	case FormatJSON:
		if builds == nil {
			builds = []plugins.LocalBuild{}
		}
		data, err := json.MarshalIndent(builds, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
	default:
		if len(builds) == 0 {
			fmt.Println("No locally-built plugin artifacts found")
			return nil
		}
		printAIPluginBuildsText(builds)
	}

	return nil
}

func printAIPluginBuildsText(builds []plugins.LocalBuild) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "TAG\tDIGEST\tNAME\tVERSION")

	for _, b := range builds {
		digest := b.Digest
		if len(digest) > 19 {
			digest = digest[:19] + "..."
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			b.Tag,
			digest,
			b.Name,
			b.Version,
		)
	}

	_ = w.Flush()
}
