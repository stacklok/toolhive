# PR Creation Rules

You MUST follow the template at `.github/pull_request_template.md` when creating pull requests. Do NOT skip or leave placeholder text in required sections.

## Required sections — do NOT omit these

- **Summary**: You MUST explain (1) WHY the change is needed and (2) WHAT changed. Lead with the motivation — the diff shows the code. Include issue references (`Closes #NNN` or `Fixes #NNN`) when a related issue exists; remove the `Fixes #` line entirely if there is none.
- **Type of change**: Check exactly one category. Do not leave all boxes unchecked.
- **Test plan**: Check every verification step you actually ran. You MUST check at least one item. For manual testing, describe exactly what you tested.

## Optional sections — remove entirely if not needed

Do NOT leave optional sections empty or with only placeholder/template text. Either fill them in or delete them.

- **Changes**: File-by-file table for PRs touching more than a few files.
- **Does this introduce a user-facing change?**: Describe the change from the user's perspective. Write "No" if not applicable.
- **Special notes for reviewers**: Non-obvious design decisions, known limitations, areas wanting extra scrutiny, or planned follow-up work.

## Style guidelines

- Keep the PR title under 70 characters, imperative mood, no trailing period.
- PR titles must NOT use conventional commit prefixes (`feat:`, `fix:`, `chore:`, etc.).
- Summary bullets MUST explain the "why" first, then the "what". Do not just list what files changed.
- When the PR is generated with Claude Code, include `Generated with [Claude Code](https://claude.com/claude-code)` at the bottom of the body.
