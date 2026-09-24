---
name: toolhive-cli-user
description: >-
  Guide for using ToolHive CLI (thv) to run and manage MCP servers, skills, and
  AI-client plugins. Use when managing `thv ai-plugin` installs, project sync or
  upgrades, or plugin publishing for Claude Code or Codex; also use for MCP
  server and skill lifecycle commands. Covers server lifecycle, registries,
  secrets, client registration, builds, permissions, skills, and AI plugins.
  NOT for Kubernetes operator usage, ToolHive development/contributing, or
  general AI-client configuration unrelated to ToolHive.
version: 0.4.0
license: Apache-2.0
---

# ToolHive CLI User Guide

## Prerequisites

- **Container Runtime**: Docker, Podman, Colima, or Rancher Desktop (with dockerd/moby)
- **ToolHive CLI**: Install with `brew install stacklok/tap/thv` (macOS/Linux) or `winget install stacklok.thv` (Windows)

Verify: `thv version`

## Quick Start

```bash
thv run filesystem      # Run server from registry
thv list                # List running servers
thv status filesystem   # Detailed server info
thv logs filesystem     # View logs
thv stop filesystem     # Stop server
thv rm filesystem       # Remove server
```

## Running MCP Servers

Five input methods: registry name, container image, protocol scheme (`uvx://`, `npx://`, `go://`), exported config (`--from-config`), or remote URL.

```bash
thv run filesystem                                          # Registry
thv run ghcr.io/github/github-mcp-server:latest -- <args>   # Container image
thv run uvx://mcp-server-git                                # Python (uvx)
thv run npx://@modelcontextprotocol/server-filesystem       # Node.js (npx)
thv run go://github.com/example/mcp-server                  # Go
thv run --from-config ./config.json                         # Exported config
thv run https://api.example.com/mcp --name my-remote        # Remote URL
```

