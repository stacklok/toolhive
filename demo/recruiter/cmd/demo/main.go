// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Command demo runs the ToolHive Memory recruiter scenario.
//
// It exercises all four memory types (semantic, episodic, procedural, resource),
// demonstrates cross-session recall between a recruiter and a hiring manager,
// and finishes by crystallizing the learned interview pattern into a Skill scaffold.
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
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiPurple = "\033[35m"
	ansiCyan   = "\033[36m"
	ansiWhite  = "\033[97m"
	ansiGray   = "\033[90m"
)

func c(color, s string) string { return color + s + ansiReset }

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()

	jd, err := os.ReadFile(jdFile)
	if err != nil {
		fatalf("reading %s: %v\n  Hint: run 'make demo' from demo/recruiter/", jdFile, err)
	}

	printBanner()

	// ── Phase 1 · Resource ────────────────────────────────────────────────────
	phase(1, "Resources", "Upload the job description as a static MCP Resource (read-only to agents)")
	resID := uploadResource(ctx, jd)
	pause()

	// ── Phase 2 · Semantic Memory ─────────────────────────────────────────────
	phase(2, "Semantic Memory", "Company-wide facts — any agent session can recall these at any time")
	shared := newSession(ctx, "shared")
	defer shared.Close()

	remember(ctx, shared, "semantic",
		"Company does not sponsor US work visas for any engineering role",
		"policy", "visa", "hiring")
	remember(ctx, shared, "semantic",
		"Senior Go Engineer base salary band: $100,000–$150,000 USD; total comp includes equity",
		"compensation", "hiring", "senior-go-engineer")
	remember(ctx, shared, "semantic",
		"Engineering team is fully remote, US timezone preferred (EST/PST). Async-first culture.",
		"remote", "culture", "hiring")
	pause()

	// ── Phase 3 · Session 1 — Recruiter: Alice Chen (2026-04-24) ─────────────
	phase(3, "Session 1 · Recruiter  —  Interview: Alice Chen", "2026-04-24")
	recruiter := newSession(ctx, "recruiter")
	defer recruiter.Close()

	remember(ctx, recruiter, "episodic",
		"Interviewed Alice Chen on 2026-04-24 for Senior Go Engineer. "+
			"Strong distributed systems background (8 years Go, ex-Cloudflare). "+
			"Struggled under time pressure on the consensus algorithm question. "+
			"Technical screen: pass. Moved to final round with hiring manager.",
		"alice-chen", "interview", "2026-04-24", "final-round")
	remember(ctx, recruiter, "episodic",
		"Alice Chen requires H1B visa sponsorship — ineligible per company policy. "+
			"Hiring manager loop should be cancelled to avoid wasting candidate and interviewer time.",
		"alice-chen", "visa", "blocker", "2026-04-24")
	pause()

	// ── Phase 4 · Session 2 — Hiring Manager: cold search ────────────────────
	phase(4, "Session 2 · Hiring Manager  —  Searching memory before the Alice call",
		"Brand new session — no prior context loaded")
	hm := newSession(ctx, "hiring-manager")
	defer hm.Close()

	search(ctx, hm, "Alice Chen Senior Go Engineer interview")
	search(ctx, hm, "visa sponsorship policy")
	pause()

	// ── Phase 5 · Session 1 — Recruiter: Bob Martinez (2026-04-25) ───────────
	phase(5, "Session 1 · Recruiter  —  Interview: Bob Martinez", "2026-04-25")
	remember(ctx, recruiter, "episodic",
		"Interviewed Bob Martinez on 2026-04-25 for Senior Go Engineer. "+
			"Strong full-stack background but limited distributed systems experience. "+
			"Expects $160K base — above the $150K band ceiling. Not progressing.",
		"bob-martinez", "interview", "2026-04-25")
	proc1 := remember(ctx, recruiter, "procedural",
		"Always clarify visa status and salary expectations within the first 15 minutes of "+
			"every phone screen. Two candidates in this cycle consumed significant recruiter "+
			"and hiring manager time before disqualifying on logistics.",
		"phone-screen", "process", "hiring", "lesson-learned")
	pause()

	// ── Phase 6 · Session 1 — Recruiter: Charlie Kim (2026-04-26) ────────────
	phase(6, "Session 1 · Recruiter  —  Interview: Charlie Kim", "2026-04-26  ·  HIRE")
	remember(ctx, recruiter, "episodic",
		"Interviewed Charlie Kim on 2026-04-26 for Senior Go Engineer. "+
			"US citizen, no sponsorship needed. 6 years Go, strong Kubernetes and controller-runtime experience. "+
			"Accepted $135K offer. RECOMMENDED FOR HIRE.",
		"charlie-kim", "interview", "hire", "2026-04-26")
	pause()

	// ── Phase 7 · Crystallize → Skill ─────────────────────────────────────────
	phase(7, "Crystallize  →  Skill", "Promoting the phone-screen pattern to a reusable Skill scaffold")
	crystallize(ctx, recruiter, "go-eng-phone-screen", proc1)
	pause()

	printSummary(resID)
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
	fmt.Printf("  %s  Session opened: %s%s\n", c(ansiGray, "→"), c(ansiBold+ansiCyan, name), ansiReset)
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
	// RememberResult serialises as {"MemoryID":"...","Conflicts":null} (no json tags).
	var resp struct {
		MemoryID  string `json:"MemoryID"`
		Conflicts []any  `json:"Conflicts"`
	}
	_ = json.Unmarshal([]byte(raw), &resp)

	icon := typeIcon(memType)
	fmt.Printf("\n  %s %s %s\n",
		icon,
		c(ansiBold+typeColor(memType), "["+memType+"]"),
		truncate(content, 72))
	if resp.MemoryID != "" {
		fmt.Printf("      %sid=%-28s tags=%v%s\n", ansiGray, resp.MemoryID, tags, ansiReset)
	}
	return resp.MemoryID
}

