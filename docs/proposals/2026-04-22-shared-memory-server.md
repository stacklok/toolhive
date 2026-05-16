# Shared Long-Term Memory Server

**Date:** 2026-04-22
**Status:** Implementation in progress (Plan 1 of 3 complete)

---

## Problem

ToolHive manages MCPs (tools) and Skills (procedural knowledge as OCI artifacts). The missing
primitive is **shared long-term memory**: a team-wide knowledge store that agents can query and
contribute to across sessions.

Without it, every agent session starts cold. Facts learned by one agent are invisible to others.
Patterns that emerge from repeated interactions are lost when the session ends.

---

## Memory Types

Two long-term memory namespaces are in scope:

| Type | Purpose | Example |
|---|---|---|
| `semantic` | Aggregated facts and world-state knowledge | "Company does not sponsor visas" |
| `procedural` | How-to knowledge, heuristics, SOPs | "Always run `task lint-fix` before committing" |
| `episodic` | Time-indexed event records | "Recruiter archived candidate on 2024-03-15 — visa required" |

**Out of scope:** working memory and conversational memory — agents handle those internally via
their context window.

---

## Architecture

### System Workload

The memory server is ToolHive's first **system workload** — a managed MCP server auto-provisioned
by ToolHive rather than explicitly started by users. Key properties:

- Auto-provisioned on first use (`thv memory init`)
- Persistent — excluded from `thv stop --all`
- Singleton per scope (one per team in `thv serve` mode)
- Registered in the registry under the reserved name `toolhive.memory`

### Transport

The memory server uses **MCP streamable HTTP** transport (not stdio). Agents connect via
`http://<host>:8080/mcp`. A `/health` liveness probe is available at the same host.

### Pluggable Backends

Three independent interfaces, configured via `memory-server.yaml`:

```yaml
storage:
  provider: sqlite          # sqlite (default) | postgres | mongodb
  dsn: /data/memory.db

vector:
  provider: sqlite-vec      # sqlite-vec (default) | qdrant | pgvector
  url: ""

embedder:
  provider: ollama          # ollama (default) | openai | cohere
  model: nomic-embed-text
  url: http://localhost:11434

server:
  host: 0.0.0.0
  port: 8080
  lifecycle_interval_hours: 24
```

Zero-infra teams use SQLite defaults with no external dependencies. Teams with Postgres can
collapse both storage and vector into pgvector.

### Deployment Modes

**Local (`thv` CLI):** Personal memory, local container, SQLite defaults.

**Team (`thv serve`):** Shared instance; all team agents connect via the API server proxy.
Auth enforced via existing OIDC middleware.

**Kubernetes (`thv-operator`):** New `MCPMemoryServer` CRD (Plan 3). Operator reconciles to
`Deployment + Service + PVC`.

---

## MCP Tool Surface

Agents consume memory exactly like any other MCP — no special integration.

| Tool | Description |
|---|---|
| `memory_remember` | Write a memory. Runs conflict detection; returns conflicts if similarity > 0.85 |
| `memory_search` | Semantic vector search, results ranked by composite trust+staleness score |
| `memory_recall` | Fetch a specific entry by ID, including full revision history |
| `memory_forget` | Delete a memory |
| `memory_update` | Correct content; previous version saved to revision history |
| `memory_flag` | Mark as potentially stale without deleting |
| `memory_list` | Structured listing with filters: type, author, tags, time-range |
| `memory_consolidate` | Merge related entries; originals archived with pointer |
| `memory_crystallize` | Promote procedural memories to a Skill scaffold for human authoring |

### Conflict Detection

On `memory_remember`, the server embeds the new content and searches for similar active entries.
If any entry has cosine similarity > 0.85, the write is blocked and the agent receives a
`conflict_detected` response with the conflicting entries. The agent decides: force-write,
update the existing entry, or abort. No LLM inference — the agent (which has context) is better
placed to judge whether two similar entries actually conflict.

### Search Ranking

`memory_search` returns results ranked by a composite score that combines vector similarity with
the entry's trust and staleness signals:

```
composite = similarity × trust_score × (1 - 0.3 × staleness_score)
```

This prevents a high-similarity but flagged or stale entry from ranking above a fresher,
more trusted one.

---

## Trust and Staleness Scoring

### Trust Score

```
trust_score = author_weight
            × age_decay(created_at, half_life=180d)
            × (1 - min(corrections × 0.05, 0.30))
            × (0.5 if flagged else 1.0)

author_weight: human=1.0, agent=0.7
```

### Staleness Score

```
staleness_score = normalize(days_since_last_access, max=90d)
                + (0.3 if flagged)
                + min(corrections × 0.1, 0.3)
```

Entries with `staleness_score > 0.8` surface in the lifecycle audit log every 24 hours.

---

## Skills Relationship

Skills (existing) and procedural memory are the same kind of knowledge at different stages of
maturity:

```
Agent/human observes something
         │
         ▼
  Procedural Memory          ← fluid, emergent, evolving
  (memory server)
         │
    (patterns emerge,
     human crystallizes)
         │
         ▼
      Skill (OCI)            ← crystallized, versioned, distributed
  (existing skills system)
```

`memory_crystallize` bridges the gap: it takes stable procedural memory entries and produces a
`SKILL.md` scaffold for a human to author and push via `thv skills push`. The source entries are
archived with a `crystallized_into` pointer so search returns the canonical Skill instead.

---

## Recommended Memory Activation Strategy

Not every agent interaction should touch the memory server. The recommended approach is a
three-tier strategy:

