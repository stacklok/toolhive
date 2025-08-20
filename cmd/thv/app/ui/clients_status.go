package ui

import (
	"fmt"
	"os"
	"sort"
	"strings"

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

// RegisteredClient represents a registered client with its associated groups
type RegisteredClient struct {
	Name   string
	Groups []string
}

// RenderRegisteredClientsTable renders the registered clients table to stdout.
func RenderRegisteredClientsTable(registeredClients []RegisteredClient, hasGroups bool) error {
	if len(registeredClients) == 0 {
		fmt.Println("No clients are currently registered.")
		return nil
	}

	// Sort clients alphabetically by name
	sort.Slice(registeredClients, func(i, j int) bool {
		return registeredClients[i].Name < registeredClients[j].Name
	})

	table := tablewriter.NewWriter(os.Stdout)

	var headers []string
	if hasGroups {
		headers = []string{"Client Type", "Groups"}
	} else {
		headers = []string{"Client Type"}
	}

	table.Options(
		tablewriter.WithHeader(headers),
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
		tablewriter.WithAlignment(tw.MakeAlign(len(headers), tw.AlignLeft)),
	)

	for _, regClient := range registeredClients {
		var row []string
		if hasGroups {
			groupsStr := ""
			if len(regClient.Groups) == 0 {
				// In practice, we should never get here
				groupsStr = "(no groups)"
			} else {
				// Sort groups alphabetically for consistency
				sortedGroups := make([]string, len(regClient.Groups))
				copy(sortedGroups, regClient.Groups)
				sort.Strings(sortedGroups)
				groupsStr = strings.Join(sortedGroups, ", ")
			}
			row = []string{regClient.Name, groupsStr}
		} else {
			row = []string{regClient.Name}
		}

		if err := table.Append(row); err != nil {
			return fmt.Errorf("failed to append row: %w", err)
		}
	}

	if err := table.Render(); err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}
	return nil
}
