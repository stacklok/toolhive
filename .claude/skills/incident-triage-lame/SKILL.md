---
name: incident-triage-lame
description: Triage active PagerDuty incidents by gathering context from all available services one tool call at a time.
allowed-tools: Bash
---

# Incident Triage (No Scripting)

We have active PagerDuty incidents. Build an incident triage report by gathering data from every available service.

Do NOT use `execute_tool_script`. Call each tool individually, one at a time.

1. Check PagerDuty for service health and active incidents
2. For each degraded service, gather context from Datadog (metrics + logs), GitHub (recent PRs), Slack (#incidents messages), Jira (related issues), and Confluence (runbooks)
3. Cross-reference the results to identify probable root causes, who's engaged, and what runbooks apply

Format the final report as markdown matching this structure exactly:

```
# Incident Triage Report

## Service Health
<paste service health output verbatim>

## Active Incidents
<paste incidents output verbatim>

## Degraded Service: <name>
### Metrics
<paste metrics verbatim>
### Error Logs
<paste logs verbatim>
### Recent PRs (Potential Root Causes)
<paste prs verbatim>
### Slack #incidents Context
<paste messages verbatim>
### Related Jira Issues
<paste jira verbatim>
### Runbooks
<paste runbooks verbatim>

(repeat for each degraded service)
```

Include the raw tool output under each heading — do not summarize or rewrite it.
