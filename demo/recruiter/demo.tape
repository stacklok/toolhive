# ToolHive Memory Demo — The Recruiter
# Requires: vhs (brew install vhs), Ollama running, nomic-embed-text pulled,
#           Claude Code CLI installed and authenticated (npm i -g @anthropic-ai/claude-code)
#
# Usage:
#   cd demo/recruiter
#   vhs demo.tape

Output demo.gif
Set FontSize 14
Set Width 160
Set Height 50
Set Theme "Dracula"
Set TypingSpeed 40ms
Set PlaybackSpeed 1.0

# ── title card ────────────────────────────────────────────────────────────────
Hide
Type "echo ''"
Enter
Sleep 500ms
Show

Type "# ToolHive Memory Demo — The Recruiter"
Sleep 1.5s
Enter
Sleep 300ms

Type "# Hiring a Senior Go Engineer at Stacklok"
Sleep 1.5s
Enter
Sleep 500ms

# ── step 1: change to demo dir ────────────────────────────────────────────────
Type "cd demo/recruiter"
Sleep 500ms
Enter
Sleep 300ms

# ── step 2: build ─────────────────────────────────────────────────────────────
Type "make build"
Sleep 500ms
Enter
Sleep 15s

# ── step 3: start server ──────────────────────────────────────────────────────
Type "make server-start server-wait"
Sleep 500ms
Enter
Sleep 8s

# ── step 4: setup phase (resource upload + semantic memories) ─────────────────
Type "# Phase 1-2: Upload job description and prime shared memory"
Sleep 1s
Enter
Sleep 300ms
Type "make demo"
Sleep 500ms
Enter

# Phase 1: Resource upload + print JD (fast — pure HTTP)
Sleep 5s

# Phase 2: Semantic memories (3 Ollama embedding calls, ~3s each)
Sleep 12s

# ── step 5: recruiter session — Alice Chen ────────────────────────────────────
Type "make session-recruiter-alice"
Sleep 500ms
Enter

# Claude Code: checks visa policy via memory_search, records 3 episodic memories
Sleep 30s

# ── step 6: hiring manager cold search ───────────────────────────────────────
Type "make session-hiring-manager"
Sleep 500ms
Enter

# Claude Code: 4 independent memory_search calls, reports pipeline state
Sleep 30s

# ── step 7: recruiter session — Bob Martinez ─────────────────────────────────
Type "make session-recruiter-bob"
Sleep 500ms
Enter

# Claude Code: checks pipeline, records episodic + procedural memory
Sleep 35s

# ── step 8: recruiter session — Charlie Kim ───────────────────────────────────
Type "make session-recruiter-charlie"
Sleep 500ms
Enter

# Claude Code: retrieves phone-screen checklist, records Charlie advancing
Sleep 30s

# ── step 9: crystallize pattern into a Skill ──────────────────────────────────
Type "make session-crystallize"
Sleep 500ms
Enter

# Claude Code: lists procedural memory, crystallizes → SKILL.md scaffold
Sleep 25s

# ── step 10: teardown ────────────────────────────────────────────────────────
Type "make teardown"
Sleep 500ms
Enter
Sleep 3s

# Final pause
Sleep 2s