### Tier 1 — Session-boundary injection (always)

At the **start** of every task-bearing session, the system prompt instructs the agent to run one
`memory_search` call with the task description before doing anything else. This is silent,
cheap (one vector search), and covers the most valuable case: cross-session continuity.

```
Before starting work, call memory_search with the task description to load
relevant team knowledge. Do this once, silently — do not explain it to the user.
```

At the **end** of a session, the agent writes what was discovered or decided that would be
useful to a different agent in a future session.

### Tier 2 — Signal-based mid-session reads (agent-decided)

The system prompt instructs the agent to call `memory_search` when it encounters:

1. **Uncertainty** — "I don't have enough context to answer this confidently"
2. **Cross-session references** — phrases like "last time", "previously", "we decided",
   "our policy", "do you remember"
3. **Team-specific facts** — questions about preferences, conventions, or domain knowledge
   not in the codebase or current context

### Tier 3 — Write on observation, not speculation

The agent calls `memory_remember` only for facts that:
- Were not already in the search results from Tier 1
- Would be useful to a **different** agent in a **future** session

The system prompt guidance:

```
Write a memory when you learn something that:
- corrects or refines an existing fact (use memory_update instead)
- is a team decision, constraint, or policy that will apply again
- is a recurring pattern observed more than once

Do NOT write memories for facts already in the codebase, documentation,
or the current conversation context.
```

### Why not automatic ingestion?

Auto-ingestion (LinkedIn's streaming pipeline approach) requires:
- An LLM call in the ingestion path to extract facts from raw transcripts
- Quality control to decide what is worth persisting
- Evaluation tooling to measure ingestion accuracy

These are deferred to a later plan. The explicit tool-use model is more predictable and
debuggable for a v1, and the agent (which has full context) makes better judgments about
what is worth writing than a pipeline operating on raw text.

---

## Comparison with LinkedIn's Cognitive Memory Agent

LinkedIn's CMA (described in their [engineering blog](https://www.linkedin.com/blog/engineering/ai/the-linkedin-generative-ai-application-tech-stack-personalization-with-cognitive-memory-agent))
is the closest public reference. Key differences:

| Dimension | LinkedIn CMA | ToolHive Memory |
|---|---|---|
| Conflict detection | On roadmap | Implemented (cosine > 0.85) |
| Trust/staleness scoring | Time-based prioritization planned | Implemented (full formula + background job) |
| User control (list/update/delete/flag) | On roadmap | Implemented |
| Search ranking | Implicit | Composite score: similarity × trust × (1 − staleness penalty) |
| Episodic memory type | Distinct tier | `TypeEpisodic` with time-range `ListFilter` |
| Retrieval orchestration | LLM-powered multi-step planner | Agent calls tools directly (agent IS the orchestrator) |
| Hierarchical aggregation | Auto tree: events → summaries → facets | Explicit: `memory_consolidate` + `memory_crystallize` |
| Tenant isolation | Per-application isolated stores | Auth at proxy layer; storage-level namespace deferred |
| Auto ingestion pipeline | Streaming + batch LLM extraction | Deferred; `memory_distill` returns candidates for agent review |

---

## Implementation Status

### Plan 1 — Memory server core (this branch)

- [x] `pkg/memory/` — domain types, interfaces (`Store`, `VectorStore`, `Embedder`), scoring, service
- [x] `pkg/memory/sqlite/` — SQLite Store + VectorStore (Go cosine similarity, no CGo)
- [x] `pkg/memory/embedder/ollama/` — Ollama HTTP embedder
- [x] `pkg/memory/mocks/` — gomock mocks for all three interfaces
- [x] `cmd/thv-memory/` — MCP server binary (streamable HTTP on `/mcp`)
- [x] `cmd/thv-memory/lifecycle/` — 24h background job (TTL expiry, score recomputation)
- [x] `cmd/thv-memory/tools/` — 9 MCP tool handlers
- [x] Integration test (SQLite + fake embedder, end-to-end remember → search → delete)

### Plan 2 — CLI + system workload integration (not started)

- `thv memory` subcommand tree
- System workload auto-provisioning (`thv memory init`)
- Registry integration under `toolhive.memory`

### Plan 3 — Kubernetes operator (not started)

- `MCPMemoryServer` CRD
- Operator controller: reconciles to `Deployment + Service + PVC`
- `MCPRegistry` integration

---

## Package Layout

```
pkg/memory/
├── types.go          — Entry, Revision, ListFilter, VectorFilter, scoring types
├── interfaces.go     — Store, VectorStore, Embedder interfaces + mockgen directives
├── service.go        — Orchestration: conflict detection, remember, search
├── scoring.go        — ComputeTrustScore, ComputeStalenessScore
├── errors.go         — ErrNotFound
├── mocks/            — Generated gomock mocks
├── sqlite/
│   ├── db.go         — DB wrapper, WAL pragmas, goose migrations
│   ├── store.go      — Store implementation
│   ├── vector.go     — VectorStore implementation (Go cosine similarity)
│   └── migrations/   — goose SQL migrations
└── embedder/
    └── ollama/       — Ollama HTTP embedder

cmd/thv-memory/
├── main.go           — Entry point, HTTP server lifecycle
├── server.go         — MCP server construction, tool registration, HTTP handler
├── config.go         — YAML config with defaults
├── lifecycle/
│   └── job.go        — Background maintenance job
└── tools/            — One file per MCP tool
    ├── remember.go, search.go, recall.go, forget.go, update.go
    ├── flag.go, list.go, consolidate.go, crystallize.go
```
