// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Command demo is the automated setup phase of the ToolHive Memory recruiter demo.
//
// It handles Phase 1 (upload the job description as a static MCP Resource and
// print it in full) and Phase 2 (write shared semantic memories). Phases 3-7
// are run as real Claude Code agent sessions — see the Makefile targets.
//
// Configuration via environment variables:
//
//	MEMORY_MCP_URL  — MCP endpoint (default: http://127.0.0.1:8765/mcp)
//	MEMORY_API_URL  — Resources REST endpoint (default: http://127.0.0.1:8765/api/resources)
//	MEMORY_JD_FILE  — Path to job description file (default: data/job-description.txt)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// ─── configuration ───────────────────────────────────────────────────────────

var (
	mcpURL = envOr("MEMORY_MCP_URL", "http://127.0.0.1:8765/mcp")
	apiURL = envOr("MEMORY_API_URL", "http://127.0.0.1:8765/api/resources")
	jdFile = envOr("MEMORY_JD_FILE", "data/job-description.txt")
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ─── ANSI helpers ─────────────────────────────────────────────────────────────

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiPurple = "\033[35m"
	ansiCyan   = "\033[36m"
	ansiWhite  = "\033[97m"
	ansiGray   = "\033[90m"
)

func col(color, s string) string { return color + s + ansiReset }

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()

	jd, err := os.ReadFile(jdFile)
	if err != nil {
		fatalf("reading %s: %v\n  Hint: run from demo/recruiter/ directory", jdFile, err)
	}

	printBanner()

	// ── Phase 1 · Resource ────────────────────────────────────────────────────
	phase(1, "Resources", "Upload the job description as a static MCP Resource (read-only to agents)")
	resID := uploadResource(ctx, jd)
	printJobDescription(jd)
	pause()

	// ── Phase 2 · Semantic Memory ─────────────────────────────────────────────
	phase(2, "Semantic Memory", "Company-wide facts — written once, recalled by any agent session at any time")
	cl := newSession(ctx, "setup")
	defer cl.Close()

	remember(ctx, cl, "semantic",
		"Company does not sponsor US work visas for any engineering role",
		"policy", "visa", "hiring")
	remember(ctx, cl, "semantic",
		"Senior Go Engineer base salary band: $100,000–$150,000 USD; total comp includes equity",
		"compensation", "hiring", "senior-go-engineer")
	remember(ctx, cl, "semantic",
		"Engineering team is fully remote, US timezone preferred (EST/PST). Async-first culture.",
		"remote", "culture", "hiring")
	pause()

	printHandoff(resID)
}

// ─── MCP helpers ──────────────────────────────────────────────────────────────

func newSession(ctx context.Context, name string) *mcpclient.Client {
	t, err := transport.NewStreamableHTTP(mcpURL)
	if err != nil {
		fatalf("transport (%s): %v", name, err)
	}
	cl := mcpclient.NewClient(t)
	if err := cl.Start(ctx); err != nil {
		fatalf("start (%s): %v", name, err)
	}
	if _, err := cl.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "demo-" + name, Version: "1.0"},
		},
	}); err != nil {
		fatalf("initialize (%s): %v", name, err)
	}
	fmt.Printf("  %s  Session opened: %s\n", col(ansiGray, "→"), col(ansiBold+ansiCyan, name))
	return cl
}

func callTool(ctx context.Context, cl *mcpclient.Client, tool string, args map[string]any) string {
	result, err := cl.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: tool, Arguments: args},
	})
	if err != nil {
		fatalf("tool/%s: %v", tool, err)
	}
	for _, content := range result.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func remember(ctx context.Context, cl *mcpclient.Client, memType, content string, tags ...string) string {
	raw := callTool(ctx, cl, "memory_remember", map[string]any{
		"content": content,
		"type":    memType,
		"author":  "human",
		"tags":    tags,
	})
	// RememberResult serialises without json tags: {"MemoryID":"...","Conflicts":null}
	var resp struct {
		MemoryID  string `json:"MemoryID"`
		Conflicts []any  `json:"Conflicts"`
	}
	_ = json.Unmarshal([]byte(raw), &resp)

	icon := typeIcon(memType)
	fmt.Printf("\n  %s %s %s\n",
		icon,
		col(ansiBold+typeColor(memType), "["+memType+"]"),
		truncate(content, 72))
	if resp.MemoryID != "" {
		fmt.Printf("      %sid=%-36s tags=%v%s\n", ansiGray, resp.MemoryID, tags, ansiReset)
	}
	return resp.MemoryID
}