func search(ctx context.Context, cl *mcpclient.Client, query string) {
	fmt.Printf("\n  %s %s\n", c(ansiYellow, "🔍"), c(ansiBold, "search: ")+c(ansiCyan, `"`+query+`"`))

	raw := callTool(ctx, cl, "memory_search", map[string]any{
		"query": query,
		"top_k": 3,
	})

	// ScoredEntry serialises as {"Entry":{...},"Similarity":0.xx} (no json tags).
	var results []struct {
		Entry      struct {
			ID      string `json:"ID"`
			Content string `json:"Content"`
			Type    string `json:"Type"`
		} `json:"Entry"`
		Similarity float64 `json:"Similarity"`
	}
	if err := json.Unmarshal([]byte(raw), &results); err != nil || len(results) == 0 {
		fmt.Printf("     %sno results%s\n", ansiGray, ansiReset)
		return
	}
	for _, r := range results {
		fmt.Printf("    %s %s %s %s\n",
			c(ansiGray, fmt.Sprintf("%.2f", r.Similarity)),
			simBar(r.Similarity),
			c(ansiDim+typeColor(r.Entry.Type), "["+r.Entry.Type+"]"),
			truncate(r.Entry.Content, 62))
	}
}

func crystallize(ctx context.Context, cl *mcpclient.Client, skillName string, ids ...string) {
	raw := callTool(ctx, cl, "memory_crystallize", map[string]any{
		"ids":  ids,
		"name": skillName,
	})
	var resp struct {
		SkillName string `json:"skill_name"`
		SkillMD   string `json:"skill_md"`
		Note      string `json:"note"`
	}
	_ = json.Unmarshal([]byte(raw), &resp)

	fmt.Printf("\n  %s Skill scaffold generated: %s\n",
		c(ansiPurple, "💎"),
		c(ansiBold+ansiCyan, resp.SkillName))
	fmt.Printf("  %s %s%s\n\n", ansiGray, resp.Note, ansiReset)

	divider := c(ansiDim, strings.Repeat("─", 62))
	fmt.Println(divider)
	lines := strings.Split(resp.SkillMD, "\n")
	limit := min(len(lines), 14)
	for _, line := range lines[:limit] {
		fmt.Printf("  %s%s%s\n", ansiGray, line, ansiReset)
	}
	if len(lines) > 14 {
		fmt.Printf("  %s… (%d more lines)%s\n", ansiGray, len(lines)-14, ansiReset)
	}
	fmt.Println(divider)
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
		fatalf("building resource request: %v", err)
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

	fmt.Printf("\n  %s POST /api/resources  ←  Senior Go Engineer job description\n", c(ansiGray, "→"))
	fmt.Printf("  %s Resource created: %s\n", c(ansiGreen, "✓"), c(ansiCyan, r.ID))
	fmt.Printf("  %s Agents discover it via memory_search or MCP resources/list%s\n",
		c(ansiGreen, "✓"), ansiReset)
	return r.ID
}

