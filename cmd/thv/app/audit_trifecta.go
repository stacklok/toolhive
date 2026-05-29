// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
	llmclient "github.com/stacklok/toolhive/pkg/llm/client"
	"github.com/stacklok/toolhive/pkg/toxicflow"
)

// auditTrifectaCmd is hidden while the heuristics are being calibrated. It
// audits a group (a set of MCP servers sharing one agent context) for
// "lethal trifecta" risk: the co-location of private-data access, untrusted-
// content exposure, and an exfiltration path.
var auditTrifectaCmd = &cobra.Command{
	Use:    "audit-trifecta [group]",
	Short:  "Audit a group of MCP servers for lethal-trifecta risk (experimental)",
	Hidden: true,
	Long: `Audit a ToolHive group for "lethal trifecta" risk.

The lethal trifecta is the co-location, within a single agent context, of:
  - access to private data,
  - exposure to untrusted content (a prompt-injection vector), and
  - the ability to exfiltrate data externally.

Because every server in a group shares the model's context, a toxic flow exists
whenever the group contains all three. This command classifies each server from
its permission profile and registry metadata and reports the group verdict.

ToolHive can assess data access and egress with reasonable confidence (they come
from the permission profile) but untrusted-content exposure poorly; expect an
"indeterminate" verdict until you classify or override the relevant servers.

Examples:
  # Audit one group
  thv audit-trifecta research

  # Audit every group as JSON
  thv audit-trifecta --all --format json

  # Apply overrides for mis-classified servers
  thv audit-trifecta research --overrides ./trifecta-overrides.json`,
	Args:              cobra.MaximumNArgs(1),
	RunE:              auditTrifectaCmdFunc,
	ValidArgsFunction: completeTrifectaGroup,
}

