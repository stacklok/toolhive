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
      ],
      "remote_servers": [
        /* remote server entries following same format as top-level remote_servers */
      ]
    }
  ]
}
```

### Group Structure

- `name`: Human-readable group identifier
- `description`: Descriptive text for discoverability (unused otherwise)
- `servers`: Array of server metadata following the same format as top-level servers
- `remote_servers`: Array of remote server metadata following the same format as top-level remote_servers

### Example

```json
{
  "servers": {
    /* existing MCP server entries */
  },
  "remote_servers": {
    /* existing remote server entries */
  },
  "groups": [
    {
      "name": "mobile-app-team-toolkit",
      "description": "MCP servers for the mobile app development team's workflows",
      "servers": {
        "fetch": {
          "description": "Allows you to fetch content from the web",
          "tier": "Community",
          "status": "Active",
          "transport": "streamable-http",
          "tools": [
            "fetch"
          ],
          "metadata": {
            "stars": 17,
            "pulls": 12390,
            "last_updated": "2025-09-05T02:29:06Z"
          },
          "repository_url": "https://github.com/stackloklabs/gofetch",
          "image": "ghcr.io/stackloklabs/gofetch/server:0.0.6",
          "permissions": {
            "network": {
              "outbound": {
                "insecure_allow_all": true,
                "allow_port": [
                  443
                ]
              }
            }
          },
          "provenance": {
            "sigstore_url": "tuf-repo-cdn.sigstore.dev",
            "repository_uri": "https://github.com/StacklokLabs/gofetch",
            "signer_identity": "/.github/workflows/release.yml",
            "runner_environment": "github-hosted",
            "cert_issuer": "https://token.actions.githubusercontent.com"
          }
        },
        "internal-company-server": {
          "description": "Allows you to view and manage proprietary resources",
          "tier": "Community",
          "status": "Active",
          "transport": "streamable-http",
          "tools": [
            "get_internal_data"
          ],
          "repository_url": "https://github.com/internal/internal",
          "image": "ghcr.io/internal/internal/server:0.0.6",
          "provenance": {
            "sigstore_url": "tuf-repo-cdn.sigstore.dev",
            "repository_uri": "https://github.com/internal/internal",
            "signer_identity": "/.github/workflows/release.yml",
            "runner_environment": "github-hosted",
            "cert_issuer": "https://token.actions.githubusercontent.com"
          },
          "env_vars": [
            {
              "name": "TEAM_ID",
              "description": "Internal team identifier",
              "required": true,
              "default": "mobile-app-team"
            },
            {
              "name": "USER_ID",
              "description": "Internal user identifier",
              "required": true,
              "secret": true
            }
          ]
        }
      },
      "remote_servers": {
        "huggingface": {
          "description": "Official Hugging Face MCP server for models, datasets, and research papers",
          "tier": "Official",
          "status": "Active",
          "transport": "streamable-http",
          "tools": [
            "hf_whoami",
            "space_search",
            "model_search",
            "model_details",
            "paper_search",
            "dataset_search",
            "dataset_details",
            "hf_doc_search",
            "hf_doc_fetch",
            "gr1_flux1_schnell_infer"
          ],
          "url": "https://huggingface.co/mcp"
        }
      }
    }
  ]
}
```

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