// ─── display helpers ─────────────────────────────────────────────────────────

func printBanner() {
	bar := strings.Repeat("═", 64)
	fmt.Printf("\n%s\n", c(ansiBold+ansiCyan, bar))
	fmt.Printf("%s\n", c(ansiBold+ansiCyan, "  ToolHive Memory Demo — The Recruiter"))
	fmt.Printf("%s\n", c(ansiCyan, "  Scenario: Hiring a Senior Go Engineer at Stacklok"))
	fmt.Printf("%s\n\n", c(ansiBold+ansiCyan, bar))
	fmt.Printf("  MCP endpoint : %s\n", c(ansiGray, mcpURL))
	fmt.Printf("  REST API     : %s\n\n", c(ansiGray, apiURL))
}

func phase(n int, title, subtitle string) {
	bar := strings.Repeat("─", 64)
	fmt.Printf("\n%s\n", c(ansiYellow, bar))
	fmt.Printf("  %s\n", c(ansiBold+ansiWhite, fmt.Sprintf("Phase %d · %s", n, title)))
	if subtitle != "" {
		fmt.Printf("  %s%s%s\n", ansiGray, subtitle, ansiReset)
	}
	fmt.Printf("%s\n", c(ansiYellow, bar))
}

func printSummary(resourceID string) {
	bar := strings.Repeat("═", 64)
	fmt.Printf("\n\n%s\n", c(ansiBold+ansiGreen, bar))
	fmt.Printf("%s\n", c(ansiBold+ansiGreen, "  Demo complete!"))
	fmt.Printf("%s\n\n", c(ansiBold+ansiGreen, bar))
	fmt.Printf("  %-22s %s\n", "Resource uploaded:", c(ansiCyan, resourceID))
	fmt.Printf("  %-22s %s\n", "Semantic memories:", "3")
	fmt.Printf("  %-22s %s\n", "Episodic memories:", "4  (Alice, Alice-visa, Bob, Charlie)")
	fmt.Printf("  %-22s %s\n", "Procedural:", "1  →  crystallized to Skill")
	fmt.Printf("  %-22s %s\n", "Sessions demoed:", "3  (shared, recruiter, hiring-manager)")
	fmt.Println()
	fmt.Printf("  %sTo repeat the demo:%s  make teardown && make all\n\n",
		ansiGray, ansiReset)
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

func simBar(sim float64) string {
	n := int(sim * 10)
	n = min(n, 10)
	return c(ansiGreen, strings.Repeat("█", n)+strings.Repeat("░", 10-n))
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ") // normalize whitespace
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func pause() {
	time.Sleep(200 * time.Millisecond)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, c(ansiRed, "ERROR: ")+format+"\n", args...)
	os.Exit(1)
}
