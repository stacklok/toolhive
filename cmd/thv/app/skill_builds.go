// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
)

var skillBuildsFormat string

var skillBuildsCmd = &cobra.Command{
	Use:   "builds",
	Short: "List locally-built skill artifacts",
	Long:  `List all locally-built OCI skill artifacts stored in the local OCI store.`,
	PreRunE: chainPreRunE(
		ValidateFormat(&skillBuildsFormat),
	),
	RunE: skillBuildsCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillBuildsCmd)

	AddFormatFlag(skillBuildsCmd, &skillBuildsFormat)
}

func skillBuildsCmdFunc(cmd *cobra.Command, _ []string) error {
	c := newSkillClient(cmd.Context())

	builds, err := c.ListBuilds(cmd.Context())
	if err != nil {
		return formatSkillError("list builds", err)
	}

	switch skillBuildsFormat {
	case FormatJSON:
		if builds == nil {
			builds = []skills.LocalBuild{}
		}
		data, err := json.MarshalIndent(builds, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
	default:
		if len(builds) == 0 {
			fmt.Println("No locally-built skill artifacts found")
			return nil
		}
		printSkillBuildsText(builds)
	}

	return nil
}

func printSkillBuildsText(builds []skills.LocalBuild) {
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
