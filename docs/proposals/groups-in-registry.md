# Registry Groups Support

## Problem Statement

Companies want to pre-configure and share group configurations that are specialized for each team. Currently, teams must manually select and configure individual MCP servers, but organizations need a way to package related servers together so teams can quickly set up their entire set of tools with one command.

## Goals

- Add groups to the registry format so companies can share team-specific server collections
- Let people use these pre-configured groups easily
- Keep existing registry format working as before

## Proposed Registry Format

Extend the current registry format to include a top-level `groups` array alongside the existing `servers` array:

```json
{
  "servers": [
    /* existing MCP server entries packaged as (server.json + extensions) */
  ],
  "groups": [
    {
      "name": "Mobile App Team Toolkit",
      "description": "MCP servers for the mobile app development team's workflows",
      "servers": [
        /* server entries following same format as top-level servers */
      ]
    }
  ]
}
```

### Group Structure

- `name`: Human-readable group identifier
- `description`: Descriptive text for discoverability (unused otherwise)
- `servers`: Array of server metadata following the same format as top-level servers

## Implementation Notes

- Groups are purely organizational - servers within groups follow identical format to top-level servers
- Group descriptions enable search functionality but have no runtime impact

## Usage

Groups will be consumed via a new `thv group run` command that takes a group name from the registry, creates the group, and creates all servers within that group.

```bash
thv group run mobile-app-team-toolkit
```

Flags:
I propose starting with a minimal subset of flags, and adding more as needed:
- `--secret` Secrets to be fetched from the secrets manager and set as environment variables (format: NAME,target=SERVER_NAME.TARGET)
- `--env` Environment variables to pass to an MCP server in the group (format: SERVER_NAME.KEY=VALUE)

```bash
thv group run mobile-app-team-toolkit --secret k8s_token,target=k8s.TOKEN --secret playwright_token,target=playwright.TOKEN --env api-server.DEBUG=true
```

## Use Cases

SMEs at companies will create specific groups for teams working on particular products or projects.

## Out of Scope / Future Considerations
- Referencing servers within groups by name. In this proposal, all servers must be fully defined within the group. This avoids the complexity around referencing servers across registries, handling invalid references and override logic.