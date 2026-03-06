# PR Creation Rules

When creating a pull request, follow the template at `.github/pull_request_template.md`.

## Required sections

- **Summary**: Concise bullet points explaining what changed and why. Focus on motivation — the diff shows the implementation. Include issue references (`Closes #NNN` or `Fixes #NNN`) when applicable.
- **Type of change**: Check exactly one category (bug fix, feature, refactoring, dependency update, documentation, or other).
- **Test plan**: Checkbox list of verification steps. Check off items that were actually run. Add specific details for manual testing.

## Optional sections — remove if not needed

- **Changes**: File-by-file table for PRs touching many files. Helps reviewers navigate the diff.
- **Does this introduce a user-facing change?**: Describe the change from the user's perspective. Important for release notes. Write "No" if not applicable.
- **Special notes for reviewers**: Non-obvious design decisions, known limitations, areas wanting extra scrutiny, or planned follow-up work.

## Style guidelines

- Keep the PR title under 70 characters, imperative mood, no trailing period.
- PR titles must NOT use conventional commit prefixes (`feat:`, `fix:`, `chore:`, etc.).
- Summary bullets should explain the "what and why", not implementation details.
- Remove template sections that don't apply rather than leaving them empty or with only placeholder text.
- When the PR is generated with Claude Code, include `Generated with [Claude Code](https://claude.com/claude-code)` at the bottom of the body.
