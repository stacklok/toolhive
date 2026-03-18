---
name: add-rule
description: Captures a team convention or best practice and adds it to the appropriate .claude/rules/ or .claude/agents/ file
---

# Add Rule — Capture a Team Convention

## Purpose

Formalize a convention, best practice, or correction into the project's `.claude/rules/` or `.claude/agents/` files so it applies automatically for all team members.

## Input

The user provides a convention in natural language. Examples:
- `/add-rule "prefer require.NoError over t.Fatal for error assertions"`
- `/add-rule "use context.Background() in tests, not context.TODO()"`
- `/add-rule "CLI commands must support --format json"`

If no argument is provided, ask: "What convention would you like to add?"

## Instructions

### 1. Understand the Convention

Parse the user's input to identify:
- **The rule**: What should or should not be done
- **The scope**: Which files or areas it applies to (Go code, tests, CLI, operator, etc.)
- **The reason**: Why this convention exists (ask if not provided — the "why" is critical for future developers to judge edge cases)

### 2. Find the Right Target File

**Rules vs Agents — key principle**: Rules define conventions; agents reference rules. Never duplicate rule content in agent files.

- **Rules files** (`.claude/rules/`): Auto-loaded based on `paths:` frontmatter globs when Claude touches matching files. These define the canonical conventions (style, testing patterns, error handling, etc.).
- **Agent files** (`.claude/agents/`): Define agent-specific behavior — persona, review checklist, output format, workflow steps. Agents inherit the full conversation context (including CLAUDE.md), so they already have access to all loaded rules. Agent files should *reference* rules (e.g., "Follows conventions in `.claude/rules/testing.md`"), never restate them.

Match the convention to an existing file based on scope:

| Scope | Target file | What goes here |
|-------|------------|----------------|
| General Go code | `.claude/rules/go-style.md` | Style, naming, error handling conventions |
| Test files | `.claude/rules/testing.md` | Testing patterns, framework usage |
| CLI commands | `.claude/rules/cli-commands.md` | CLI architecture, flag conventions |
| Kubernetes operator | `.claude/rules/operator.md` | CRD, controller conventions |
| PR creation | `.claude/rules/pr-creation.md` | PR format, review expectations |
| Agent workflow/persona | `.claude/agents/<agent-name>.md` | Agent-specific behavior, checklists, output format |

If no existing file fits, propose creating a new rule file with appropriate `paths:` frontmatter. New rule files need a glob pattern that determines when they auto-load.

**If the convention is about code** (how to write Go, test patterns, error handling), it belongs in a rules file — even if it's most relevant to a specific agent. The agent can reference the rule.

### 3. Draft the Addition

Read the target file and draft the new content:
- Match the style and formatting of existing rules in the file
- Place the rule in the most logical section (or propose a new section if needed)
- Keep it concise — one to three lines is ideal
- Include a brief rationale if the "why" isn't obvious from the rule itself
- Use code examples for conventions that benefit from showing good vs bad patterns

**Format examples:**

Simple rule:
```markdown
- Use `context.Background()` in tests, not `context.TODO()` — tests have no caller to propagate cancellation from
```

Rule with example:
```markdown
## Prefer Table-Driven Tests

Use table-driven tests over repeated test functions:
` ``go
// Good
tests := []struct{ name string; input int; want int }{...}

// Avoid: separate TestFoo1, TestFoo2, TestFoo3 functions
` ``
```

### 4. Present the Change

Show the user:
1. **Target file** and the section where the rule will be added
2. **The exact edit** — the lines being added in context
3. **A one-line confirmation prompt**: "Add this rule to `.claude/rules/testing.md`? (y/n)"

### 5. Apply on Confirmation

Use the Edit tool to add the rule to the target file. After applying:
- Verify the file is still well-structured
- If the rule was added to a rules file, mention that agents already pick it up automatically — rules are auto-loaded when matching files are touched, and agents inherit the full context. No agent file edits are needed unless the agent needs to explicitly reference the rule in a checklist.

## Edge Cases

- **Duplicate rule**: If a similar rule already exists, show it to the user and ask whether to update the existing rule or skip
- **Contradicts existing rule**: If the new convention contradicts an existing one, highlight the conflict and ask the user to resolve it
- **Too broad for one file**: If the convention spans multiple scopes, suggest adding it to CLAUDE.md instead or splitting into multiple rule additions
- **Personal preference vs team convention**: If the rule sounds personal (e.g., "I prefer tabs"), ask: "Is this a team-wide convention or a personal preference? Personal preferences go in your `~/.claude/` memory instead."
