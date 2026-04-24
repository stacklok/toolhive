# ══════════════════════════════════════════════════════════════════════════════
# ToolHive Memory Demo — The Recruiter Scenario
# ══════════════════════════════════════════════════════════════════════════════
#
# Prerequisites
#   • Ollama running locally with nomic-embed-text pulled
#       brew install ollama && ollama pull nomic-embed-text && ollama serve
#   • Go 1.22+
#
# Quick start
#   make all          — build binaries, start server, run demo, stop server
#   make demo         — run demo only  (server must already be running)
#   make teardown     — stop server, delete DB and demo binary (safe to repeat)
#
# Individual targets
#   make build        — build thv-memory server + demo binary
#   make server-start — start memory server in background
#   make server-stop  — stop memory server
#   make server-logs  — tail server log file
#   make status       — check server health
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

# Env for the demo binary
DEMO_ENV := \
  MEMORY_MCP_URL=$(BASE_URL)/mcp \
  MEMORY_API_URL=$(BASE_URL)/api/resources \
  MEMORY_JD_FILE=$(CURDIR)/data/job-description.txt

.PHONY: all demo build server-start server-stop server-wait server-logs status teardown

# ── all: one-shot full demo ───────────────────────────────────────────────────
all: build server-start server-wait demo server-stop

# ── demo: run scenario (server must be up) ────────────────────────────────────
demo: $(DEMO_BIN)
	@echo ""
	$(DEMO_ENV) $(DEMO_BIN)

# ── build: compile both binaries ─────────────────────────────────────────────
build: $(SERVER_BIN) $(DEMO_BIN)

$(SERVER_BIN):
	@echo "▶  Building thv-memory server..."
	cd $(WORKTREE_ROOT) && task build
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
	@rm -f $(DB_FILE) $(DEMO_BIN) $(LOG_FILE) $(CONFIG)
	@echo "✓  Teardown complete — safe to run 'make all' again"
