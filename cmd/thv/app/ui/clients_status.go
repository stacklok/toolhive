package ui

import (
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/stacklok/toolhive/pkg/client"
)

// RenderClientStatusTable renders the client status table to stdout.
func RenderClientStatusTable(clientStatuses []client.MCPClientStatus) error {
	if len(clientStatuses) == 0 {
		fmt.Println("No supported clients found.")
		return nil
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.Options(
		tablewriter.WithHeader([]string{"Client Type", "Installed", "Registered"}),
		tablewriter.WithRendition(
			tw.Rendition{
				Borders: tw.Border{
					Left:   tw.State(1),
					Top:    tw.State(1),
					Right:  tw.State(1),
					Bottom: tw.State(1),
				},
			},
		),
		tablewriter.WithAlignment(tw.MakeAlign(3, tw.AlignLeft)),
	)

	for _, status := range clientStatuses {
		installed := "❌ No"
		if status.Installed {
			installed = "✅ Yes"
		}
		registered := "❌ No"
		if status.Registered {
			registered = "✅ Yes"
		}
		if err := table.Append([]string{
			string(status.ClientType),
			installed,
			registered,
		}); err != nil {
			return fmt.Errorf("failed to append row: %w", err)
		}
	}

	if err := table.Render(); err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}
	return nil
}
