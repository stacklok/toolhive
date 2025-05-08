// Package main is the entry point for the ToolHive CLI Doc Generator.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	cli "github.com/stacklok/toolhive/cmd/thv/app"
)

func main() {
	var dir string
	root := &cobra.Command{
		Use:          "gendoc",
		Short:        "Generate ToolHive's help docs",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return doc.GenMarkdownTree(cli.NewRootCmd(), dir)
		},
	}
	root.Flags().StringVarP(&dir, "dir", "d", "doc", "Path to directory in which to generate docs")
	if err := root.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