// completeTrifectaGroup completes the group argument with known group names.
func completeTrifectaGroup(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, err := toxicflow.ListGroupNames(cmd.Context())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

var (
	auditTrifectaAll       bool
	auditTrifectaFormat    string
	auditTrifectaExplain   bool
	auditTrifectaOverrides string
	auditTrifectaLive      bool
	auditTrifectaLLMModel  string
	auditTrifectaLLMURL    string
	auditTrifectaLLMKey    string
	auditTrifectaLLMProxy  bool
)

// trifectaLLMKeyEnv is the fallback source for the LLM API key so it need not
// be passed on the command line.
const trifectaLLMKeyEnv = "THV_AUDIT_LLM_API_KEY"

func init() {
	AddFormatFlag(auditTrifectaCmd, &auditTrifectaFormat, FormatJSON, FormatText)
	auditTrifectaCmd.Flags().BoolVar(&auditTrifectaAll, "all", false, "Audit every group")
	auditTrifectaCmd.Flags().BoolVar(&auditTrifectaExplain, "explain", false, "Show the evidence behind each finding")
	auditTrifectaCmd.Flags().StringVar(&auditTrifectaOverrides, "overrides", "",
		"Path to a JSON file of role overrides for mis-classified servers")
	auditTrifectaCmd.Flags().BoolVar(&auditTrifectaLive, "live", false,
		"Probe running servers for live tool annotations (openWorldHint)")
	auditTrifectaCmd.Flags().StringVar(&auditTrifectaLLMModel, "llm-model", "",
		"LLM model for untrusted-content inference (falls back to keyword search if unset)")
	auditTrifectaCmd.Flags().StringVar(&auditTrifectaLLMURL, "llm-base-url", "",
		"OpenAI-compatible base URL for the LLM (e.g. https://api.openai.com/v1)")
	auditTrifectaCmd.Flags().StringVar(&auditTrifectaLLMKey, "llm-api-key", "",
		"API key for the LLM (or set "+trifectaLLMKeyEnv+")")
	auditTrifectaCmd.Flags().BoolVar(&auditTrifectaLLMProxy, "llm-proxy", false,
		"Use the running `thv llm` proxy for inference (handles auth; needs --llm-model)")
	auditTrifectaCmd.PreRunE = ValidateFormat(&auditTrifectaFormat, FormatJSON, FormatText)
}

// buildTrifectaInference selects the inference strategy: the `thv llm` proxy
// when --llm-proxy is set, else an LLM when a model and base URL are supplied
// (the API key is optional), else the offline keyword search. A partial LLM
// config falls back to keywords with a warning so a missing setting never
// silently disables inference the user asked for.
func buildTrifectaInference() (toxicflow.SourceInference, error) {
	// Prefer the running `thv llm` proxy when asked: it injects auth and
	// forwards to the configured gateway, so no key or base URL is needed here.
	if auditTrifectaLLMProxy {
		return llmProxyInference()
	}

	key := auditTrifectaLLMKey
	if key == "" {
		key = os.Getenv(trifectaLLMKeyEnv)
	}
	model, baseURL := auditTrifectaLLMModel, auditTrifectaLLMURL

	// An explicit LLM needs a model and base URL; the key is optional (for
	// keyless local models). With nothing set, fall back to keyword search.
	switch {
	case model == "" && baseURL == "" && key == "":
		return toxicflow.NewKeywordInference(), nil
	case model == "" || baseURL == "":
		fmt.Fprintln(os.Stderr,
			"warning: incomplete LLM config (need --llm-model and --llm-base-url, or --llm-proxy); using keyword search")
		return toxicflow.NewKeywordInference(), nil
	}

	c, err := llmclient.New(llmclient.Config{Model: model, BaseURL: baseURL, APIKey: key})
	if err != nil {
		return nil, fmt.Errorf("configure LLM: %w", err)
	}
	return toxicflow.NewLLMInference(c), nil
}

// llmProxyInference builds an inference backend that talks to the local
// `thv llm` reverse proxy. The proxy must be running (`thv llm proxy`); it
// injects the OIDC token and forwards to the configured gateway.
func llmProxyInference() (toxicflow.SourceInference, error) {
	if auditTrifectaLLMModel == "" {
		return nil, fmt.Errorf("--llm-model is required with --llm-proxy")
	}
	if auditTrifectaLLMURL != "" || auditTrifectaLLMKey != "" {
		fmt.Fprintln(os.Stderr, "warning: --llm-base-url/--llm-api-key are ignored with --llm-proxy")
	}

	llmCfg := config.NewDefaultProvider().GetConfig().LLM
	if !llmCfg.IsConfigured() {
		return nil, fmt.Errorf("no thv llm gateway configured: run \"thv llm config set\" and start the proxy with \"thv llm proxy\"")
	}

	baseURL := llmCfg.ProxyBaseURL()
	fmt.Fprintf(os.Stderr, "using thv llm proxy at %s (model %s)\n", baseURL, auditTrifectaLLMModel)

	c, err := llmclient.New(llmclient.Config{Model: auditTrifectaLLMModel, BaseURL: baseURL})
	if err != nil {
		return nil, fmt.Errorf("configure LLM proxy: %w", err)
	}
	return toxicflow.NewLLMInference(c), nil
}

func auditTrifectaCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	if !auditTrifectaAll && len(args) == 0 {
		return fmt.Errorf("specify a group name or use --all")
	}

	overrides, err := loadTrifectaOverrides(auditTrifectaOverrides)
	if err != nil {
		return err
	}

	inference, err := buildTrifectaInference()
	if err != nil {
		return err
	}

	collector, err := toxicflow.NewCollector(ctx, inference, auditTrifectaLive)
	if err != nil {
		return fmt.Errorf("failed to set up audit: %w", err)
	}

	// Resolve the groups to audit. Validate a named group exists so a typo is
	// reported as "not found" rather than silently auditing zero servers and
	// reading as a reassuring "no toxic flow".
	known, err := toxicflow.ListGroupNames(ctx)
	if err != nil {
		return err
	}
	groupNames := known
	if !auditTrifectaAll {
		if !slices.Contains(known, args[0]) {
			return fmt.Errorf("group %q not found (run \"thv group list\")", args[0])
		}
		groupNames = []string{args[0]}
	}

	results := make([]toxicflow.GroupAssessment, 0, len(groupNames))
	for _, g := range groupNames {
		assessment, err := collector.AssessGroup(ctx, g, overrides)
		if err != nil {
			return fmt.Errorf("failed to audit group %q: %w", g, err)
		}
		results = append(results, assessment)
	}

	warnUnknownOverrideTargets(overrides, results)

	if auditTrifectaFormat == FormatJSON {
		return printTrifectaJSON(results)
	}
	printTrifectaText(results)
	return nil
}

// loadTrifectaOverrides reads a JSON array of overrides from path, or returns
// nil when no path is given.
func loadTrifectaOverrides(path string) ([]toxicflow.Override, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-supplied CLI flag
	if err != nil {
		return nil, fmt.Errorf("failed to read overrides file: %w", err)
	}
	var overrides []toxicflow.Override
	if err := json.Unmarshal(data, &overrides); err != nil {
		return nil, fmt.Errorf("failed to parse overrides file: %w", err)
	}
	for _, o := range overrides {
		if err := toxicflow.ValidateOverride(o); err != nil {
			return nil, err
		}
	}
	return overrides, nil
}

