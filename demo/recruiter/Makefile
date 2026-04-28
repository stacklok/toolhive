# ══════════════════════════════════════════════════════════════════════════════
# ToolHive Memory Demo — The Recruiter Scenario
# ══════════════════════════════════════════════════════════════════════════════
#
# Prerequisites
#   • Ollama running locally with nomic-embed-text pulled
#       brew install ollama && ollama pull nomic-embed-text && ollama serve
#   • Go 1.22+
#   • Claude Code CLI installed and authenticated (for agent sessions)
#       npm install -g @anthropic-ai/claude-code
#
# Quick start
#   make all          — build binaries, start server, run full demo, stop server
#   make demo         — run full demo  (server must already be running)
#   make teardown     — stop server, delete DB and demo binary (safe to repeat)
#
# Individual targets
#   make build        — build thv-memory server + demo binary
#   make server-start — start memory server in background
#   make server-stop  — stop memory server
#   make server-logs  — tail server log file
#   make status       — check server health
#
# Agent sessions (run individually after setup phase)
#   make session-recruiter-alice   — Phase 3: recruiter records Alice's phone screen
#   make session-hiring-manager    — Phase 4: hiring manager searches memory cold
#   make session-recruiter-bob     — Phase 5: recruiter records Bob + procedural lesson
#   make session-recruiter-charlie — Phase 6: recruiter uses checklist, records Charlie
#   make session-crystallize       — Phase 7: crystallize phone-screen pattern → Skill
# ══════════════════════════════════════════════════════════════════════════════

SHELL := /bin/bash
.DEFAULT_GOAL := all

# Locate the worktree root (parent of demo/recruiter)
WORKTREE_ROOT := $(shell cd ../.. && pwd)

# Binaries
SERVER_BIN  := $(WORKTREE_ROOT)/bin/thv-memory
DEMO_BIN    := $(CURDIR)/.demo-bin

# Server config (generated from template)
CONFIG_TMPL := $(CURDIR)/config/memory-server.yaml.tmpl
CONFIG      := $(CURDIR)/config/memory-server.yaml

# Runtime files (all cleaned by teardown)
DB_FILE     := $(CURDIR)/demo-memory.db
PID_FILE    := $(CURDIR)/.server.pid
LOG_FILE    := $(CURDIR)/.server.log

# Demo server port — non-standard to avoid conflicts with anything on 8080
PORT        := 8765
BASE_URL    := http://127.0.0.1:$(PORT)

# MCP config file for Claude Code agent sessions
MCP_CONFIG  := $(CURDIR)/.demo.mcp.json

# Env for the demo binary
DEMO_ENV := \
  MEMORY_MCP_URL=$(BASE_URL)/mcp \
  MEMORY_API_URL=$(BASE_URL)/api/resources \
  MEMORY_JD_FILE=$(CURDIR)/data/job-description.txt

.PHONY: all demo build \
        server-start server-stop server-wait server-logs status \
        mcp-config \
        session-recruiter-alice session-hiring-manager \
        session-recruiter-bob session-recruiter-charlie \
        session-crystallize \
        teardown

# ── all: one-shot full demo ───────────────────────────────────────────────────
all: build server-start server-wait demo server-stop

# ── demo: run full scenario (server must be up) ───────────────────────────────
# Runs setup (Phases 1-2) then all five Claude Code agent sessions (Phases 3-7)
demo: $(DEMO_BIN) mcp-config
	@echo ""
	$(DEMO_ENV) $(DEMO_BIN)
	@$(MAKE) --no-print-directory session-recruiter-alice
	@$(MAKE) --no-print-directory session-hiring-manager
	@$(MAKE) --no-print-directory session-recruiter-bob
	@$(MAKE) --no-print-directory session-recruiter-charlie
	@$(MAKE) --no-print-directory session-crystallize

# ── mcp-config: generate MCP config for Claude Code sessions ─────────────────
mcp-config:
	@printf '{\n  "mcpServers": {\n    "toolhive-memory": {\n      "type": "http",\n      "url": "$(BASE_URL)/mcp"\n    }\n  }\n}\n' > $(MCP_CONFIG)
	@echo "✓  MCP config written to $(MCP_CONFIG)"

# ── agent sessions (Phase 3-7) ────────────────────────────────────────────────
# Each session prints the prompt to use, then waits for you to run it in
# Claude Code (claude --mcp-config .demo.mcp.json) and press Enter to continue.

define print-prompt
	@echo ""
	@echo "═══════════════════════════════════════════════════════════════"
	@echo "  $(1)"
	@echo "  MCP config: $(MCP_CONFIG)"
	@echo "═══════════════════════════════════════════════════════════════"
	@echo ""
	@cat $(2)
	@echo ""
	@echo "───────────────────────────────────────────────────────────────"
	@read -p "  ↑ Use this prompt in Claude Code, then press Enter to continue... " _