For all flags, authentication options, and telemetry configuration, see [COMMANDS.md](references/COMMANDS.md#thv-run).
For detailed usage patterns, see [EXAMPLES.md](references/EXAMPLES.md).

## Managing Servers

```bash
thv list                          # Running servers
thv list --all                    # Include stopped
thv list --format json            # JSON output
thv list --format mcpservers      # MCP client config format
thv list --group production       # Filter by group

thv status filesystem             # Detailed server info (URL, port, transport, uptime)
thv status filesystem --format json

thv stop filesystem github        # Stop specific servers
thv stop --all                    # Stop all

thv start filesystem              # Resume stopped server
thv start --all                   # Start all stopped servers
thv start --group development     # Start all in group
thv restart filesystem            # Alias for start (backward compat)

thv rm filesystem github          # Remove servers
thv rm --all                      # Remove all

thv logs filesystem               # Container logs
thv logs filesystem --follow      # Real-time
thv logs filesystem --proxy       # Proxy logs
thv logs prune                    # Clean orphaned logs
```

Note: Remote servers trigger fresh OAuth authentication on start.

## Registry Operations

```bash
thv registry list                    # List all servers
thv registry list --format json      # JSON output
thv search github                    # Search by keyword
thv registry info github             # Detailed server info
```

Custom registry:
```bash
thv config set-registry https://my-registry.example.com  # Remote
thv config set-registry /path/to/local/registry          # Local
thv config get-registry                                   # View current
thv config unset-registry                                 # Reset to default
```

## Group Management

All servers are assigned to `default` group unless specified.

```bash
thv group create development           # Create group
thv group list                         # List groups
thv run fetch --group development      # Assign server to group
thv group run kubernetes               # Run all servers from registry group
thv group rm development               # Remove group (servers move to default)
thv group rm development --with-workloads  # Remove group AND its servers
```

Each server belongs to one group. To run same server in multiple groups, create uniquely named instances.

## Secrets Management

Setup is required before use: `thv secret setup` (interactive provider selection).

**Providers:** Encrypted (AES-256-GCM, password in OS keyring) or 1Password (read-only, requires `OP_SERVICE_ACCOUNT_TOKEN`).

```bash
thv secret set MY_API_KEY              # Interactive input
echo "value" | thv secret set MY_KEY   # Piped input
thv secret list                        # List all
thv secret get MY_API_KEY              # Retrieve
thv secret delete MY_API_KEY           # Remove
```

Using secrets with servers:
```bash
thv run github --secret GITHUB_TOKEN,target=GITHUB_PERSONAL_ACCESS_TOKEN
thv run server --secret KEY1,target=ENV1 --secret KEY2,target=ENV2
```

## Client Configuration

```bash
thv client status              # Check all supported clients
thv client setup               # Interactive setup
thv client register claude-code --group development  # Register with group
thv client list-registered     # List registered
thv client remove              # Remove client
```

## Permissions and Network Isolation

**Permission profiles** control what a container can access (filesystem, network):

```bash
thv run myserver --permission-profile network          # Network access only
thv run myserver --permission-profile none             # No extra permissions
thv run myserver --permission-profile ./custom.json    # Custom profile (JSON)
```

Registry servers include a default profile. Without registry info, default is `network`.

**Network isolation** restricts outbound traffic to an allowlist via an egress proxy:

```bash
thv run myserver --isolate-network    # Block all outbound except allowlisted hosts
```

**Volume mounts** for filesystem access:

```bash
thv run filesystem -v /home/user/projects:/workspace:ro    # Read-only mount
```

**.thvignore** hides sensitive files from volume mounts using gitignore-style patterns. Place `.thvignore` in mounted directories or globally at `~/.config/toolhive/thvignore`. Disable global patterns with `--ignore-globally=false`.

For detailed examples, see [EXAMPLES.md](references/EXAMPLES.md#permissions-and-network-isolation).

## Building, Export, and Tool Overrides

```bash
thv build uvx://mcp-server-git                                      # Build container
thv build --tag my-registry/server:v1.0 npx://package               # Custom tag
thv build --dry-run --output Dockerfile.mcp uvx://mcp-server-git    # Dockerfile only

thv export my-server ./config.json              # Export JSON
thv export my-server ./server.yaml --format k8s # Export Kubernetes YAML
thv run --from-config ./config.json             # Import config
```

For tool overrides, see [EXAMPLES.md](references/EXAMPLES.md#tool-filtering-and-overrides).

## Skills Management

Requires `thv serve` to be running. Skills have two scopes: `user` (global, default) and `project` (local to a project root).

```bash
thv skill install my-skill                              # Install from registry
thv skill install ghcr.io/org/skill:v1.0                # Install by OCI reference
thv skill install my-skill --clients claude-code          # Target specific client(s)
thv skill install my-skill --scope project --project-root .  # Project-scoped
thv skill install my-skill --group development           # Add to group
thv skill install my-skill --force                       # Overwrite existing

thv skill list                                           # List all installed
thv skill ls --scope user --format json                  # Filter and format
thv skill ls --client claude-code --group dev            # Filter by client/group

thv skill info my-skill                                  # Show details
thv skill info my-skill --format json                    # JSON output

thv skill uninstall my-skill                             # Remove skill
thv skill uninstall my-skill --scope project --project-root .

thv skill validate ./my-skill-dir                        # Check skill definition
thv skill build ./my-skill-dir                           # Build OCI artifact
thv skill build ./my-skill-dir --tag ghcr.io/org/skill:v1.0
thv skill push ghcr.io/org/skill:v1.0                   # Push to registry
```

For all flags and detailed examples, see [COMMANDS.md](references/COMMANDS.md#skill-commands) and [EXAMPLES.md](references/EXAMPLES.md#skills-management-examples).

## AI Plugin Management

Use `thv ai-plugin` for manifest-based bundles for AI clients, not ToolHive itself. Plugins may contain commands, agents, skills, hooks, and declared MCP/LSP entries; they are distinct from standalone ToolHive skills and MCP workloads. Plugin-declared MCP/LSP entries are not ToolHive-managed workloads.

These commands use the ToolHive API; start `thv serve` first. The supported plugin clients are `claude-code` and `codex`; target either, or `all` available clients, with `--clients`. Install an exact registry name, OCI reference, or `git://host/org/repo[@ref][#subdir]` source, then use `list`/`info` to inspect what ToolHive installed. Verification and lock-file trust are project-only: user-scoped installs do not use lock-file verification.

```bash
thv ai-plugin install example-plugin --clients claude-code
thv ai-plugin install ghcr.io/example-org/example-plugin:v1.0 --scope project --project-root .
thv ai-plugin install git://github.com/example-org/example-plugin@v1.0#plugin --scope project --project-root .
thv ai-plugin list --scope project --project-root .
thv ai-plugin sync --check --project-root .
thv ai-plugin upgrade --preview --project-root .
```

ToolHive extracts, registers, and enables Claude Code plugins. For Codex, ToolHive only materializes the ToolHive marketplace; complete activation yourself:

```bash
codex plugin install example-plugin@toolhive
```

ToolHive does not edit Codex `config.toml` or invoke Codex. Installed-component metadata only describes the extracted/materialized tree; it does not prove client activation. A client can leave unsupported components in that tree without loading them.

For command details, trust boundaries, and publishing, see [COMMANDS.md](references/COMMANDS.md#ai-plugin-commands) and [EXAMPLES.md](references/EXAMPLES.md#ai-plugin-management-examples).

## Debugging

```bash
thv inspector filesystem                        # MCP Inspector UI
thv mcp list tools --server filesystem
thv mcp list resources --server filesystem
thv mcp list prompts --server filesystem
thv mcp call read_file --server filesystem --args '{"path":"/etc/hosts"}'  # Invoke a tool
thv runtime check                               # Verify container runtime
```

For tool invocation patterns (file args, stdin, JSON output, error handling), see [EXAMPLES.md](references/EXAMPLES.md#invoke-a-tool).

## Guardrails

- NEVER use `docker rm` or `podman rm` on ToolHive-managed containers -- always use `thv rm` for proper cleanup.
- NEVER pass secrets as `-e SECRET=value` -- use `--secret` with managed secrets instead.
- Confirm destructive operations (`thv rm --all`, `thv stop --all`, `thv group rm --with-workloads`) with the user before running.
- Skill commands require `thv serve` to be running. If a skill command fails with a connection error, suggest starting `thv serve` first.
- AI plugin commands also require `thv serve`. Treat plugin content as AI-followed instructions: inspect the source and use project-scoped installs only in the intended project.
- For an externally key-pair-signed OCI plugin, give `--public-key` only on its first project install; ToolHive pins the key in `toolhive.lock.yaml` for sync and upgrade. Do not replace that pin without explicit review.
- Never add `--allow-unsigned`, `--allow-signer-change`, `--allow-ref-change`, `--force`, `--prune`, or `--yes` silently. Unsigned consent is explicit and does not bypass invalid signature verification. Confirm before `sync`/`upgrade` applies content or `builds remove` deletes a local artifact.
- If the user asks about Kubernetes deployment, this skill does not cover the operator -- direct them accordingly.

## Error Handling

| Symptom | Cause | Recovery |
|---------|-------|----------|
| Container can't reach localhost | Bridge network isolation | Use `host.docker.internal` (Docker Desktop), `host.containers.internal` (Podman), `172.17.0.1` (Linux) |
| Port already in use | Another server on same port | Use `--proxy-port <different-port>` |
| Permission denied on volume | Mount path or profile issue | Check volume mount paths and permission profiles (`--permission-profile`) |
| Container runtime not found | No runtime or socket issue | Run `thv runtime check`; override socket with `TOOLHIVE_PODMAN_SOCKET`, `TOOLHIVE_COLIMA_SOCKET`, or `TOOLHIVE_DOCKER_SOCKET` |
| Secret operation fails | Provider not configured | Run `thv secret setup` first |
| Image pull fails | Network or auth issue | Check network connectivity; for private registries, ensure credentials are configured |
| Remote auth token expired | OAuth token lifetime exceeded | Restart the server (`thv restart`) to trigger fresh authentication |
| Sensitive files exposed in mount | No `.thvignore` configured | Add `.thvignore` in mounted directory or globally at `~/.config/toolhive/thvignore` |
| Skill command fails with connection error | `thv serve` not running | Start `thv serve` before using skill commands |
| Skill validation fails | Invalid SKILL.md or directory structure | Run `thv skill validate ./path` and fix reported errors |
| AI plugin API unreachable | `thv serve` is not running | Start `thv serve`; increase `TOOLHIVE_API_TIMEOUT` only for a server that is still working |
| Plugin sync check fails | Missing or drifted project plugin | Review the result, then run `thv ai-plugin sync` and confirm; do not add trust-bypass flags automatically |
| Plugin activation unclear | Client did not load a declared component | Inspect client support and client output; materialized component metadata is not activation proof |
| Plugin signature rejected | Untrusted, invalid, or changed signer | Inspect the artifact and lock pin; `--allow-unsigned` cannot bypass invalid verification |

## Global Options

- `--debug`: Verbose output
- `-h, --help`: Command help

Container runtime auto-detected: Podman -> Colima -> Docker.

## See Also

- [COMMANDS.md](references/COMMANDS.md) - Complete command reference with all flags
- [EXAMPLES.md](references/EXAMPLES.md) - Detailed usage examples and workflows
