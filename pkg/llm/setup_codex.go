// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/stacklok/toolhive/pkg/llmgateway"
)

// Codex on a Bedrock-backed gateway. Codex speaks only the OpenAI Responses API,
// and on Bedrock only the closed GPT models serve it, through cross-region
// inference profiles ("us.openai.gpt-6.1-sol", "global.openai.gpt-5.6-…"). The
// open-weight models Bedrock also exposes over its OpenAI-compatible API
// (nvidia.*, qwen.*, deepseek.*, openai.gpt-oss-*) serve Chat Completions only,
// so Codex gets a 404 from them. The gateway's model list cannot say which
// models serve Responses, so setup filters by ID shape, the same way Claude
// Code's tiers do (see parseBedrockModel).
//
// Codex has tiers much like Claude Code's: its built-in Bedrock provider runs
// the conversation on the flagship model and pins memory consolidation to
// terra and memory extraction to luna. Setup does the same with the Bedrock
// GPT families: sol is the main model, terra and luna the memory models.
//
// The helpers below mirror the Claude Code ones in setup.go step for step:
//
//	codexTiers                 ↔ bedrockTiers
//	parseCodexBedrockModel     ↔ parseBedrockModel
//	newestCodexTiers           ↔ newestBedrockTiers
//	resolveCodexTiers          ↔ resolveBedrockModels
//	codexDiscoveryError        ↔ bedrockDiscoveryError
//	applyCodexBedrock          ↔ applyClaudeCodeBedrock

// codexClient is the canonical client identifier for Codex. A string literal
// for the same import-cycle reason as claudeCodeClient.
const codexClient = "codex"

// codexFamilies are the Bedrock GPT families a Codex tier can pin, flagship
// first.
var codexFamilies = []string{"sol", "terra", "luna"}

// codexBedrockModelPattern matches a Responses-capable Bedrock GPT model ID: a
// "us." or "global." inference-profile prefix, the "openai." vendor segment, and
// "gpt-<major>[.<minor>]" with optional hyphenated variant tokens (captured, the
// family among them) and Bedrock revision suffix. "gpt-oss-…" does not match,
// since "oss" is not a version. Matching ignores case, as isCodexModelID does,
// so a mixed-case pin is recognised and refused by codexBedrockModelProblem
// with the lowercase ID rather than as an unknown model.
var codexBedrockModelPattern = regexp.MustCompile(
	`(?i)^(?:us|global)\.openai\.gpt-(\d+)(?:\.(\d+))?((?:-[a-z0-9]+)*)(?::\d+)?$`)

// bareBedrockGPTPattern matches a closed Bedrock GPT model ID without an
// inference-profile prefix, which Bedrock does not serve on demand.
var bareBedrockGPTPattern = regexp.MustCompile(`(?i)^openai\.gpt-\d`)

// errNoCodexBedrockModels reports a gateway model list with no
// Responses-capable Bedrock GPT model, so Codex has nothing to use.
var errNoCodexBedrockModels = errors.New("the gateway's model list has no sol, terra, or luna Bedrock GPT model " +
	"that serves the Responses API (expected us.openai.gpt-… or global.openai.gpt-…); Codex can't use Bedrock's " +
	"open-weight models, which serve only Chat Completions. Ask the gateway operator to enable one, or pin one " +
	"with --models")

// codexTiers holds the model ID pinned for each Codex tier in bedrock-compat
// mode. An empty tier is left unpinned.
type codexTiers struct {
	sol, terra, luna string
}

// slot returns the field for a family name, or nil for an unknown family.
func (t *codexTiers) slot(family string) *string {
	switch family {
	case "sol":
		return &t.sol
	case "terra":
		return &t.terra
	case "luna":
		return &t.luna
	default:
		return nil
	}
}

// emptyFamilies lists the tiers left without a pinned model ID, in a stable
// order.
func (t *codexTiers) emptyFamilies() []string {
	var empty []string
	for _, family := range codexFamilies {
		if *t.slot(family) == "" {
			empty = append(empty, family)
		}
	}
	return empty
}

// mainModel is the model Codex converses with: the sol tier, or the next tier
// down when the gateway serves no sol model. "" when every tier is empty.
func (t *codexTiers) mainModel() string {
	for _, family := range codexFamilies {
		if m := *t.slot(family); m != "" {
			return m
		}
	}
	return ""
}

