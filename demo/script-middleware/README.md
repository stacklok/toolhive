# Script Middleware Demo

Demonstrates `execute_tool_script` — a Starlark scripting layer that lets agents
orchestrate multiple MCP tool calls in a single atomic operation.

## What this shows

An agent connected to a VirtualMCPServer with 8 enterprise tool backends
(Slack, Jira, GitHub, PagerDuty, Datadog, Confluence, Google Drive, Linear)
uses `execute_tool_script` to gather and cross-reference data across services
in one call instead of 8+ sequential round-trips.

## Setup (local Kind cluster)

### Prerequisites
- `kind`, `kubectl`, `docker` installed
- ToolHive operator image built locally: `task build-all-images`

### Deploy

```bash
# From repo root
./demo/script-middleware/deploy.sh
```

This creates a Kind cluster, installs the operator, deploys 8 dummy MCP servers
and a VirtualMCPServer, and sets up port-forwarding on localhost:4483.

### Connect with Claude Code

```bash
# In Claude Code settings, add as an MCP server:
#   URL: http://localhost:4483/mcp
#   Transport: streamable-http

# Then give Claude this prompt (see below)
```

### Teardown

```bash
kind delete cluster --name script-demo
```

## The Prompt

Give this to Claude (or any MCP-capable agent) after connecting:

> We have active PagerDuty incidents. Use execute_tool_script to build an
> incident triage report by gathering data from every available service.
>
> Write a script that:
> 1. Gets the service health list and active incidents from PagerDuty
> 2. For each service that is NOT "Operational", gathers context:
>    - Datadog metrics and error logs for that service
>    - Recent GitHub PRs (look for potential root cause deploys)
>    - Slack #incidents messages for team context
>    - Related Jira issues
>    - Confluence runbooks
> 3. Parses the text results to extract key details (incident IDs, error
>    messages, who's involved, what was recently deployed)
> 4. Returns a structured dict mapping each degraded service to its
>    full triage context
>
> The script should use loops and string parsing — don't just call each
> tool once, cross-reference the results.

### What the agent should produce

A Starlark script that loops over degraded services, calls 5-6 tools per
service, parses the text output to extract names/IDs/timestamps, and returns
a structured dict. Something like:

```python
services = pagerduty_list_services()
incidents = pagerduty_list_incidents()
report = {}

for line in services.split("\n"):
    if "Degraded" in line or "Critical" in line:
        svc = line.split(" — ")[0].strip()

        metrics = datadog_query_metrics(query=svc, timeframe="last_1h")
        logs = datadog_search_logs(query="ERROR", service=svc)
        prs = github_search_prs(query="merged", repo=svc)
        slack = slack_read_messages(channel="#incidents")
        jira = jira_search_issues(query=svc, project="ENG")
        runbook = confluence_search_pages(query=svc + " runbook")

        # Extract people involved from Slack messages
        people = []
        for msg in slack.split("\n"):
            if "]" in msg:
                who = msg.split("]")[1].split(":")[0].strip()
                if who and who not in people:
                    people.append(who)

        # Find incident IDs for this service
        svc_incidents = []
        for inc in incidents.split("\n"):
            if svc in inc:
                svc_incidents.append(inc.strip())

        report[svc] = {
            "incidents": svc_incidents,
            "metrics_summary": metrics,
            "recent_errors": logs,
            "recent_prs": prs,
            "team_engaged": people,
            "related_jira": jira,
            "runbook": runbook,
        }

return report
```

## Why this is interesting

1. **Loops + conditionals** — the script iterates over degraded services,
   not a static list. The agent writes real control flow.

2. **Cross-referencing** — incident IDs from PagerDuty are matched against
   service names. Slack messages are parsed to extract who's engaged.

3. **8+ tool calls in one round-trip** — without `execute_tool_script`,
   the agent needs sequential calls with model inference between each.
   The script runs server-side and returns one aggregated result.

4. **Text parsing** — the script does string splitting and filtering that
   would otherwise require the model to process raw text from each tool.

## Coherent demo story

The dummy data tells a story: Alice deployed `v2.4.1` which caused the
checkout service to timeout, spiking web-app latency. PagerDuty fired two
incidents (SEV1 checkout, SEV2 web-app). The Slack #incidents channel shows
Alice, Bob, and Carol coordinating. Datadog logs show the exact error chain.
GitHub shows the merged PR that caused it. The script stitches all of this
together into a single triage report.
