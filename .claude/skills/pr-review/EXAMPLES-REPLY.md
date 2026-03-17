# PR Review Reply Examples

Common scenarios with actual commands for replying to and resolving GitHub PR review comments.

## Example 1: Simple "Fixed in Commit" Reply

**Scenario:** Copilot suggested fixing nolint comment spacing. You fixed it in commit c4bb55d.

### Step 1: Get the comment ID

```bash
gh api repos/stacklok/toolhive-registry-server/pulls/20/comments | jq '.[] | {id, path, line, body: .body[0:100], author: .user.login}'
```

**Output:**
```json
{
  "id": 2445150488,
  "path": "pkg/versions/version.go",
  "line": 24,
  "body": "Corrected spacing in nolint comment...",
  "author": "copilot-pull-request-reviewer"
}
```

### Step 2: Reply to the comment

```bash
gh api -X POST repos/stacklok/toolhive-registry-server/pulls/20/comments/2445150488/replies \
  -f body="Fixed in c4bb55d"
```

### Step 3: Get the thread ID

```bash
gh api graphql -f query='
query {
  repository(owner: "stacklok", name: "toolhive-registry-server") {
    pullRequest(number: 20) {
      reviewThreads(first: 20) {
        nodes {
          id
          isResolved
          comments(first: 5) {
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
}' | jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.comments.nodes[0].id == 2445150488) | {threadId: .id, isResolved}'
```

**Output:**
```json
{
  "threadId": "PRRT_kwDOP_5nS85emMpx",
  "isResolved": false
}
```

### Step 4: Resolve the thread

```bash
gh api graphql -f query='
mutation {
  resolveReviewThread(input: {threadId: "PRRT_kwDOP_5nS85emMpx"}) {
    thread {
      id
      isResolved
    }
  }
}'
```

**Output:**
```json
{
  "data": {
    "resolveReviewThread": {
      "thread": {
        "id": "PRRT_kwDOP_5nS85emMpx",
        "isResolved": true
      }
    }
  }
}
```

---

## Example 2: Batch Processing Multiple Fixed Comments

**Scenario:** Multiple comments fixed in the same commit. Process them all at once.

### Step 1: Get all unresolved comments

```bash
gh api graphql -f query='
query {
  repository(owner: "stacklok", name: "toolhive-registry-server") {
    pullRequest(number: 20) {
      reviewThreads(first: 20) {
        nodes {
          id
          isResolved
          comments(first: 10) {
            nodes {
              id
              path
              line
              body
              author { login }
            }
          }
        }
      }
    }
  }
}' | jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false)'
```

### Step 2: Present to user for approval

```
Found 2 unresolved threads fixed in commit c4bb55d:

1. pkg/versions/version.go:24 - "Fix nolint spacing"
2. cmd/thv-registry-api/app/commands.go:53 - "Handle GetString error"

Reply "Fixed in c4bb55d" to both and resolve? (y/n)
```

### Step 3: Reply to each comment (if user approves)

```bash
# Reply to first comment
gh api -X POST repos/stacklok/toolhive-registry-server/pulls/20/comments/2445150488/replies \
  -f body="Fixed in c4bb55d"

# Reply to second comment
gh api -X POST repos/stacklok/toolhive-registry-server/pulls/20/comments/2445150511/replies \
  -f body="Fixed in c4bb55d"
```

### Step 4: Resolve both threads

```bash
# Resolve first thread
gh api graphql -f query='
mutation {
  resolveReviewThread(input: {threadId: "PRRT_kwDOP_5nS85emMpx"}) {
    thread { id isResolved }
  }
}'

# Resolve second thread
gh api graphql -f query='
mutation {
  resolveReviewThread(input: {threadId: "PRRT_kwDOP_5nS85emMqG"}) {
    thread { id isResolved }
  }
}'
```

---

## Example 3: Answering a Question (Don't Auto-Resolve)

**Scenario:** Reviewer asks why you chose a specific approach. Answer but wait for acknowledgment.

### Step 1: Draft response for user approval

```
Reviewer asked: "Why use buffered channel here?"

Draft response:
"Good question! The buffered channel prevents blocking when the producer
is faster than the consumer. In our case, the metrics collector can
generate events faster than the writer can persist them, so the buffer
(size 100) acts as a temporary queue to smooth out the bursts.

Alternative would be unbuffered channel, but that would slow down the
collector. I've added a comment in the code to explain this trade-off."

Send this response? (y/n/edit)
```