// ─── Resources REST API helper ─────────────────────────────────────────────────

func uploadResource(ctx context.Context, content []byte) string {
	body, _ := json.Marshal(map[string]any{
		"content": string(content),
		"type":    "semantic",
		"tags":    []string{"job-description", "senior-go-engineer", "hiring"},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("POST %s: %v", apiURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		fatalf("POST /api/resources returned %d: %v", resp.StatusCode, errBody["error"])
	}

	var r struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)

	fmt.Printf("\n  %s POST /api/resources  ←  Senior Go Engineer job description\n", col(ansiGray, "→"))
	fmt.Printf("  %s Resource registered: %s\n", col(ansiGreen, "✓"), col(ansiCyan, r.ID))
	fmt.Printf("  %s Agents discover it via memory_search or MCP resources/list\n", col(ansiGreen, "✓"))
	return r.ID
}

func printJobDescription(content []byte) {
	divider := col(ansiDim, strings.Repeat("─", 64))
	fmt.Printf("\n%s\n", divider)
	for _, line := range strings.Split(string(content), "\n") {
		fmt.Printf("  %s%s%s\n", ansiGray, line, ansiReset)
	}
	fmt.Printf("%s\n", divider)
}

// ─── display helpers ─────────────────────────────────────────────────────────

func printBanner() {
	bar := strings.Repeat("═", 64)
	fmt.Printf("\n%s\n", col(ansiBold+ansiCyan, bar))
	fmt.Printf("%s\n", col(ansiBold+ansiCyan, "  ToolHive Memory Demo — The Recruiter"))
	fmt.Printf("%s\n", col(ansiCyan, "  Scenario: Hiring a Senior Go Engineer at Stacklok"))
	fmt.Printf("%s\n\n", col(ansiBold+ansiCyan, bar))
	fmt.Printf("  Server : %s\n\n", col(ansiGray, mcpURL))
}

func phase(n int, title, subtitle string) {
	bar := strings.Repeat("─", 64)
	fmt.Printf("\n%s\n", col(ansiYellow, bar))
	fmt.Printf("  %s\n", col(ansiBold+ansiWhite, fmt.Sprintf("Phase %d · %s", n, title)))
	if subtitle != "" {
		fmt.Printf("  %s%s%s\n", ansiGray, subtitle, ansiReset)
	}
	fmt.Printf("%s\n", col(ansiYellow, bar))
}

func printHandoff(resourceID string) {
	bar := strings.Repeat("═", 64)
	fmt.Printf("\n\n%s\n", col(ansiBold+ansiGreen, bar))
	fmt.Printf("%s\n", col(ansiBold+ansiGreen, "  Setup complete — memory server is primed"))
	fmt.Printf("%s\n\n", col(ansiBold+ansiGreen, bar))
	fmt.Printf("  Resource : %s\n", col(ansiCyan, resourceID))
	fmt.Printf("  Semantic : 3 company-wide facts written\n\n")
	fmt.Printf("  %sNext: run the agent sessions to see Claude use the memory:%s\n\n", ansiBold, ansiReset)
	fmt.Printf("    %smake session-recruiter-alice%s   — recruiter records Alice Chen's interview\n", ansiCyan, ansiReset)
	fmt.Printf("    %smake session-hiring-manager%s    — hiring manager searches cold\n", ansiCyan, ansiReset)
	fmt.Printf("    %smake session-recruiter-bob%s     — recruiter records Bob + procedural lesson\n", ansiCyan, ansiReset)
	fmt.Printf("    %smake session-recruiter-charlie%s — recruiter records Charlie (HIRE)\n", ansiCyan, ansiReset)
	fmt.Printf("    %smake session-crystallize%s       — crystallize phone-screen pattern → Skill\n\n", ansiCyan, ansiReset)
	fmt.Printf("    %smake demo%s                      — run all sessions in sequence\n\n", ansiPurple, ansiReset)
}

func typeIcon(t string) string {
	switch t {
	case "semantic":
		return "🧠"
	case "episodic":
		return "📅"
	case "procedural":
		return "📋"
	default:
		return "💾"
	}
}

func typeColor(t string) string {
	switch t {
	case "semantic":
		return ansiCyan
	case "episodic":
		return ansiYellow
	case "procedural":
		return ansiPurple
	default:
		return ansiWhite
	}
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func pause() { time.Sleep(200 * time.Millisecond) }

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, col(ansiGreen, "ERROR: ")+format+"\n", args...)
	os.Exit(1)
}
