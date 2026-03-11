# ToolHive CLI Usage Examples

## Basic Workflows

### Quick Start with Registry Server

```bash
# Run filesystem server
thv run filesystem

# Check it's running
thv list

# Get detailed status
thv status filesystem

# View capabilities
thv mcp list tools --server filesystem

# View logs
thv logs filesystem

# Stop when done
thv stop filesystem

# Clean up
thv rm filesystem
```

### Run Server with Custom Configuration

```bash
# Named server with environment variables
thv run github \
  --name my-github \
  -e GITHUB_PERSONAL_ACCESS_TOKEN=ghp_xxxx \
  -- --toolsets repos

# Server with volume mount
thv run filesystem \
  --name project-fs \
  -v /home/user/projects:/workspace:ro

# Server with multiple labels
thv run fetch \
  --name docs-fetch \
  -l env=production \
  -l team=backend \
  -l version=1.0
```

## Protocol Scheme Examples

### Python (uvx)

```bash
# From PyPI
thv run uvx://mcp-server-git

# With version
thv run uvx://mcp-server-git@1.0.0

# With arguments
thv run uvx://mcp-server-sqlite -- --db-path /data/mydb.sqlite
```

### Node.js (npx)

```bash
# From npm
thv run npx://@modelcontextprotocol/server-everything

# Scoped package
thv run npx://@modelcontextprotocol/server-filesystem

# With arguments
thv run npx://@modelcontextprotocol/server-filesystem -- /allowed/path
```

### Go

```bash
# From module
thv run go://github.com/example/mcp-server@latest

# Local project
thv run go://./my-mcp-server

# Parent directory
thv run go://../shared/mcp-server
```

## Secrets Management Examples

### Initial Setup

```bash
# Configure encrypted provider (recommended)
thv secret setup
# Select: encrypted
# Enter password when prompted

# Or use 1Password
export OP_SERVICE_ACCOUNT_TOKEN=your-token
thv secret setup
# Select: 1password
```

### Store and Use Secrets

```bash
# Store interactively
thv secret set GITHUB_TOKEN
# Enter token when prompted

# Store via pipe
echo "ghp_xxxxxxxxxxxx" | thv secret set GITHUB_TOKEN

# List stored secrets
thv secret list

# Use secret when running server
thv run github --secret GITHUB_TOKEN,target=GITHUB_PERSONAL_ACCESS_TOKEN

# Multiple secrets
thv run myserver \
  --secret API_KEY,target=API_KEY \
  --secret DB_PASSWORD,target=DATABASE_PASSWORD
```

## Group Management Examples

### Environment-Based Groups

```bash
# Create environment groups
thv group create development
thv group create staging
thv group create production

# Run servers in specific groups
thv run filesystem --name dev-fs --group development
thv run filesystem --name prod-fs --group production

# List servers by group
thv list --group development
thv list --group production

# Stop all servers in a group
thv stop --group development
```

### Client Group Restrictions

```bash
# Register client with group restriction
thv client register claude-code --group development

# Now this client only sees servers in development group
```

### Restart Servers

```bash
# Restart a single server
thv start filesystem
thv restart filesystem          # Same thing (alias)

# Restart all servers in a group
thv start --group development

# Restart all servers
thv start --all
```

### Deploy Registry Groups

```bash
# Registry defines groups like "kubernetes", "devops", etc.
# Run all servers from a registry group
thv group run kubernetes
```

## Remote Server Examples

### Basic Remote Server

```bash
# Simple remote server
thv run https://api.example.com/mcp --name remote-api
```

### Remote with OIDC Authentication

```bash
# Full OIDC setup
thv run https://api.example.com/mcp --name secure-api \
  --remote-auth-issuer https://auth.example.com \
  --remote-auth-client-id my-client-id \
  --remote-auth-client-secret-file /path/to/secret \
  --remote-auth-scopes "openid profile"

# Dynamic client registration (no credentials needed)
thv run https://api.example.com/mcp --name auto-api \
  --remote-auth \
  --remote-auth-issuer https://auth.example.com
```

### Remote with OAuth2 (non-OIDC)

```bash
thv run https://api.example.com/mcp --name oauth-api \
  --remote-auth-authorize-url https://auth.example.com/oauth/authorize \
  --remote-auth-token-url https://auth.example.com/oauth/token \
  --remote-auth-client-id my-client-id \
  --remote-auth-client-secret my-secret
```

### Remote with Bearer Token

```bash
# From file (recommended)
thv run https://api.example.com/mcp --name token-api \
  --remote-auth-bearer-token-file /path/to/token.txt

# Direct (less secure)
thv run https://api.example.com/mcp --name token-api \
  --remote-auth-bearer-token "your-token-here"
```

## Building Containers

### Pre-build for Kubernetes

