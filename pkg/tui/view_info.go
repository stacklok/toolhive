// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/runner"
)

// renderInfo renders key-value info for the selected workload, enriched with RunConfig.
func renderInfo(w *core.Workload, cfg *runner.RunConfig, width int) string {
	_ = width
	styles := infoStyles{
		dim2:   lipgloss.NewStyle().Foreground(ui.ColorDim2),
		text:   lipgloss.NewStyle().Foreground(ui.ColorText),
		dim:    lipgloss.NewStyle().Foreground(ui.ColorDim),
		cyan:   lipgloss.NewStyle().Foreground(ui.ColorCyan),
		yellow: lipgloss.NewStyle().Foreground(ui.ColorYellow),
		green:  lipgloss.NewStyle().Foreground(ui.ColorGreen),
	}

	var lines []string
	lines = append(lines, renderInfoRuntime(w, styles)...)
	if cfg == nil {
		lines = append(lines, "\n"+styles.dim2.Render("  Loading config…"))
		return strings.Join(lines, "\n")
	}
	lines = append(lines, renderInfoConfig(cfg, styles)...)
	return strings.Join(lines, "\n")
}

type infoStyles struct {
	dim2, text, dim, cyan, yellow, green lipgloss.Style
}

func (s infoStyles) row(key, val string) string {
	return s.dim2.Render(fmt.Sprintf("  %-14s", key)) + s.text.Render(val)
}

func (s infoStyles) section(title string) string {
	return "\n" + s.dim.Render("  "+strings.Repeat("─", 30)) + "\n" +
		s.dim.Render(fmt.Sprintf("  %s", strings.ToUpper(title))) + "\n"
}

func renderInfoRuntime(w *core.Workload, s infoStyles) []string {
	lines := []string{s.section("Runtime")}
	lines = append(lines, s.row("Name", w.Name))
	lines = append(lines, s.row("Status", string(w.Status)))
	lines = append(lines, s.row("URL", w.URL))
	lines = append(lines, s.row("Port", fmt.Sprintf("%d", w.Port)))
	lines = append(lines, s.row("Transport", string(w.TransportType)))
	if w.Group != "" {
		lines = append(lines, s.row("Group", w.Group))
	}
	if w.Remote {
		lines = append(lines, s.row("Remote", "yes"))
	}
	lines = append(lines, s.row("Created", w.CreatedAt.Format("2006-01-02 15:04:05")))
	return lines
}

func renderInfoConfig(cfg *runner.RunConfig, s infoStyles) []string {
	var lines []string
	if cfg.Image != "" {
		lines = append(lines, s.section("Image"))
		lines = append(lines, s.row("Image", cfg.Image))
	}
	if len(cfg.EnvVars) > 0 {
		lines = append(lines, s.section("Environment"))
		for k, v := range cfg.EnvVars {
			lines = append(lines, s.cyan.Render(fmt.Sprintf("  %-16s", k))+s.dim2.Render(v))
		}
	}
	if len(cfg.Volumes) > 0 {
		lines = append(lines, s.section("Volumes"))
		for _, v := range cfg.Volumes {
			lines = append(lines, renderInfoVolumeLine(v, s))
		}
	}
	if len(cfg.Secrets) > 0 {
		lines = append(lines, s.section("Secrets"))
		for _, sec := range cfg.Secrets {
			lines = append(lines, "  "+s.yellow.Render(sec))
		}
	}
	if cfg.PermissionProfile != nil {
		lines = append(lines, renderInfoPermissions(cfg, s)...)
	}
	return lines
}

func renderInfoVolumeLine(v string, s infoStyles) string {
	parts := strings.SplitN(v, ":", 3)
	mode := ""
	if len(parts) == 3 {
		mode = " " + s.dim.Render("["+parts[2]+"]")
	}
	host := s.dim2.Render(fmt.Sprintf("  %-24s", parts[0]))
	arrow := s.dim.Render("→ ")
	var cont string
	if len(parts) >= 2 {
		cont = s.text.Render(parts[1])
	}
	return host + arrow + cont + mode
}

func renderInfoPermissions(cfg *runner.RunConfig, s infoStyles) []string {
	lines := []string{s.section("Permissions")}
	outbound := cfg.PermissionProfile.Network.Outbound
	prefix := "  " + s.dim2.Render("network outbound  ")
	switch {
	case outbound.InsecureAllowAll:
		lines = append(lines, prefix+s.yellow.Render("allow all"))
	case len(outbound.AllowHost) > 0:
		lines = append(lines, prefix+s.green.Render(strings.Join(outbound.AllowHost, ", ")))
	default:
		lines = append(lines, prefix+s.dim.Render("denied"))
	}
	return lines
}