endef

session-recruiter-alice: mcp-config
	$(call print-prompt,Phase 3 · Recruiter Session — Alice Chen phone screen,$(CURDIR)/prompts/recruiter-alice.txt)

session-hiring-manager: mcp-config
	$(call print-prompt,Phase 4 · Hiring Manager — cold memory search,$(CURDIR)/prompts/hiring-manager.txt)

session-recruiter-bob: mcp-config
	$(call print-prompt,Phase 5 · Recruiter Session — Bob Martinez + procedural lesson,$(CURDIR)/prompts/recruiter-bob.txt)

session-recruiter-charlie: mcp-config
	$(call print-prompt,Phase 6 · Recruiter Session — Charlie Kim (HIRE),$(CURDIR)/prompts/recruiter-charlie.txt)

session-crystallize: mcp-config
	$(call print-prompt,Phase 7 · Crystallize — phone-screen pattern → Skill,$(CURDIR)/prompts/crystallize.txt)

# ── build: compile both binaries ─────────────────────────────────────────────
build: $(SERVER_BIN) $(DEMO_BIN)

$(SERVER_BIN):
	@echo "▶  Building thv-memory server..."
	@mkdir -p $(WORKTREE_ROOT)/bin
	cd $(WORKTREE_ROOT) && go build -o $(SERVER_BIN) ./cmd/thv-memory/
	@echo "✓  $(SERVER_BIN)"

$(DEMO_BIN): $(CURDIR)/cmd/demo/main.go
	@echo "▶  Building demo binary..."
	cd $(WORKTREE_ROOT) && go build -o $(DEMO_BIN) ./demo/recruiter/cmd/demo/
	@echo "✓  $(DEMO_BIN)"

# ── server config (generated from template) ──────────────────────────────────
$(CONFIG): $(CONFIG_TMPL)
	@echo "▶  Generating server config..."
	@sed 's|DEMO_DB_PATH|$(DB_FILE)|g' $(CONFIG_TMPL) > $(CONFIG)
	@echo "✓  $(CONFIG)"

# ── server-start ─────────────────────────────────────────────────────────────
server-start: $(SERVER_BIN) $(CONFIG)
	@if [ -f $(PID_FILE) ] && kill -0 $$(cat $(PID_FILE)) 2>/dev/null; then \
	  echo "⚠  Server already running (pid=$$(cat $(PID_FILE)))"; \
	else \
	  echo "▶  Starting memory server on port $(PORT)..."; \
	  MEMORY_CONFIG=$(CONFIG) $(SERVER_BIN) > $(LOG_FILE) 2>&1 & \
	  echo $$! > $(PID_FILE); \
	  echo "✓  Server started (pid=$$!, log=$(LOG_FILE))"; \
	fi

# ── server-wait: poll /health until ready ────────────────────────────────────
server-wait:
	@printf "▶  Waiting for server..."
	@for i in $$(seq 1 30); do \
	  if curl -sf $(BASE_URL)/health > /dev/null 2>&1; then \
	    printf " ready!\n"; exit 0; \
	  fi; \
	  printf "."; sleep 1; \
	done; \
	printf " TIMEOUT\n"; \
	echo "   Check logs: make server-logs"; \
	exit 1

# ── server-stop ──────────────────────────────────────────────────────────────
server-stop:
	@if [ -f $(PID_FILE) ]; then \
	  PID=$$(cat $(PID_FILE)); \
	  kill $$PID 2>/dev/null && echo "✓  Server stopped (pid=$$PID)" || echo "⚠  Process $$PID not found"; \
	  rm -f $(PID_FILE); \
	else \
	  echo "⚠  No PID file found — server may not be running"; \
	fi

# ── server-logs ──────────────────────────────────────────────────────────────
server-logs:
	@tail -f $(LOG_FILE)

# ── status: quick health check ───────────────────────────────────────────────
status:
	@if curl -sf $(BASE_URL)/health > /dev/null 2>&1; then \
	  echo "✓  Server healthy at $(BASE_URL)"; \
	else \
	  echo "✗  Server not responding at $(BASE_URL)"; \
	fi

# ── teardown: stop server + wipe all demo data ───────────────────────────────
teardown: server-stop
	@echo "▶  Removing demo data..."
	@rm -f $(DB_FILE) $(DEMO_BIN) $(SERVER_BIN) $(LOG_FILE) $(CONFIG) $(MCP_CONFIG)
	@echo "✓  Teardown complete — safe to run 'make all' again"