```bash
# Build with custom tag
thv build --tag my-registry.io/mcp/filesystem:v1.0.0 \
  npx://@modelcontextprotocol/server-filesystem

# Push to registry (standard docker)
docker push my-registry.io/mcp/filesystem:v1.0.0
```

### Build with Embedded Arguments

```bash
# Arguments baked into ENTRYPOINT
thv build --tag launchdarkly:latest \
  npx://@launchdarkly/mcp-server -- start
```

### Generate Dockerfile Only

```bash
# Output Dockerfile for inspection/modification
thv build --dry-run --output Dockerfile.mcp \
  uvx://mcp-server-git

# Review and customize
cat Dockerfile.mcp

# Build manually
docker build -f Dockerfile.mcp -t my-mcp:custom .
```

## Export and Import Examples

### Backup Configuration

```bash
# Export to JSON
thv export my-server ./backup/my-server.json

# Export to Kubernetes YAML
thv export my-server ./k8s/my-server.yaml --format k8s
```

### Migrate Configuration

```bash
# Export from one machine
thv export production-server ./config.json

# Transfer file to new machine, then import
thv run --from-config ./config.json
```

### Share Configuration

```bash
# Export team's standard setup
thv export team-toolkit ./team-config.json

# Team member imports
thv run --from-config ./team-config.json --name my-toolkit
```

## Tool Filtering and Overrides

### Filter Available Tools

```bash
# Only expose specific tools
thv run github --tools list_issues,get_issue,create_issue

# Multiple tools comma-separated
thv run fetch --tools fetch,fetch_html
```

### Override Tool Names/Descriptions

Create `overrides.json`:
```json
{
  "toolsOverride": {
    "fetch": {
      "name": "docs-fetch",
      "description": "Fetches content from documentation websites only"
    },
    "list_issues": {
      "name": "get-github-issues",
      "description": "Lists issues from the main repository"
    }
  }
}
```

Apply:
```bash
thv run fetch --tools-override overrides.json
```

## Debugging Examples

### Inspect Server Capabilities

```bash
# List tools
thv mcp list tools --server filesystem

# List resources
thv mcp list resources --server filesystem

# List prompts
thv mcp list prompts --server filesystem

# JSON output for parsing
thv mcp list tools --server filesystem --format json
```

### Launch Inspector UI

```bash
# Default ports
thv inspector filesystem
# UI at http://localhost:6274

# Custom ports
thv inspector filesystem --ui-port 7000 --mcp-proxy-port 7001
```

### View Logs

```bash
# Container logs
thv logs filesystem

# Follow in real-time
thv logs filesystem --follow

# Proxy logs (for debugging HTTP/auth issues)
thv logs filesystem --proxy
```

### Verify Runtime

```bash
# Check container runtime is accessible
thv runtime check
```

## Client Registration Examples

### Check Available Clients

```bash
# See all supported clients and their status
thv client status
```

### Interactive Setup

```bash
# Guided setup for detected clients
thv client setup
```

### Register Specific Clients

```bash
# Register Claude Code
thv client register claude-code

# Register VS Code
thv client register vscode

# Register with group restriction
thv client register cursor --group development
```

### Manage Registrations

```bash
# List registered clients
thv client list-registered

# Remove a client
thv client remove cursor
```

## Permissions and Network Isolation

### Permission Profiles

```bash
# No extra permissions (most restrictive)
thv run myserver --permission-profile none

# Network access only (no filesystem)
thv run myserver --permission-profile network

# Custom profile from JSON file
thv run myserver --permission-profile ./my-permissions.json
```

### Network Isolation (Egress Proxy)

```bash
# Isolate network — only allowlisted hosts can be reached
thv run myserver --isolate-network

# Combined with custom permissions
thv run myserver --isolate-network --permission-profile ./restricted.json
```

### .thvignore — Hide Sensitive Files

Place `.thvignore` in mounted directories to hide matching files from the container:

```bash
# Create .thvignore in project root
cat > /home/user/projects/.thvignore << 'EOF'
.env
.git/
*.pem
secrets/
EOF

# Mount the directory — .thvignore patterns are applied automatically
thv run filesystem -v /home/user/projects:/workspace:ro
```

Global patterns at `~/.config/toolhive/thvignore` apply to all mounts. Disable with `--ignore-globally=false`.

## Network Configuration Examples

### Host Networking

```bash
# Use host network (container shares host network namespace)
thv run myserver --network host
```

### Custom Docker Network

```bash
# Create network first
docker network create mcp-network

# Run server in that network
thv run myserver --network mcp-network
```

### Access Host Services from Container

```bash
# For services running on host machine
# Docker Desktop (Mac/Windows):
thv run myserver -e SERVICE_URL=http://host.docker.internal:8080

# Podman:
thv run myserver -e SERVICE_URL=http://host.containers.internal:8080

# Linux (bridge network):
thv run myserver -e SERVICE_URL=http://172.17.0.1:8080
```

