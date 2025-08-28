# MCP Server Registry Management Process

## Overview
This document outlines the processes for managing MCP (Model Context Protocol) servers within the ToolHive registry, covering adding, removing, appealing decisions, and handling duplicate submissions.

> **⚠️ Registry Migration Notice**
>
> The ToolHive registry has been migrated to a separate repository for better management and maintenance.
>
> **To add or modify MCP servers, please visit: https://github.com/stacklok/toolhive-registry**

## Adding MCP Servers

**New Process (Current):**
1. Visit the [toolhive-registry repository](https://github.com/stacklok/toolhive-registry)
2. Follow the contribution guidelines in that repository
3. Submit PR with required server definition files
4. Automated technical verification and building
5. Manual review by registry maintainers
6. Final approval and automatic release

**Legacy Process (Deprecated):**
1. ~~Submit PR with required registry.json content~~ (No longer used)
2. ~~Automated technical verification~~
3. ~~Manual review by registry maintainers~~
4. ~~Final approval or rejection decision~~

## Removing MCP Servers
1. Automated non-compliance detection
2. Notification to registry maintainers
3. Grace period for remediation
4. Final review and decision
5. Public notification with reasoning

## Appeals Process
- Open to MCP server users and maintainers
- Based on objective criteria
- Transparent communication of outcomes

## Handling Duplicates
- Assess functional differentiation from existing entries
- Prioritize based on:
    - Community adoption and activity levels
    - Overall code quality
    - Long-term viability and backing
- Add deprecation notices before removal (1-2 month transition period)
- Document rationale for decisions
