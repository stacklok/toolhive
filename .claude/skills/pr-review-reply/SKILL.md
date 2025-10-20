---
name: PR Review Reply
description: Reply to and resolve GitHub PR review comments using the GitHub CLI and GraphQL API.
---

# PR Review Reply

Reply to and resolve GitHub PR review comments using the GitHub CLI and GraphQL API in an interactive, collaborative workflow.

## Instructions

You are helping the user respond to review comments on their GitHub Pull Request. This is a **collaborative process** - you gather information, make recommendations, but the user decides how to respond.

### Prerequisites

- GitHub CLI (`gh`) must be installed and authenticated
- User must have write access to the repository
- PR must exist and be open

### Workflow

#### 1. Gather Review Comments

Fetch all review comments from the PR and present them to the user in an organized way:

**For each comment show:**
- Author and timestamp
- File and line number
- Comment body
- Any existing replies
- Resolution status

**Group comments by:**
- Status: unresolved vs resolved
- Type: suggestions, questions, nitpicks, critical issues
- Author: group by reviewer

#### 2. Analyze and Recommend

For each unresolved comment, provide a recommendation:

**If code needs fixing:**
- "Recommendation: Fix the issue, then reply with commit SHA and resolve"
- Explain what needs to be done
- Offer to help implement if appropriate

**If it's a question:**
- "Recommendation: Answer the question, wait for acknowledgment before resolving"
- Draft a potential answer for user approval

**If it's a suggestion to consider:**
- "Recommendation: Discuss trade-offs, decide with user whether to implement"
- Present pros/cons

**If already addressed:**
- "Recommendation: Reply with commit reference and resolve immediately"
- Show which commit fixed it

#### 3. Get User Decisions

**Present summary:**
```
Found 5 unresolved review comments:

1. [Critical] pkg/versions/version.go:24 - @Copilot
   "Fix nolint spacing"
   Status: Fixed in commit c4bb55d
   Recommendation: Reply "Fixed in c4bb55d" and resolve

2. [Nitpick] cmd/app/commands.go:53 - @Copilot
   "Handle GetString error"
   Status: Fixed in commit c4bb55d
   Recommendation: Reply "Fixed in c4bb55d" and resolve

3. [Question] pkg/server/handler.go:45 - @reviewer
   "Why use buffered channel here?"
   Status: Needs answer
   Recommendation: Draft response for your review

How would you like to proceed?
- Reply and resolve all fixed items (1-2)
- Draft responses for questions (3)
- Process individually
- Custom approach
```

#### 4. Execute User's Choice

Based on user decisions:
- Draft reply messages for approval
- Submit replies after user confirms
- Resolve threads only when user approves
- Track progress with TodoWrite

#### 5. Report Results

After processing, show:
- What was done (replied/resolved)
- What remains (still needs attention)
- Any errors or issues
- Next steps if any

## Interactive Decision Points

### Before Replying
**Ask:** "Here's my draft reply: '{message}'. Send this?"
- User can edit, approve, or skip

### Before Resolving
**Ask:** "Mark this thread as resolved?"
- Only if issue is truly addressed
- User may want to wait for reviewer acknowledgment

### For Bulk Operations
**Ask:** "I found 5 comments fixed in commit abc123. Reply 'Fixed in abc123' to all and resolve?"
- Show list of affected comments
- Let user review before executing

## Best Practices

### Reply Messages
- **Be specific**: Reference commit SHAs when applicable
- **Be helpful**: Explain reasoning, not just "fixed"
- **Be respectful**: Thank reviewers for feedback
- **Use markdown**: Format code, lists, links

### When to Resolve
✅ **Resolve when:**
- Issue is fixed and committed
- Question answered and acknowledged
- Discussion concluded with agreement
- User confirms it's complete

❌ **Don't auto-resolve:**
- Without user confirmation
- When still discussing
- When waiting for reviewer response
- When unsure about the fix

### Workflow Strategy
1. **Gather all context** before recommending
2. **Present organized summary** to user
3. **Make clear recommendations** with reasoning
4. **Get user approval** before taking action
5. **Execute batch operations** efficiently with consent
6. **Report back** what was done

## Collaboration with Specialized Agents

When appropriate, use the Task tool to launch specialized agents for complex review comments:

**Pattern:**
1. Identify comment needs specialized expertise
2. Launch appropriate agent to analyze and draft response
3. Present agent's recommendation to user
4. User approves/edits before sending

**Examples:**
- Security-related comments → security review agent
- Complex architectural questions → expert developer agent
- Domain-specific concerns → relevant expert agent
- Codebase-specific questions → project expert agent

## Error Handling

If errors occur:
- Explain what went wrong clearly
- Suggest corrective action
- Don't retry without user approval
- Offer to show raw error for debugging

## See Also

- [EXAMPLES.md](EXAMPLES.md) - Common scenarios with actual commands
- [pr-inline-review](../pr-inline-review/skill.md) - For submitting review comments
