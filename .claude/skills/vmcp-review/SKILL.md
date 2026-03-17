---
name: vmcp-review
description: Reviews vMCP code changes for known anti-patterns that make the codebase harder to understand or more brittle. Use when reviewing PRs, planning features, or refactoring vMCP code.
---

# vMCP Code Review

## Purpose

Review code in `pkg/vmcp/` and `cmd/vmcp/` for known anti-patterns that increase cognitive load, create brittle dependencies, or undermine testability. This skill is used both for reviewing proposed changes and for auditing existing code.

## Instructions

### 1. Determine Scope

Identify the files to review:

- If reviewing a PR or diff, examine only the changed files under `pkg/vmcp/` and `cmd/vmcp/`
- If auditing a package, examine all `.go` files in the target package
- Skip files outside the vMCP codebase — this skill is vMCP-specific

### 2. Anti-Pattern Detection

For each file under review, check against the anti-pattern catalog in `docs/vmcp-anti-patterns.md`. Not every anti-pattern applies to every file — use judgment about which checks are relevant based on what the code does.

For each finding, classify severity:

- **Must fix**: The anti-pattern is being introduced or significantly expanded by this change
- **Should fix**: The anti-pattern exists in touched code and the change is a good opportunity to address it
- **Note**: The anti-pattern exists in nearby code but is not directly related to this change — flag for awareness only

### 3. Present Findings

Structure your report as:

```markdown
## vMCP Review: [scope description]

### Must Fix
- **[Anti-pattern name]** in `path/to/file.go:line`: [What's wrong and what to do instead]

### Should Fix
- **[Anti-pattern name]** in `path/to/file.go:line`: [What's wrong and what to do instead]

### Notes
- **[Anti-pattern name]** in `path/to/file.go:line`: [Brief description, for awareness]

### Clean
No issues found for: [list anti-patterns that were checked and passed]
```

If no issues are found, say so explicitly — a clean review is valuable signal.

## What This Skill Does NOT Cover

- General Go style issues (use `golangci-lint` for that)
- Security vulnerabilities (use the security-advisor agent)
- Test quality (use the unit-test-writer agent)
- Non-vMCP code (use the general code-reviewer agent)
- Performance issues (unless they stem from an anti-pattern like repeated body parsing)