// warnUnknownOverrideTargets prints a warning for any override that names a
// server not present in the audited groups, so a typo does not silently
// fail open.
func warnUnknownOverrideTargets(overrides []toxicflow.Override, results []toxicflow.GroupAssessment) {
	seen := map[string]bool{}
	for _, a := range results {
		for _, s := range a.Servers {
			seen[s.Name] = true
		}
	}
	for _, o := range overrides {
		if o.Server != "" && !seen[o.Server] {
			fmt.Fprintf(os.Stderr, "warning: override targets server %q, which is not in the audited group(s)\n", o.Server)
		}
	}
}

func printTrifectaJSON(results []toxicflow.GroupAssessment) error {
	if results == nil {
		results = []toxicflow.GroupAssessment{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func printTrifectaText(results []toxicflow.GroupAssessment) {
	if len(results) == 0 {
		fmt.Println("No groups to audit.")
		return
	}
	for i, a := range results {
		if i > 0 {
			fmt.Println()
		}
		printGroupAssessment(a)
	}
}

func printGroupAssessment(a toxicflow.GroupAssessment) {
	fmt.Printf("Group: %s   verdict: %s\n", a.Group, strings.ToUpper(string(a.Verdict)))

	if len(a.Servers) == 0 {
		fmt.Println("  (no servers in group)")
		return
	}

	overridden := false
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "  SERVER\tDATA\tSOURCE\tSINK")
	for _, s := range a.Servers {
		_, _ = fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n",
			s.Name,
			cell(s.Finding(toxicflow.RoleData)),
			cell(s.Finding(toxicflow.RoleSource)),
			cell(s.Finding(toxicflow.RoleSink)))
		overridden = overridden || anyOverridden(s)
	}
	_ = w.Flush()

	if overridden {
		fmt.Println("  * value set by override — re-run with --explain for the reason")
	}

	if auditTrifectaExplain {
		printTrifectaEvidence(a)
	}

	fmt.Printf("  Private data    : %s\n", joinOrNone(a.DataHolders))
	fmt.Printf("  Untrusted intake: %s\n", joinOrNone(a.Sources))
	fmt.Printf("  Exfil sink      : %s\n", joinOrNone(a.Sinks))
	if len(a.Unclassified) > 0 {
		fmt.Printf("  Unclassified    : %s (untrusted-content exposure unknown)\n",
			strings.Join(a.Unclassified, ", "))
	}

	fmt.Printf("\n  %s\n", trifectaVerdictMessage(a))
}

func printTrifectaEvidence(a toxicflow.GroupAssessment) {
	fmt.Println("  evidence:")
	for _, s := range a.Servers {
		for _, role := range toxicflow.AllRoles {
			f := s.Finding(role)
			for _, ev := range f.Evidence {
				fmt.Printf("    %s [%s]: %s\n", s.Name, role, ev)
			}
		}
	}
}

// trifectaVerdictMessage returns an actionable one-liner for the verdict.
func trifectaVerdictMessage(a toxicflow.GroupAssessment) string {
	switch a.Verdict {
	case toxicflow.VerdictPresent:
		if len(a.SelfContainedFlow) > 0 {
			return fmt.Sprintf("WARNING: Toxic flow present. %s hold(s) all three roles alone — "+
				"a prompt injection there could read private data and exfiltrate it. "+
				"Cheapest fix: tighten that server's permission profile (restrict egress, "+
				"drop mounts), no regrouping needed.", strings.Join(a.SelfContainedFlow, ", "))
		}
		return "WARNING: Toxic flow present. A prompt injection via an untrusted-content " +
			"server could read private data and exfiltrate it. Split the group so the " +
			"untrusted-content server no longer shares a context with the data/exfil servers."
	case toxicflow.VerdictPossible:
		return "WARNING: Possible toxic flow. All three roles are present, some at low " +
			"confidence. Review the contributing servers above."
	case toxicflow.VerdictIndeterminate:
		return "REVIEW: Indeterminate. Data and exfil paths exist, but untrusted-content " +
			"exposure could not be determined. Classify or override the unclassified " +
			"servers to resolve."
	case toxicflow.VerdictNone:
		return "OK: No toxic flow. At least one leg is confidently absent."
	default:
		return ""
	}
}

// cell renders a finding's confidence for the table, marking overrides with a *.
func cell(f toxicflow.RoleFinding) string {
	if f.Overridden {
		return string(f.Confidence) + "*"
	}
	return string(f.Confidence)
}

// anyOverridden reports whether any of the server's findings was overridden.
func anyOverridden(s toxicflow.ServerAssessment) bool {
	for _, role := range toxicflow.AllRoles {
		if s.Finding(role).Overridden {
			return true
		}
	}
	return false
}

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}
