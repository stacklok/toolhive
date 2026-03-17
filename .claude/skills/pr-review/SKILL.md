---
name: pr-review
description: Submit inline review comments to GitHub PRs and reply to/resolve review threads using the GitHub CLI and GraphQL API.
---

# PR Review

Submit inline review comments to GitHub Pull Requests and reply to/resolve review threads using the GitHub CLI.

## Prerequisites

- GitHub CLI (`gh`) must be installed and authenticated
- User must have write access to the repository
- PR must exist and be open

---

## Part 1: Submitting Inline Review Comments

### Workflow

1. **Collect findings**: The user will provide you with:
   - Repository owner and name (or detect from current directory)
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

### JSON Structure

#### Top-level fields

- `body` (required): Overall review summary
- `event` (required): `"COMMENT"`, `"APPROVE"`, or `"REQUEST_CHANGES"`
- `comments` (required): Array of comment objects

#### Comment object fields

- `path` (required): File path relative to repository root
- `line` (required): Line number (positive integer)
- `body` (required): Comment text (supports markdown)

### Inline Code Suggestions

GitHub supports inline code suggestions that users can commit directly from the PR UI.

#### When to Use Suggestions

**Good candidates:**
- Fixing typos or incorrect file paths
- Correcting simple syntax errors
- Updating version numbers or constants
- Renaming variables or functions
- Fixing formatting or indentation
- Adding missing content

**Not suitable:**
- Complex logic changes requiring multiple files
- Changes that need testing or validation
- Architectural changes requiring discussion
- Changes requiring user decision/context

#### Suggestion Syntax

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

### Submitting Best Practices

- Be specific with line numbers and file paths
- Provide evidence (link to code/documentation)
- Be constructive - suggest fixes, not just problems
- Use markdown formatting for clarity
- Include context explaining why it's an issue

#### When Including Suggestions
1. Read the current line(s) using Read tool first
2. Provide exact replacement text
3. Match existing formatting and style
4. Verify syntax is correct
5. One suggestion block per comment

#### Review Strategy
1. Group related findings into a single review
2. Put simple fixes with suggestions first
3. Use appropriate event type
4. Write clear summary in `body`

### Output Format

Report after submission:
- Review ID and URL
- Number of comments submitted
- Number with suggestions
- PR title and number

---

## Part 2: Replying to and Resolving Review Comments

### Workflow

#### 1. Gather Review Comments

Fetch all review comments from the PR and present them organized by:
- Status: unresolved vs resolved
- Type: suggestions, questions, nitpicks, critical issues
- Author: group by reviewer

**For each comment show:**
- Author and timestamp
- File and line number
- Comment body
- Any existing replies
- Resolution status

#### 2. Analyze and Recommend

For each unresolved comment, provide a recommendation:

**If code needs fixing:**
- "Recommendation: Fix the issue, then reply with commit SHA and resolve"

**If it's a question:**
- "Recommendation: Answer the question, wait for acknowledgment before resolving"

**If it's a suggestion to consider:**
- "Recommendation: Discuss trade-offs, decide with user whether to implement"

**If already addressed:**
- "Recommendation: Reply with commit reference and resolve immediately"

#### 3. Get User Decisions

**Present summary:**
```
Found 5 unresolved review comments:

1. [Critical] pkg/versions/version.go:24 - @Copilot
   "Fix nolint spacing"
   Status: Fixed in commit c4bb55d
   Recommendation: Reply "Fixed in c4bb55d" and resolve

2. [Question] pkg/server/handler.go:45 - @reviewer
   "Why use buffered channel here?"
   Status: Needs answer
   Recommendation: Draft response for your review

How would you like to proceed?
- Reply and resolve all fixed items (1)
- Draft responses for questions (2)
- Process individually
- Custom approach
```

#### 4. Execute User's Choice

Based on user decisions:
- Draft reply messages for approval
- Submit replies after user confirms
- Resolve threads only when user approves

#### 5. Report Results

After processing, show:
- What was done (replied/resolved)
- What remains (still needs attention)
- Any errors or issues
- Next steps if any

### Interactive Decision Points

#### Before Replying
**Ask:** "Here's my draft reply: '{message}'. Send this?"
- User can edit, approve, or skip

#### Before Resolving
**Ask:** "Mark this thread as resolved?"
- Only if issue is truly addressed
- User may want to wait for reviewer acknowledgment

#### For Bulk Operations
**Ask:** "I found 5 comments fixed in commit abc123. Reply 'Fixed in abc123' to all and resolve?"
- Show list of affected comments
- Let user review before executing

### Reply Best Practices

- **Be specific**: Reference commit SHAs when applicable
- **Be helpful**: Explain reasoning, not just "fixed"
- **Be respectful**: Thank reviewers for feedback
- **Use markdown**: Format code, lists, links

### When to Resolve
**Resolve when:**
- Issue is fixed and committed
- Question answered and acknowledged
- Discussion concluded with agreement
- User confirms it's complete

**Don't auto-resolve:**
- Without user confirmation
- When still discussing
- When waiting for reviewer response
- When unsure about the fix

---

## Command Reference

### Submit a review
```bash
gh api -X POST repos/{owner}/{repo}/pulls/{pr}/reviews --input /tmp/pr-review-comments.json
```

### Get all PR comments with details
```bash
gh api repos/{owner}/{repo}/pulls/{pr}/comments | \
  jq '.[] | {id, path, line, author: .user.login, body: .body[0:100]}'
```

### Reply to a specific comment
```bash
gh api -X POST repos/{owner}/{repo}/pulls/{pr}/comments/{comment_id}/replies \
  -f body="Your reply message"
```

### Get all review threads (to find thread IDs)
```bash
gh api graphql -f query='
query {
  repository(owner: "{owner}", name: "{repo}") {
    pullRequest(number: {pr}) {
      reviewThreads(first: 20) {
        nodes {
          id
          isResolved
          comments(first: 10) {
            nodes {
              id
              body
              author { login }
            }
          }
        }
      }
    }
  }
}'
```

### Resolve a thread
```bash
gh api graphql -f query='
mutation {
  resolveReviewThread(input: {threadId: "{thread_id}"}) {
    thread {
      id
      isResolved
    }
  }
}'
```

### Unresolve a thread
```bash
gh api graphql -f query='
mutation {
  unresolveReviewThread(input: {threadId: "{thread_id}"}) {
    thread {
      id
      isResolved
    }
  }
}'
```

## Error Handling

- **401 Unauthorized**: Run `gh auth login`
- **404 Not Found**: Verify PR number and repo access
- **422 Unprocessable Entity**: Check JSON format
- **Invalid line number**: Ensure line exists at PR's commit

## See Also

- [Inline Review Examples](EXAMPLES-INLINE.md) - Examples of submitting review comments
- [Reply Examples](EXAMPLES-REPLY.md) - Examples of replying to and resolving review comments
