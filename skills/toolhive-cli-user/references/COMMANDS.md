# ToolHive CLI Command Reference

## Root Command

```
thv [flags]
```

**Global Flags:**
- `--debug`: Enable debug mode
- `-h, --help`: Help for any command

## Server Management

### thv run

Run an MCP server.

```
thv run [flags] SERVER_OR_IMAGE_OR_PROTOCOL [-- ARGS...]
```

**Input Methods:**
1. Registry name: `thv run filesystem`
2. Container image: `thv run ghcr.io/example/mcp-server:latest`
3. Protocol scheme: `thv run uvx://package`, `npx://package`, `go://package`
4. Config file: `thv run --from-config <path>`
5. Remote URL: `thv run https://api.example.com --name my-server`

**Key Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--name` | Server name | Auto-generated |
| `--group` | Group assignment | `default` |
| `-e, --env` | Environment variables (KEY=VALUE) | |
| `--env-file` | Load env vars from file | |
| `--secret` | Secret (NAME,target=TARGET) | |
| `-v, --volume` | Volume mount (host:container[:ro]) | |
| `-l, --label` | Labels (key=value) | |
| `--tools` | Filter tools (comma-separated) | |
| `--tools-override` | Path to tool override JSON | |
| `-f, --foreground` | Run in foreground | false |
| `--proxy-port` | Host proxy port | Auto |
| `--host` | Proxy listen host | 127.0.0.1 |
| `--transport` | Transport mode (sse, streamable-http, stdio) | |
| `--network` | Docker network mode | bridge |
| `--isolate-network` | Isolate container network via egress proxy | false |
| `--from-config` | Load from exported config | |
| `--permission-profile` | Permission profile (none, network, or JSON path) | Registry default or `network` |
| `--ca-cert` | Custom CA certificate for the container | |
| `--ignore-globally` | Load global `.thvignore` patterns | true |

**Remote Server Authentication Flags:**
| Flag | Description |
|------|-------------|
| `--remote-auth` | Enable OAuth to remote server |
| `--remote-auth-issuer` | Remote OIDC issuer |
| `--remote-auth-client-id` | Remote OAuth client ID |
| `--remote-auth-client-secret` | Remote OAuth secret |
| `--remote-auth-client-secret-file` | Path to secret file |
| `--remote-auth-bearer-token-file` | Bearer token file |
| `--remote-auth-authorize-url` | OAuth authorize URL (non-OIDC) |
| `--remote-auth-token-url` | OAuth token URL (non-OIDC) |

### thv list

List running MCP servers.

```
thv list [flags]
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--all` | Include stopped servers | false |
| `--format` | Output format (text, json, mcpservers) | text |
| `--group` | Filter by group | |
| `--label` | Filter by label (key=value) | |

The `mcpservers` format outputs JSON suitable for MCP client configuration files.

### thv status

Show detailed status of a specific MCP server.

```
thv status [flags] WORKLOAD_NAME
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--format` | Output format (text, json) | text |

Shows: name, status, health, package, URL, port, transport, proxy mode, group, created time, uptime.

### thv stop

Stop one or more MCP servers.

```
thv stop [flags] [SERVER_NAME...]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--all` | Stop all servers |
| `--group` | Stop by group |
| `--timeout` | Timeout in seconds |

### thv start

Start (resume) stopped servers. Alias: `thv restart` (backward compatibility).

```
thv start [flags] [SERVER_NAME...]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--all` | Start all stopped servers |
| `--group` | Start by group |
| `-f, --foreground` | Run in foreground |

Mutually exclusive: `--all`, `--group`, and positional server name.

### thv rm

Remove MCP servers.

```
thv rm [flags] [SERVER_NAME...]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--all` | Remove all servers |
| `--group` | Remove by group |

### thv logs

View server logs.

```
thv logs [flags] SERVER_NAME
thv logs prune
```

**Flags:**
| Flag | Description |
|------|-------------|
| `-f, --follow` | Follow log output |
| `-p, --proxy` | Show proxy logs |

## Registry Commands

### thv registry list

List available MCP servers.

```
thv registry list [flags]
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--format` | Output format (text, json) | text |
| `--refresh` | Force refresh cache | false |

### thv registry info

Get server details.

```
thv registry info [flags] SERVER_NAME
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--format` | Output format (text, json) | text |

### thv search

Search for MCP servers.

```
thv search [flags] QUERY
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--format` | Output format (text, json) | text |

## Group Commands

### thv group create

Create a server group.

```
thv group create GROUP_NAME
```

### thv group list

List all groups.

```
thv group list
```

### thv group run

Run all servers from a registry group.

```
thv group run GROUP_NAME
```

### thv group rm

Remove a group.

```
thv group rm [flags] GROUP_NAME
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--with-workloads` | Also remove servers in group |

## Secret Commands

### thv secret setup

Configure secrets provider (interactive).

```
thv secret setup
```

### thv secret set

Store a secret.

```
thv secret set SECRET_NAME
```

### thv secret get

Retrieve a secret.

```
thv secret get SECRET_NAME
```

### thv secret list

List all secrets.

```
thv secret list
```

### thv secret delete

Delete a secret.

```
thv secret delete SECRET_NAME
```

### thv secret provider

Set provider directly.

```
thv secret provider PROVIDER_NAME
```

### thv secret reset-keyring

Reset keyring password.

```
thv secret reset-keyring
```

## Client Commands

### thv client status

Show status of supported clients.

```
thv client status
```

### thv client setup

Interactive client setup.

```
thv client setup
```

### thv client register

Register a specific client.

```
thv client register [flags] [CLIENT_NAME]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--group` | Restrict client to group |

### thv client list-registered

List registered clients.

```
thv client list-registered
```

### thv client remove

Remove a client.

```
thv client remove [CLIENT_NAME]
```

## Build Commands

### thv build

Build container without running.

```
thv build [flags] PROTOCOL_SCHEME [-- ARGS...]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `-t, --tag` | Custom image tag |
| `-o, --output` | Write Dockerfile to file |
| `--dry-run` | Generate Dockerfile only |
| `--ca-cert` | Custom CA certificate |