// isCodexModelID reports whether a --models entry names an OpenAI GPT model,
// which makes it Codex's to pin rather than a Claude Code tier or a filter for
// another tool (Pi, Claude Desktop). Entries for other vendors (nvidia.*,
// qwen.*, ...) and OpenAI's open-weight gpt-oss models, which Codex cannot use,
// are left to those tools, so a --models list shared across tools does not
// refuse Codex.
func isCodexModelID(id string) bool {
	lower := strings.ToLower(id)
	if isOpenWeightGPT(lower) {
		return false
	}
	return strings.Contains(lower, "openai.") || strings.HasPrefix(lower, "gpt-")
}

// isOpenWeightGPT reports whether lowerID, a lowercased model ID, names one of
// OpenAI's open-weight gpt-oss models, which Bedrock serves only over Chat
// Completions.
func isOpenWeightGPT(lowerID string) bool {
	return strings.Contains(lowerID, "gpt-oss")
}

// parseCodexBedrockModel reports whether id is a Responses-capable Bedrock GPT
// model ID and, if so, its tier family ("" for a variant that names none, such
// as gpt-6-astra) and its [major, minor] version for ranking.
func parseCodexBedrockModel(id string) (family string, version []int, ok bool) {
	m := codexBedrockModelPattern.FindStringSubmatch(id)
	if m == nil {
		return "", nil, false
	}
	major, _ := strconv.Atoi(m[1])
	minor := 0
	if m[2] != "" {
		minor, _ = strconv.Atoi(m[2])
	}
	for _, tok := range strings.Split(strings.ToLower(strings.TrimPrefix(m[3], "-")), "-") {
		if slices.Contains(codexFamilies, tok) {
			family = tok
			break
		}
	}
	return family, []int{major, minor}, true
}

// codexBedrockModelProblem explains why a pinned id cannot serve Codex on
// Bedrock, or returns nil when it can.
func codexBedrockModelProblem(id string) error {
	const hint = "pin a Bedrock GPT inference profile such as us.openai.gpt-6.1-sol, " +
		"or drop it from --models to use the newest one the gateway lists"
	lower := strings.ToLower(id)
	switch {
	case codexBedrockModelPattern.MatchString(id) && id == lower:
		return nil
	case codexBedrockModelPattern.MatchString(id):
		return fmt.Errorf("--models entry %q is not lowercase; Bedrock model IDs are, so use %q", id, lower)
	case bareBedrockGPTPattern.MatchString(id):
		return fmt.Errorf("--models entry %q has no inference-profile prefix; Bedrock serves GPT models "+
			"through us. or global. profiles (e.g. us.%s)", id, lower)
	default:
		return fmt.Errorf("--models entry %q is not a Bedrock GPT inference profile; only those serve the "+
			"Responses API Codex speaks. To use Codex, %s", id, hint)
	}
}

// newestCodexTiers picks, for each family, the Responses-capable Bedrock GPT
// model in discovered with the highest [major, minor] version. Ties (the same
// model under the us. and global. profiles) go to the lexicographically
// smallest ID, the same tie-break newestBedrockTiers uses. Ranking within a
// family keeps a newer luna from ever displacing sol as the main model.
func newestCodexTiers(discovered []string) codexTiers {
	var tiers codexTiers
	best := make(map[string][]int, len(codexFamilies))
	for _, id := range discovered {
		family, version, ok := parseCodexBedrockModel(id)
		if !ok || family == "" {
			continue
		}
		dst := tiers.slot(family)
		prev, seen := best[family]
		order := slices.Compare(version, prev)
		if !seen || order > 0 || (order == 0 && id < *dst) {
			best[family] = version
			*dst = id
		}
	}
	return tiers
}

