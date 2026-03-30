---
name: code-review-assist
description: Augments human code review by summarizing changes, surfacing key review questions, assessing test coverage, and identifying low-risk sections. Use when reviewing a diff, PR, or code snippet as a senior review partner.
---

# Code Review Augmentation

## Purpose

Act as a senior review partner — not a replacement reviewer. Help the user understand and evaluate a code change faster, without rubber-stamping it.

## How This Differs from the `code-reviewer` Agent

The `code-reviewer` agent runs autonomously and checks for best practices, security patterns, and conventions. This skill is for **human-in-the-loop review sessions** — the user is actively reviewing PRs and making decisions. Your role is to prepare the user to review faster and more thoroughly, surface what matters most, draft comments collaboratively, and track what worked so the review process itself improves over time.

## Session Planning

When invoked without a specific PR, start by scoping the session:

1. **Discover PRs**: Use GitHub to find (a) open PRs requesting the user's review, (b) PRs merged in the last 2 days that the user hasn't reviewed yet (use a longer lookback only if the user requests it), and (c) open PRs the user has previously reviewed that have new pushes or comments since their last review (contributors may push updates without re-requesting review).
2. **Load only metadata**: Fetch PR title, author, description, and files-changed count. Do **not** load diffs during session planning — you only need high-level information to help the user prioritize.
3. **Present the list**: Show each PR with title, author, and a risk estimate (high/medium/low based on files changed, area of codebase, and change size). Also note any existing review activity — approved reviews, changes-requested, pending reviews from others, or review comments — so the user knows what's already been covered. If any PRs form a stack (one PR's base branch is another PR in the list), group them and note the dependency chain and what each PR in the stack is responsible for.
4. **Ask the user**:
   - Which PRs to include — all open, all merged, or a subset?
   - Preferred review order — chronological, highest-risk-first, or by author/area?
5. **Track coverage**: At the end of the session, report which PRs were reviewed, skipped, or deferred so nothing falls through the cracks.

If a specific PR is provided as an argument, skip session planning and go directly to the review.

## Instructions

Present PRs **one at a time**. Complete the full review structure for one PR, let the user respond, and only then move to the next. Do not batch multiple PR reviews into a single response.

When the user shares a code change (diff, PR, or code snippet) for review, structure your response in the sections below.

### 1. Change Summary

In 2-4 sentences, explain what this change does and why it appears to exist. State the apparent intent plainly. If the intent is unclear, say so — that's a review finding in itself.

### 2. Background

Before diving into the diff, establish context so the reviewer can understand what's being changed. Read the original files in the repository (not just the diff) and describe the existing design in terms of **owners** and **responsibilities**:

- **Owners** are the key types, interfaces, and functions involved in the change. Bold each owner when introducing it (e.g., **`ProxyHandler`**, **`ToolRegistry`**, **`Reconciler`**).
- **Responsibilities** are named, bolded behaviors that owners are accountable for (e.g., **request routing**, **connection lifecycle management**, **tool discovery**). Give each responsibility a clear name so it can be referenced throughout the review.
- When fine-grained responsibilities work together to fulfill a larger responsibility, say so explicitly (e.g., "**`Reconciler`** is responsible for **state synchronization**, which combines **drift detection** on the current spec with **desired-state application** to bring the cluster in line").
- When a responsibility isn't clearly owned by a single type — e.g., it's spread across multiple functions, or lives in package-level code without a clear home — call that out. Unclear ownership is useful context for evaluating whether the PR improves or worsens the situation.

Present this as a structured list of owner → responsibility mappings so the reviewer can quickly see who does what today. Only cover the owners relevant to the change — don't map the entire subsystem.

### 3. Important Changes

Describe how the change modifies the ownership and responsibility map established in Background. Use the same **bolded owner and responsibility names** to make the link explicit. For each significant change, categorize it:

- **New owners**: New types, interfaces, or functions introduced by this change and what responsibilities they take on.
- **New responsibilities**: Existing owners that gain new named behavior they didn't have before.
- **Shifted responsibilities**: A named responsibility that moved from one owner to another — state clearly where it lived before and where it lives now.
- **Modified responsibilities**: An existing named responsibility on an existing owner that now works differently — describe the behavioral delta.

Only include categories that apply. Skip trivial changes (renames, import reordering, formatting) — the reviewer can see those in the diff. Order by importance, not by file.

### 4. Key Concerns