## Export Commands

### thv export

Export workload configuration.

```
thv export [flags] WORKLOAD_NAME PATH
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--format` | Output format (json, k8s) | json |

## Configuration Commands

### thv config set-registry

Set custom registry URL.

```
thv config set-registry URL_OR_PATH
```

### thv config get-registry

Get current registry.

```
thv config get-registry
```

### thv config unset-registry

Reset to default registry.

```
thv config unset-registry
```

### thv config set-ca-cert / get-ca-cert / unset-ca-cert

Manage default CA certificate for container builds.

```
thv config set-ca-cert /path/to/corporate-ca.crt
thv config get-ca-cert
thv config unset-ca-cert
```

## Skill Commands

All skill commands require `thv serve` to be running. They communicate via HTTP client with auto-discovery.

### thv skill install

Install a skill by name or OCI reference.

```
thv skill install [flags] SKILL_NAME
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--clients` | Comma-separated target client applications (e.g. claude-code,opencode) | |
| `--scope` | Installation scope (user, project) | user |
| `--force` | Overwrite existing skill directory | false |
| `--project-root` | Project root path (required when scope=project) | |
| `--group` | Group to add the skill to after installation | |

### thv skill uninstall

Remove an installed skill.

```
thv skill uninstall [flags] SKILL_NAME
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--scope` | Scope to uninstall from (user, project) | user |
| `--project-root` | Project root path (required when scope=project) | |

Shell completion available for skill names.

### thv skill list

List installed skills. Alias: `thv skill ls`.

```
thv skill list [flags]
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--scope` | Filter by scope (user, project) | |
| `--client` | Filter by client application | |
| `--format` | Output format (text, json) | text |
| `--group` | Filter by group | |
| `--project-root` | Project root path for project-scoped skills | |

**Text output columns:** NAME, VERSION, SCOPE, STATUS, CLIENTS, REFERENCE

### thv skill info

Show detailed information about a skill.

```
thv skill info [flags] SKILL_NAME
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--scope` | Filter by scope (user, project) | |
| `--format` | Output format (text, json) | text |
| `--project-root` | Project root path for project-scoped skills | |

Shell completion available for skill names.

### thv skill build

Build a skill from a local directory into an OCI artifact.

```
thv skill build [flags] PATH
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `-t, --tag` | OCI tag for the built artifact | |

Prints the OCI reference on success. Shell completion available for directory paths.

### thv skill push

Push a previously built skill artifact to a remote OCI registry.

```
thv skill push REFERENCE
```

No additional flags.

### thv skill validate

Check that a skill definition is valid and well-formed.

```
thv skill validate [flags] PATH
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--format` | Output format (text, json) | text |

**Text output:** Lists errors and warnings line by line. **JSON output:** `ValidationResult` with `Valid`, `Errors`, `Warnings` fields.

Shell completion available for directory paths.

## Utility Commands

### thv inspector

Launch MCP Inspector UI.

```
thv inspector [flags] WORKLOAD_NAME
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `-u, --ui-port` | Inspector UI port | 6274 |
| `-p, --mcp-proxy-port` | Proxy port | 6277 |

### thv mcp list

List MCP server capabilities.

```
thv mcp list tools --server SERVER
thv mcp list resources --server SERVER
thv mcp list prompts --server SERVER
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--server` | Server URL or name | Required |
| `--format` | Output format | text |
| `--timeout` | Connection timeout | |
| `--transport` | Transport (auto, sse, streamable-http) | auto |

### thv runtime check

Check container runtime.

```
thv runtime check
```

### thv version

Show version information.

```
thv version [flags]
```

**Flags:**
| Flag | Description | Default |
|------|-------------|---------|
| `--format` | Output format (text, json) | text |