// resolveCodexTiers picks the model pinned for each Codex tier, the way
// resolveBedrockModels picks Claude Code's: each tier starts from the newest
// model of its family in discovered, and a valid --models entry for Codex (see
// isCodexModelID) replaces it by naming "sol", "terra", or "luna" in the ID.
// An entry naming no family pins the main model (the sol tier). A second entry
// for the same tier wins with a warning. Codex has no built-in Bedrock model to
// fall back to, so when no entry for Codex is valid the first invalid one is
// the error; next to a valid pin, an invalid entry is ignored with a warning,
// as resolveBedrockModels does for an entry matching no Claude Code tier. Pass
// a nil discovered to resolve the --models pins alone.
func resolveCodexTiers(models, discovered []string) (codexTiers, []string, error) {
	tiers := newestCodexTiers(discovered)
	var warnings []string
	var invalid []error
	assigned := make(map[string]string, len(codexFamilies))
	for _, m := range models {
		if !isCodexModelID(m) {
			continue
		}
		if err := codexBedrockModelProblem(m); err != nil {
			invalid = append(invalid, err)
			continue
		}
		family, _, _ := parseCodexBedrockModel(m)
		if family == "" {
			family = codexFamilies[0]
		}
		if prev, ok := assigned[family]; ok {
			warnings = append(warnings, fmt.Sprintf(
				"--models entry %q overrides %q for the Codex %s tier; only the last is used", m, prev, family))
		}
		assigned[family] = m
		*tiers.slot(family) = m
	}
	if len(assigned) == 0 && len(invalid) > 0 {
		return codexTiers{}, nil, invalid[0]
	}
	for _, err := range invalid {
		warnings = append(warnings, err.Error()+"; ignored")
	}
	return tiers, warnings, nil
}

// hasCodexPin reports whether --models holds a valid pin for Codex, so Codex
// needs no model discovery.
func hasCodexPin(models []string) bool {
	pinned, _, _ := resolveCodexTiers(models, nil)
	return pinned.mainModel() != ""
}

// applyCodexBedrock resolves Codex's tiers and writes them onto applyCfg: the
// main model (with its context window when the gateway listing reports one)
// and the terra and luna memory models, each falling back to the main model
// when the gateway serves no model of that family, so no background request
// asks for an OpenAI name a Bedrock-only gateway rejects. It surfaces override
// diagnostics and names any tier it filled from another. Discovery and the
// pre-login pin check already drop Codex when no model resolves, so the error
// here is a defensive skip.
func applyCodexBedrock(
	errOut io.Writer, applyCfg *llmgateway.ApplyConfig, models []string, discovered discoveredGatewayModels,
) error {
	tiers, warnings, err := resolveCodexTiers(models, discovered.openAI)
	if err != nil {
		return err
	}
	primary := tiers.mainModel()
	if primary == "" {
		return errNoCodexBedrockModels
	}
	for _, w := range warnings {
		_, _ = fmt.Fprintf(errOut, "Warning: %s\n", w)
	}
	if empty := tiers.emptyFamilies(); len(empty) > 0 {
		_, _ = fmt.Fprintf(errOut, "Warning: no Bedrock model found in the gateway catalogue for the Codex %s "+
			"tier(s); Codex uses %s in their place.\n", strings.Join(empty, ", "), primary)
	}
	applyCfg.BedrockCompat = true
	applyCfg.BedrockCodexModel = primary
	applyCfg.BedrockCodexContextWindow = discovered.openAIContextWindows[primary]
	applyCfg.BedrockCodexConsolidationModel = cmp.Or(tiers.terra, primary)
	applyCfg.BedrockCodexExtractModel = cmp.Or(tiers.luna, primary)
	return nil
}

// filterCodexBedrockPin refuses Codex before login when Bedrock mode is on and
// --models pins it to a model that cannot serve it: an error when Codex was
// named with --client, a warning and a skip when it was auto-detected. No
// discovery result could make such a pin usable.
func filterCodexBedrockPin(
	clients []string, targetClient string, bedrockCompat bool, models []string, errOut io.Writer,
) ([]string, error) {
	if !bedrockCompat || !slices.Contains(clients, codexClient) {
		return clients, nil
	}
	_, _, err := resolveCodexTiers(models, nil)
	if err == nil {
		return clients, nil
	}
	if targetClient == codexClient {
		return nil, fmt.Errorf("configuring Codex for Bedrock: %w", err)
	}
	_, _ = fmt.Fprintf(errOut, "Warning: skipping auto-detected codex: %v\n", err)
	return slices.DeleteFunc(slices.Clone(clients), func(c string) bool { return c == codexClient }), nil
}

// codexDiscoveryError reports why Bedrock-mode Codex cannot pick a model from
// the gateway listing, or nil when it can (or need not, because --models pins
// it). It mirrors bedrockDiscoveryError: a listing with no sol, terra, or luna
// model is as unusable as a failed fetch.
func codexDiscoveryError(needsCodexModel bool, models, discovered []string, openErr error) error {
	if !needsCodexModel || hasCodexPin(models) {
		return nil
	}
	if openErr != nil {
		return openErr
	}
	if newestCodexTiers(discovered) == (codexTiers{}) {
		return errNoCodexBedrockModels
	}
	return nil
}