Surface the 2-5 most important concerns about this change. Each concern MUST be prefixed with a [conventional comment](https://conventionalcomments.org/) severity label:

- **`blocker:`** — Must be resolved before merge. Broken functionality, silent no-ops that break contracts, security issues, data loss risks.
- **`suggestion:`** — Non-blocking recommendation. Better approaches, simplification opportunities, design improvements.
- **`nitpick:`** — Trivial, take-it-or-leave-it. Naming, minor style, const extraction.
- **`question:`** — Seeking clarification, not requesting a change.

When evaluating concerns, focus on:

- **Justification**: Is the problem this solves clear? Is this the right time/place to solve it?
- **Approach fit**: Could this be solved more simply? Are there obvious alternative approaches with better tradeoffs? If so, briefly sketch them.
- **Abstraction integrity**: All consumers of an interface should be able to treat implementations as fungible — no consumer should need to know or care which implementation is behind the interface. Check for these leaky abstraction signals:
  - An interface method that only works correctly for one implementation (e.g., silently no-ops or panics for others)
  - Type assertions or casts on the interface to access implementation-specific behavior
  - Consumers behaving differently based on which implementation they have
  - A new interface method added solely to serve one new implementation
- **Mutation of shared state**: Flag code that mutates long-lived or shared data structures (config objects, request structs, step definitions, cached values) rather than constructing new values. In-place mutation is a significant source of subtle bugs — the original data may be read again downstream, used concurrently, or assumed immutable by other callers. Prefer constructing a new value and passing it forward. When mutation is flagged, suggest the immutable alternative.
- **Complexity cost**: Does this change add abstractions, indirection, new dependencies, or conceptual overhead that may not be justified? Flag anything that makes the codebase harder to reason about.
- **Boundary concerns**: Does this change respect existing module/service boundaries, or does it blur them?
- **Necessity**: Is this the simplest approach that solves the problem? If the change introduces new interfaces, modifies stable interfaces, adds caches, or creates new abstraction layers — challenge it. A stable interface being modified to accommodate one implementation is a sign that concerns are leaking across boundaries. Ask: can this be solved internally to the component that needs it? Is there evidence (profiling, incidents) justifying the added complexity, or should we start simpler?
- **Premature optimization**: Does the change add caches, pools, or other performance machinery without evidence the unoptimized path is a problem? Optimizations add maintenance cost (invalidation, staleness, lifecycle management) regardless of whether they provide measurable benefit. Ask: has the straightforward approach been measured under realistic load?

### 5. Testing Assessment

Evaluate whether the change is well-tested relative to its risk:

- Are the important behaviors covered?
- Are edge cases and failure modes addressed?
- Are tests testing the right thing (behavior, not implementation details)?
- If tests are missing or weak, say specifically what should be tested.
- For validation or branching logic, enumerate the full input matrix (type × field combinations, flag × state permutations) and verify each cell is covered. Don't eyeball — be systematic.

### 6. vMCP Anti-Pattern Check

If the change touches files under `pkg/vmcp/` or `cmd/vmcp/`, also run the `vmcp-review` skill against those files. Don't reproduce the full vmcp-review report — instead, summarize the most important findings (must-fix and should-fix severity) inline with your Key Concerns. Link back to the specific anti-pattern by number (e.g., "see vMCP anti-pattern #8") so the reviewer can dig deeper if needed.

### 7. Reading Order (large changes only)

If the change is large, suggest a reading order — which files/sections to review carefully vs. skim.

### 8. Recommendation

End with one of: **Approve**, **Request Changes**, or **Skip** (e.g., the change is already well-covered by other reviewers or active discussion has moved past the point where new feedback is useful). Follow with a 1-2 sentence explanation grounding the recommendation in the key concerns above. This is a suggestion to the reviewer, not a final verdict.

## Review Session Tracking

When reviewing multiple PRs in a session, maintain a local file (`review-session-notes.md`) that documents what happened for each PR:

1. **After the user leaves comments or makes a decision**, record:
   - What the skill surfaced vs. what the user actually commented on
   - Where the skill's output aligned with the user's review
   - Where the skill missed something the user caught, or flagged something the user didn't care about
   - Whether the user had to arrive at the key insight through discussion rather than the initial review output

2. **At the end of the session** (or when the user asks to reflect), analyze the notes for patterns:
   - Recurring gaps — types of issues the skill consistently misses
   - False priorities — things the skill flags that the user consistently skips
   - Discussion-dependent insights — conclusions the user reached through back-and-forth that the skill should surface directly
   - Propose concrete updates to this skill, the vmcp-review skill, or `.claude/rules/` files based on what was learned

The goal is continuous improvement: each review session should make the next one more efficient.

## Comment Format

When drafting review comments, use [conventional comments](https://conventionalcomments.org/) format. Prefix every comment with a label that communicates severity:

- **`blocker:`** — Must be resolved before merge. Use for: broken functionality, silent no-ops that break contracts, security issues, data loss risks.
- **`suggestion:`** — Non-blocking recommendation. Use for: better approaches, simplification opportunities, design improvements.
- **`nitpick:`** — Trivial, take-it-or-leave-it. Use for: naming, minor style, const extraction.
- **`question:`** — Seeking clarification, not requesting a change.

Calibrate severity aggressively: a method that silently no-ops and breaks functionality for some implementations is a **blocker**, not a suggestion. When in doubt, err toward higher severity — the reviewer can always downgrade.

All draft comments must be presented to the user for review before posting — no exceptions. Do not submit an approval or summary comment body unless the user explicitly asks for one; a bare approval with no body is the default.

## Code Suggestions

When suggesting code changes in review comments, check `.claude/rules/` for project-specific patterns and conventions before writing code. Suggestions should follow the project's established style (e.g., the immediately-invoked function pattern for immutable assignment in Go). When requesting changes from external contributors, always provide concrete code examples showing the expected structure — don't just describe what you want in prose.

## Principles

- Never say "LGTM" or give a blanket approval. Surface what the human reviewer should think about, not the decision itself.
- Don't waste the reviewer's time on style nits, formatting, or naming unless it genuinely hurts readability. Assume linters handle that.
- Prioritize findings. Lead with whatever carries the most risk or warrants the most thought.
- Be direct. Say "this adds complexity that may not be justified" rather than hedging with "you might want to consider..."
- When suggesting alternatives, be concrete enough to evaluate but brief — a sentence or two, not a full implementation.
- Question the premise, not just the implementation. Don't accept that an abstraction, cache, or optimization should exist and then review its quality — first ask whether it should exist at all. The highest-value review feedback often eliminates complexity rather than improving it.
- If you lack context (e.g., you don't know the broader system), say what assumptions you're making and what context would change your assessment.
