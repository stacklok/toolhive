---
name: pr-inline-review
description: Submit inline review comments to GitHub Pull Requests using the GitHub CLI, with support for inline code suggestions.
---

# PR Inline Review

Submit inline review comments to GitHub Pull Requests using the GitHub CLI, with support for inline code suggestions.

## Instructions

You are helping submit inline review comments to a GitHub Pull Request with optional inline code suggestions.

### Prerequisites

- GitHub CLI (`gh`) must be installed and authenticated
- User must have write access to the repository
- PR must exist and be open

### Workflow

1. **Collect findings**: The user will provide you with:
   - Repository owner and name (or let you detect from current directory)
   - PR number
   - A list of findings, each containing:
     - File path (relative to repo root)
     - Line number
     - Comment body/description
     - (Optional) Suggested fix if it's a simple change

2. **Read current content**: If providing suggestions, use the Read tool to see the exact current content

3. **Create review JSON**: Build a JSON structure at `/tmp/pr-review-comments.json`:
   ```json
   {
     "body": "Overall review summary",
     "event": "COMMENT",
     "comments": [
       {
         "path": "path/to/file.ext",
         "line": 123,
         "body": "Comment text with optional suggestion"
       }
     ]
   }
   ```

4. **Submit review**: Use GitHub CLI:
   ```bash
   gh api -X POST repos/{owner}/{repo}/pulls/{pr_number}/reviews --input /tmp/pr-review-comments.json
   ```

5. **Return URL**: Extract and return the review URL from the response

## JSON Structure

### Top-level fields

- `body` (required): Overall review summary
- `event` (required): `"COMMENT"`, `"APPROVE"`, or `"REQUEST_CHANGES"`
- `comments` (required): Array of comment objects

### Comment object fields

- `path` (required): File path relative to repository root
- `line` (required): Line number (positive integer)
- `body` (required): Comment text (supports markdown)

## Inline Code Suggestions

GitHub supports inline code suggestions that users can commit directly from the PR UI.

### When to Use Suggestions

✅ **Good candidates:**
- Fixing typos or incorrect file paths
- Correcting simple syntax errors
- Updating version numbers or constants
- Renaming variables or functions
- Fixing formatting or indentation
- Adding missing content

❌ **Not suitable:**
- Complex logic changes requiring multiple files
- Changes that need testing or validation
- Architectural changes requiring discussion
- Changes requiring user decision/context

### Suggestion Syntax

Include a suggestion block in the comment `body`:

**Single-line:**
````markdown
Description of the issue.

```suggestion
corrected line of code
```

Evidence: reference
````

**Multi-line:**
````markdown
Description of the issue.

```suggestion
first corrected line
second corrected line
third corrected line
```

Evidence: reference
````

## Best Practices

### Comment Writing
- Be specific with line numbers and file paths
- Provide evidence (link to code/documentation)
- Be constructive - suggest fixes, not just problems
- Use markdown formatting for clarity
- Include context explaining why it's an issue

### When Including Suggestions
1. Read the current line(s) using Read tool first
2. Provide exact replacement text
3. Match existing formatting and style
4. Verify syntax is correct
5. One suggestion block per comment

### Review Strategy
1. Group related findings into a single review
2. Put simple fixes with suggestions first
3. Use appropriate event type
4. Write clear summary in `body`
5. Use TodoWrite to track progress

## Error Handling

- **401 Unauthorized**: Run `gh auth login`
- **404 Not Found**: Verify PR number and repo access
- **422 Unprocessable Entity**: Check JSON format
- **Invalid line number**: Ensure line exists at PR's commit

## Output Format

Report after submission:
- Review ID and URL
- Number of comments submitted
- Number with suggestions
- PR title and number

## See Also

- [EXAMPLES.md](EXAMPLES.md) - Common use cases and examples
