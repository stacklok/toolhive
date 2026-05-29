---
name: audit-trifecta
description: >-
  Audit a ToolHive group of MCP servers for "lethal trifecta" / toxic-flow risk
  — the co-location of private-data access, untrusted-content exposure, and an
  exfiltration path in one agent context. Use when checking whether a group is
  safe from prompt-injection data exfiltration, reviewing MCP server combinations
  for security, interpreting `thv audit-trifecta` output, or writing overrides for
  mis-classified servers. NOT for running/managing servers (use toolhive-cli-user)
  or Kubernetes operator audits.
license: Apache-2.0
metadata:
  version: 0.1.0
---

# Audit a ToolHive Group for Lethal-Trifecta Risk

## What this checks

The **lethal trifecta** is the co-location, in a single agent context, of three
capabilities. ToolHive models them as roles in a **toxic flow**:

- **data** — access to private/sensitive data (filesystem reads, secrets, authed backends)
- **source** — exposure to untrusted content, a prompt-injection vector (web fetch, email, issues)
- **sink** — the ability to exfiltrate (network egress, remote endpoints)

A ToolHive **group** is a set of MCP servers that share one agent's context, so a
toxic flow exists whenever the group contains all three roles: an injection via a
*source* server can read a *data* server's secrets and send them out a *sink*.

ToolHive assesses **data** and **sink** confidently (they come from the permission
profile) but **source** poorly — untrusted-content exposure has no first-class
signal. Expect `indeterminate` verdicts until source servers are classified or
overridden.

## Prerequisites

- `thv` built with the `audit-trifecta` subcommand (currently hidden/experimental).
- At least one group with workloads. List groups with `thv group list`.

## Instructions

1. **Run the audit** for the group in question:
   ```bash
   thv audit-trifecta <group>
   thv audit-trifecta --all              # every group
   thv audit-trifecta <group> --explain  # show the evidence behind each finding
   ```

   Improve accuracy two ways (both optional, both safe — they can only *raise*
   suspicion, never produce a false "no toxic flow"):
   ```bash
   # Probe running (actively-proxied) servers for live tool annotations
   thv audit-trifecta <group> --live

   # Use an LLM to judge untrusted-content/private-data from descriptions
   # (falls back to keyword search if model/base-url are unset)
   thv audit-trifecta <group> \
     --llm-model gpt-4o-mini --llm-base-url https://api.openai.com/v1
   # API key via --llm-api-key or the THV_AUDIT_LLM_API_KEY env var
   # (key is optional — omit it for keyless local models)

   # Or reuse a running `thv llm` proxy (it injects auth; no key needed)
   thv audit-trifecta <group> --llm-proxy --llm-model <model>
   ```
   The `--llm-base-url` must speak the OpenAI-compatible `/v1/chat/completions`
   API. `--llm-proxy` points at the local `thv llm` reverse proxy (must be
   running via `thv llm proxy`), which handles gateway auth for you.
   The LLM only ever receives **public** metadata (name, description, tags, tool
   names) — never permission profiles, secrets, or config. Point `--llm-base-url`
   at a local model to keep traffic on-host.

2. **Read the verdict** (see [Verdicts](#verdicts)). The text output shows a
   per-server role table, then the contributing servers per role and a one-line
   recommendation.

3. **For `present` or `possible`** — there is a real or likely toxic flow.
   Recommend remediation in this priority order (see [Remediation](#remediation)):
   split the group → restrict egress on data-holders → drop the source.

4. **For `indeterminate`** — data and sink exist but source is unknown for the
   listed `Unclassified` servers. Investigate each: does it ingest content the
   user does not control (web pages, emails, issue/PR bodies, arbitrary files)?
   - If **yes**, it is a source → the verdict becomes `present`/`possible`; remediate.
   - If **no**, write an override setting its `source` to `none` (see
     [Overrides](#overrides)). The test for "no" is strict: the server ingests
     **no content any external party can influence**. "First-party" is not
     sufficient — an internal wiki, issue tracker, or mailbox is first-party
     *infrastructure* but routinely carries attacker-influenced *content*
     (a comment, a PR body, a forwarded email). Those are still sources.

5. **For `none`** — at least one leg is confidently absent; the group is safe from
   this class of attack. State that plainly.

6. **Re-run with `--overrides`** after writing any overrides to confirm the new
   verdict:
   ```bash
   thv audit-trifecta <group> --overrides ./trifecta-overrides.json
   ```

## Verdicts

| Verdict | Meaning | Action |
|---|---|---|
| `present` | All three roles at high confidence | Remediate now |
| `possible` | All three present, some at low confidence | Review contributors, likely remediate |
| `indeterminate` | data + sink present, source unknown | Classify the unclassified servers or override |
| `none` | A leg is confidently absent | Safe — no action |

## Remediation

Lead with the cheapest effective fix:

1. **Split the group** — move the source server (or the data server) into its own
   group so they no longer share one agent context. This breaks the flow
   structurally and is usually the right answer.
2. **Restrict egress** — tighten the sink server's permission profile to an
   allowlist (`network.outbound.allow_host`) or `isolate_network`, so even a
   successful injection cannot reach an exfiltration destination.
3. **Drop the source** — remove the untrusted-content server from the group if it
   is not essential.

Do not recommend "just be careful" — the trifecta is an architecture problem; the
fix is to remove one leg from the shared context.

## Overrides

Overrides correct mis-classified servers. They are top-priority evidence, so use
them deliberately and always with a real `reason` (it is the audit trail).

Write a JSON array to a file (e.g. `trifecta-overrides.json`):

```json
[
  {
    "server": "intranet-docs",
    "role": "source",
    "confidence": "none",
    "reason": "reads only the first-party internal wiki, not untrusted content"
  },
  {
    "server": "weather",
    "role": "sink",
    "confidence": "none",
    "reason": "egress restricted to api.weather.gov, not an exfiltration channel"
  }
]
```

Fields:
- `server` — workload name. An empty string applies the override to **every**
  server in the group — a blunt instrument; prefer naming a specific server.
- `role` — `data`, `source`, or `sink`.
- `confidence` — `none`, `unknown`, `possible`, or `likely`.
- `reason` — required justification; surfaced in `--explain` and the audit trail.

Overrides are validated on load: an unknown `role`/`confidence` or a missing
`reason` fails with an error rather than silently degrading a finding. Overridden
values are marked with `*` in the default output.

**Only override what you have verified.** Setting `source: none` on a server that
*does* ingest untrusted content silently re-opens the trifecta. When in doubt,
investigate rather than override.

## Error Handling

- **"specify a group name or use --all"** — pass a group name or `--all`.
- **Empty / `(no servers in group)`** — the group has no workloads; check
  `thv group list` and `thv list --group <group>`.
- **Everything `indeterminate`** — expected when servers are not in the registry
  and have no tool annotations; classify sources via overrides, or wait for live
  annotation probing.

## See Also

- `toolhive-cli-user` skill — running, grouping, and configuring MCP servers.
- `thv audit-trifecta --help` — full flag reference.
