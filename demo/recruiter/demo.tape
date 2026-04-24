# ToolHive Memory Demo — The Recruiter
# Requires: vhs (brew install vhs), Ollama running, nomic-embed-text pulled
#
# Usage:
#   cd demo/recruiter
#   vhs demo.tape

Output demo.gif
Set FontSize 14
Set Width 140
Set Height 46
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
Type "make server-start"
Sleep 500ms
Enter
Sleep 1s

Type "make server-wait"
Sleep 500ms
Enter
Sleep 5s

# ── step 4: run demo ──────────────────────────────────────────────────────────
Type "make demo"
Sleep 500ms
Enter

# Phase 1: Resource upload (fast — pure HTTP)
Sleep 3s

# Phase 2: Semantic memories (3 Ollama calls, ~3s each)
Sleep 12s

# Phase 3: Recruiter session, Alice interviews (2 Ollama calls)
Sleep 8s

# Phase 4: Hiring manager cold search (2 Ollama calls for query embed)
Sleep 8s

# Phase 5: Bob + procedural memory (2 Ollama calls)
Sleep 8s

# Phase 6: Charlie (1 Ollama call)
Sleep 5s

# Phase 7: Crystallize (no embed needed)
Sleep 3s

# Summary
Sleep 3s

# ── step 5: teardown ──────────────────────────────────────────────────────────
Sleep 1s
Type "make teardown"
Sleep 500ms
Enter
Sleep 2s

# Final pause
Sleep 2s