### Step 2: Send reply (after user approval)

```bash
gh api -X POST repos/stacklok/toolhive-registry-server/pulls/20/comments/2445160000/replies \
  -f body="Good question! The buffered channel prevents blocking when the producer is faster than the consumer. In our case, the metrics collector can generate events faster than the writer can persist them, so the buffer (size 100) acts as a temporary queue to smooth out the bursts.

Alternative would be unbuffered channel, but that would slow down the collector. I've added a comment in the code to explain this trade-off."
```

### Step 3: Don't resolve yet

**Important:** Leave the thread unresolved until the reviewer acknowledges or approves your answer.

---

## Example 4: Disagree Respectfully

**Scenario:** Reviewer suggests a change you don't think is needed. Explain your reasoning.

### Step 1: Draft respectful disagreement

```
Reviewer suggested: "Extract this into a separate function"

Draft response:
"I considered this, but decided against it for a few reasons:

1. The logic is only used once in this specific context
2. Extracting would require passing 4 parameters
3. The current form keeps the error handling localized

However, if you feel strongly about it, I'm happy to refactor! Let me
know your thoughts.

Related discussion: https://github.com/org/repo/discussions/123"

Send this response? (y/n/edit)
```

### Step 2: Send and wait for discussion

Don't resolve - this is now a discussion thread. Resolve only after reaching agreement.

---

## Example 5: Already Fixed in Earlier Commit

**Scenario:** Reviewer comments on something already fixed before the review was submitted.

### Response:

```bash
gh api -X POST repos/stacklok/toolhive-registry-server/pulls/20/comments/2445170000/replies \
  -f body="Good catch! This was actually already fixed in an earlier commit (ab956b8) before this review. The updated code now handles this case correctly.

See: https://github.com/stacklok/toolhive-registry-server/commit/ab956b8#diff-abc123"
```

Then resolve immediately since it's already addressed.

---

## Example 6: Need More Context

**Scenario:** Review comment isn't clear. Ask for clarification.

### Response:

```bash
gh api -X POST repos/stacklok/toolhive-registry-server/pulls/20/comments/2445180000/replies \
  -f body="Thanks for the feedback! Could you clarify what you mean by 'handle the edge case'?

Are you referring to:
- When the input is nil?
- When the slice is empty?
- When the index is out of bounds?

Once I understand which case you're concerned about, I'll make sure it's properly handled."
```

Leave unresolved until clarified and fixed.

---

## Example 7: Acknowledge Non-Blocking Suggestion

**Scenario:** Reviewer made an optional suggestion you won't implement right now.

### Response:

```bash
gh api -X POST repos/stacklok/toolhive-registry-server/pulls/20/comments/2445190000/replies \
  -f body="Great suggestion! I agree this would be a nice improvement.

For this PR, I'd like to keep the scope focused on the immediate fix, but I've created issue #456 to track this enhancement for a future PR.

Thanks for the idea!"
```

Resolve after user approves (since you've addressed it by creating an issue).

---

## Command Reference

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

### Find thread ID for a specific comment
```bash
gh api graphql -f query='...' | \
  jq '.data.repository.pullRequest.reviewThreads.nodes[] |
      select(.comments.nodes[0].id == COMMENT_ID) |
      {threadId: .id, isResolved}'
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

### Unresolve a thread (if needed)
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

---

## Tips for Each Scenario

### For "Fixed in Commit" Responses
- Include the short SHA (first 7 chars)
- Optionally link to the commit or diff
- Resolve immediately after replying
- Batch process multiple if same commit

### For Questions
- Draft answer first, get user approval
- Be thorough but concise
- Include links to relevant docs/code
- Don't auto-resolve - wait for acknowledgment

### For Disagreements
- Be respectful and explain reasoning
- Offer alternatives or compromise
- Link to relevant discussions or standards
- Never resolve - let discussion conclude naturally

### For Clarifications
- Ask specific questions
- Offer multiple interpretations
- Be open to learning
- Resolve only after understanding and fixing

### For Optional Suggestions
- Acknowledge the value
- Explain if deferring (create issue)
- Thank the reviewer
- Can resolve if properly acknowledged
