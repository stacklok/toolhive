# Release Notes Template

Use this template to produce the final release notes body. Omit any section
that has zero entries — do not include empty headers.

Replace placeholders (`<...>`) with actual content. Emoji shortcodes are written
literally here for clarity — render them as actual emoji in the final output.

---

```markdown
# 🚀 **Toolhive vX.Y.Z is live!**

<one-to-two sentence theme summary of this release>

## ⚠️ Breaking Changes

<for each breaking change:>
- **<title>** — <one-liner: what breaks and what to do> ([migration guide](#migration-guide-anchor))

<for each breaking change, a collapsible migration guide:>

<details>
<summary><strong>Migration guide: <title></strong></summary>

<description of who is affected>

### Before

```yaml
<old manifest or config>
```

### After

```yaml
<new manifest or config>
```

### Migration steps

1. <step>
2. <step>
3. <step>

*PR: [#NNN](https://github.com/stacklok/toolhive/pull/NNN) — Closes [#NNN](https://github.com/stacklok/toolhive/issues/NNN)*

</details>


## 🔄 Deprecations

<for each NEW deprecation in this release — do not carry forward old ones:>
- **`field.or.feature`** deprecated in favour of `replacement` — will be removed in <version> ([#NNN](https://github.com/stacklok/toolhive/pull/NNN))


## 🆕 New Features

- <one-sentence user impact> ([#NNN](https://github.com/stacklok/toolhive/pull/NNN))

## 🐛 Bug Fixes

- <one-sentence description> ([#NNN](https://github.com/stacklok/toolhive/pull/NNN))

## 🧹 Misc

- <one-sentence description> ([#NNN](https://github.com/stacklok/toolhive/pull/NNN))

## 📦 Dependencies

<table of dependency updates from renovate/dependabot PRs:>

| Module | Version |
|--------|---------|
| `module/name` | vX.Y.Z |


👋 Welcome to our newest contributors: **@handle** 🎉

<details>
<summary><strong>Full commit log</strong></summary>

<paste the GitHub auto-generated "What's Changed" block here verbatim,
including PR titles, @author links, and the "New Contributors" sub-section
if present>

</details>

🔗 Full changelog: https://github.com/stacklok/toolhive/compare/vPREVIOUS...vCURRENT
```

---

## Section rules

| Section | When to include | Content guidance |
|---------|----------------|------------------|
| Breaking Changes | At least one breaking change confirmed by expert agent or PR checkbox | One-liner at top + collapsible migration guide with before/after examples |
| Deprecations | At least one NEW deprecation introduced in this release | One-liner with replacement, removal version, and PR link |
| New Features | At least one user-facing feature added | One sentence, lead with user impact, PR link at end |
| Bug Fixes | At least one bug fixed | One sentence, PR link at end |
| Misc | Any internal changes (refactors, tests, CI, naming) | One sentence, PR link at end |
| Dependencies | Any renovate/dependabot PRs | Table of module name + version |
| New Contributors | GitHub auto-generated section lists new contributors | Celebrate them by handle |
| Full Commit Log | Always | Verbatim GitHub auto-generated "What's Changed" block inside `<details>` |

## Writing guidelines

- **One sentence per bullet** — lead with user impact, not implementation detail.
- **Breaking change one-liners** must say what breaks and what the user must do.
- **Migration guides** always include before/after YAML or code, plus numbered steps.
- **Do not reformat the auto-generated commit log** — paste it exactly as GitHub produces it.
- **Link PRs** as `[#NNN](url)` — not bare numbers.
