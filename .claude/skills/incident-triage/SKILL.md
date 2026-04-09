---
name: incident-triage
description: Triage active PagerDuty incidents by gathering context from all available services using execute_tool_script.
allowed-tools: Bash
---

# Incident Triage

We have active PagerDuty incidents. Use `execute_tool_script` to build an incident triage report by gathering data from every available service in a single scripted call.

Write a Starlark script that:

1. Gets the service health list and active incidents from PagerDuty
2. For each service that is NOT "Operational", gathers context in parallel:
   - Datadog metrics and error logs for that service
   - Recent GitHub PRs (look for potential root cause deploys)
   - Slack #incidents messages for team context
   - Related Jira issues
   - Confluence runbooks
3. Parses the text results to extract key details — incident IDs, error messages, who's involved, what was recently deployed
4. Formats the result as a **markdown report** and returns it as a string

The script should return a ready-to-display markdown string — NOT a dict. Build the markdown inside the script so no post-processing is needed. Structure it like:

```
# Incident Triage Report

## Service Health
<paste service health text>

## Active Incidents
<paste incidents text>

## Degraded Service: <name>
### Metrics
<paste metrics>
### Error Logs
<paste logs>
### Recent PRs (Potential Root Causes)
<paste prs>
### Slack #incidents Context
<paste messages>
### Related Jira Issues
<paste jira>
### Runbooks
<paste runbooks>

(repeat for each degraded service)
```

Use loops over the degraded services and string parsing to cross-reference results.

Use `parallel()` to fan out tool calls concurrently. `parallel()` takes a list of zero-arg callables (use `lambda`) and returns results in order. Fan out all services at once:

```python
def gather_context(svc):
    results = parallel([
        lambda s=svc: datadog_datadog_query_metrics(query=s),
        lambda s=svc: datadog_datadog_search_logs(query=s),
        lambda s=svc: github_github_search_prs(query=s),
        lambda s=svc: slack_slack_read_messages(channel="incidents"),
        lambda s=svc: jira_jira_search_issues(query=s),
        lambda s=svc: confluence_confluence_search_pages(query=s),
    ])
    return results

# Fan out ALL services concurrently (nested parallel)
contexts = parallel([lambda s=svc: gather_context(s) for svc in degraded_services])
```

NOTE: Starlark lambdas capture variables by reference. When using `lambda` inside a loop, bind the loop variable via a default argument to avoid the classic closure bug:
```python
# WRONG — all lambdas see the final value of svc
[lambda: query(svc) for svc in services]
# RIGHT — bind svc at definition time
[lambda s=svc: query(s) for svc in services]
```

IMPORTANT: The script returns a fully formatted markdown report. After calling execute_tool_script, display the result text verbatim. Do NOT summarize, reformat, or add your own analysis — the script output IS the final answer.
