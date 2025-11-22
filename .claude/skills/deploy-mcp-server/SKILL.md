---
name: Deploy MCP Server to Kubernetes
description: Deploy an MCPServer to Kubernetes using configuration from a registry (Kubernetes or local CLI) (project)
---

# Deploy MCP Server to Kubernetes

This skill helps you deploy an MCPServer custom resource to a Kubernetes cluster using server configuration retrieved from either a Kubernetes MCPRegistry or the local CLI registry.

**Note:** For managing and querying MCPRegistry resources (listing registries, viewing available servers), use the **Manage MCP Registries** skill instead.

## Instructions

**IMPORTANT - Todo List Preparation:**

Before executing any deployment steps, you MUST use the TodoWrite tool to create a comprehensive task list. This is critical for tracking the multi-step deployment process and ensuring no steps are skipped.

Create a task list that includes ALL of these steps:
1. Gather required information (namespace, registry source)
2. Retrieve server configuration from registry
3. Process server configuration (transport, permissions, resources)
4. Handle ALL environment variables (MANDATORY - cannot be skipped)
5. Confirm resource settings
6. Generate and review YAML
7. Create secrets if needed
8. Deploy to Kubernetes
9. Verify deployment
10. Clean up (port-forwards, etc.)

Mark each task as "in_progress" when you start it, and "completed" immediately when finished. This provides clear visibility to the user about deployment progress.

Follow these steps to deploy an MCP server:

**⚠️  IMPORTANT WORKFLOW REQUIREMENT ⚠️**

When deploying an MCP server, you MUST complete ALL of the following steps in order:
1. Gather required information (namespace, registry source)
2. Retrieve server configuration from the registry
3. Process server configuration (transport, permissions, resources)
4. **Handle ALL environment variables** - THIS STEP IS MANDATORY and CANNOT be skipped
5. Confirm resource settings
6. Generate and review YAML
7. Deploy to Kubernetes
8. Verify deployment

**The environment variable configuration step (Step 4) is REQUIRED for every deployment**, even if the user tries to skip it. You must explicitly walk through each environment variable with the user.

### 1. Gather Required Information

Ask the user for:
- **Server name**: The name of the MCP server to deploy (e.g., "fetch", "github")
  - If the user is unsure, suggest using the **Manage MCP Registries** skill to list available servers
- **Namespace**: The Kubernetes namespace to deploy to (default: "toolhive-system")
- **Registry source**: Where to get the server configuration from:
  - `kubernetes`: Use a Kubernetes MCPRegistry resource
  - `local`: Use the local CLI registry (`thv registry info`)

If using Kubernetes registry:
- **Registry namespace**: The namespace where the MCPRegistry is located (if different from deployment namespace)
- **Registry name**: The name of the MCPRegistry resource (if user knows it)
  - If unsure, use the **Manage MCP Registries** skill to list available registries

### 2. Retrieve Server Configuration

#### Option A: Local CLI Registry

Use `thv registry info <server-name> --format json` to get the server configuration.

Example:
```bash
thv registry info fetch --format json
```

This returns JSON with fields like:
- `image`: Container image to use
- `transport`: Transport protocol (stdio, streamable-http, sse)
- `permissions`: Network and file permissions
- `env_vars`: Required and optional environment variables
- `port`: Default port (if applicable)

#### Option B: Kubernetes Registry

1. Get the registry name and namespace from the user, or use the **Manage MCP Registries** skill to help them select one

2. Extract the API endpoint from the MCPRegistry status:
   ```bash
   kubectl get mcpregistry <registry-name> -n <namespace> -o jsonpath='{.status.apiStatus.endpoint}'
   ```

   The endpoint will be an in-cluster Service URL like: `http://thv-git-api.toolhive-system:8080`

3. **Set up port-forward to access the registry API**:

   Extract the service name and port from the endpoint, then create port-forward:
   ```bash
   kubectl port-forward -n <namespace> svc/<service-name> <local-port>:<service-port>
   ```

   Example:
   ```bash
   kubectl port-forward -n toolhive-system svc/thv-git-api 8080:8080 &
   ```

4. **Query specific server configuration**:
   ```bash
   curl -s http://localhost:<local-port>/v0/servers/<server-name>
   ```

   This returns the complete server configuration needed for deployment.

5. Clean up the port-forward when deployment is complete:
   ```bash
   # Find and kill the port-forward process
   ps aux | grep "kubectl port-forward"
   kill <pid>
   ```

### 3. Process Server Configuration

Extract the necessary fields from the registry data:
- `image`: Required
- `transport`: Default to "stdio" if not specified
- `port`: Only include if transport is "streamable-http" or "sse"
- `targetPort`: Only if different from port
- `args`: Command-line arguments to pass to the container (if specified in registry)
- `env`: Environment variables (ask user for values of required vars)
- `resources`: Resource limits/requests (provide sensible defaults: 100m CPU, 128Mi memory)
- `permissions`: Convert to `permissionProfile` if network permissions are specified

**IMPORTANT:** If the server configuration includes an `args` field, you MUST include it in the MCPServer spec. Many servers require specific command-line arguments to start properly (e.g., `["http"]` for HTTP mode, `["stdio"]` for stdio mode). Omitting required args will cause the container to fail or show help text instead of starting.

#### 3.1 Handle stdio Transport - ProxyMode Selection

If the transport is "stdio", ALWAYS ask the user to select the proxy mode:

```
The server uses 'stdio' transport, which requires a proxy for HTTP access.

Select proxy mode:
A) sse (Server-Sent Events) - Default, recommended for most use cases
B) streamable-http (Streamable HTTP) - Alternative streaming protocol

Your choice (A or B):
```

Valid options according to MCPServer CRD:
- `sse` (default)
- `streamable-http`

Set the `proxyMode` field in the spec accordingly. If transport is not "stdio", do NOT include proxyMode in the spec.

### 4. Handle Environment Variables

**⚠️  CRITICAL - THIS STEP CANNOT BE SKIPPED ⚠️**

MANDATORY RULES:
1. **EVERY** environment variable from the registry configuration MUST be addressed
2. For EACH variable, the user MUST:
   - Provide a value (custom or accept default), OR
   - Explicitly skip it by typing 'skip' (ONLY if optional AND not required)
3. **NEVER** skip this step or assume default values without user confirmation
4. **NEVER** proceed with deployment if required variables are missing values
5. **ALWAYS** confirm all environment variable values with the user before generating YAML
6. If there are NO environment variables in the configuration, explicitly state this to the user

**Important:** When prompting for optional values, always instruct users to type 'skip' if they want to skip the variable. Do NOT use "press Enter to skip" as empty responses don't work in chat interfaces.

**Why this step cannot be skipped:**
- MCP servers often require specific configuration to function correctly
- Using wrong or missing values can cause deployment failures or security issues
- Users must understand what each variable does and consciously choose values

#### 4.1 Secret Management Strategy

First, check if there are any environment variables marked as `secret: true` in the registry configuration.

If secret variables exist, ask the user:
```
This server has <N> secret/sensitive environment variable(s): <list names>

Would you like to:
A) Create a new Kubernetes Secret to store these values
B) Use an existing Kubernetes Secret (you'll provide the secret name and keys)
C) Store values directly in the manifest (⚠️  NOT RECOMMENDED - secrets will be visible in plain text)

Your choice (A/B/C):
```

Based on the user's choice:

**Option A - Create New Secret:**
1. Generate secret name: `<mcpserver-instance-name>-secrets`
2. For each secret variable:
   - Prompt: "**<VAR_NAME>** (<description> - optional/required):\nEnter value (or type 'skip' to skip if optional):"
   - If user provides a value: Collect it securely
   - If user types 'skip' and variable is optional: Skip this secret variable
   - If user types 'skip' and variable is required: Inform them it's required and re-prompt
   - Map to secret key (lowercase env var name)
3. Create the Kubernetes Secret BEFORE deploying MCPServer:
   ```bash
   kubectl create secret generic <mcpserver-instance-name>-secrets \
     -n <namespace> \
     --from-literal=<key1>=<value1> \
     --from-literal=<key2>=<value2>
   ```
4. Verify secret creation: `kubectl get secret <secret-name> -n <namespace>`
5. Reference in MCPServer spec using `secrets` field

**Option B - Use Existing Secret:**
1. Ask: "What is the name of the existing Secret?"
2. Verify the secret exists: `kubectl get secret <secret-name> -n <namespace>`
3. For each secret variable:
   - Ask: "Which key in Secret '<secret-name>' contains <VAR_NAME>?"
   - Validate the key exists: `kubectl get secret <secret-name> -n <namespace> -o jsonpath='{.data.<key>}'`
4. Use `secrets` field in MCPServer spec with user-provided mappings

**Option C - Store Inline (Discouraged):**
1. ⚠️  Warn the user: "WARNING: Secret values will be stored in plain text in the MCPServer manifest. This is NOT recommended for production use."
2. Ask for confirmation: "Are you sure you want to proceed? (yes/no)"
3. If confirmed, collect values and store in `env` field
4. If not confirmed, return to secret strategy selection

#### 4.2 Present All Environment Variables

CRITICAL: ALL environment variables from the registry MUST be addressed. Follow this workflow:

**Step 1 - Categorize Variables:**
Separate environment variables into:
- Required non-secret variables
- Required secret variables
- Optional non-secret variables
- Optional secret variables

**Step 2 - Present Complete List:**
Show ALL variables to the user in a clear, organized format:

```
The <server-name> server has the following environment variables:

REQUIRED (plain text):
1. <VAR_NAME>
   Description: <description>
   Default: <default-value> [if any]

2. <VAR_NAME>
   Description: <description>
   Default: <default-value> [if any]

REQUIRED (secrets - will be handled separately):
3. <VAR_NAME>
   Description: <description>

OPTIONAL (plain text):
4. <VAR_NAME>
   Description: <description>
   Default: <default-value> [if any]

OPTIONAL (secrets - will be handled separately):
5. <VAR_NAME>
   Description: <description>
   Default: <default-value> [if any]
```

**Step 3 - Collect Non-Secret Variable Values:**
For each non-secret variable (required and optional):

If variable has a default value:
```
<VAR_NAME>: "<default-value>"
Use default? (yes/no, or enter custom value):
```
- If "yes": Use default value
- If "no": Prompt for custom value
- If custom value provided: Use that value
- If empty and optional: Mark as skipped
- If empty and required: Re-prompt (cannot be empty)

If variable has NO default value:
```
<VAR_NAME>:
Enter value (or type 'skip' to skip if optional) [required/optional]:
```
- If value provided: Use that value
- If user types 'skip' and variable is optional: Skip the variable
- If user types 'skip' and variable is required: Inform them it's required and re-prompt
- If empty response and required: Re-prompt until value provided

**Step 4 - Collect Secret Variable Values:**
Handle secret variables according to the strategy chosen in 4.1:
- If option A or C: Prompt for each secret value directly
- If option B: Verify the values exist in the referenced secret

**Step 5 - Confirm All Values:**
Before generating YAML, show a complete summary:
```
Environment variable summary:

Plain text variables:
  <VAR_NAME>: <value>
  <VAR_NAME>: <value>

Secret variables (stored in Secret '<secret-name>'):
  <VAR_NAME>: ****** (from secret key: <key>)
  <VAR_NAME>: ****** (from secret key: <key>)

Skipped optional variables:
  <VAR_NAME>

Proceed with deployment? (yes/no):
```

If user says "no", allow them to modify any values.

#### 4.3 Default Value Handling

When the registry provides a default value for a variable:
1. ALWAYS show it to the user
2. ALWAYS ask if they want to use it or provide a custom value
3. NEVER silently use defaults without user awareness
4. For optional variables with defaults, offer to skip (which means NOT setting the variable at all)

Example for variable with default:
```
CB_MCP_READ_ONLY_QUERY_MODE (optional):
  Description: Prevent data modification queries
  Default: "true"

Your choice:
  A) Use default value ("true")
  B) Enter custom value
  C) Skip this variable (don't set it)

Choice (A/B/C):
```

### 5. Confirm Resource Settings

ALWAYS ask the user to confirm or customize resource settings before generating the YAML.

Default resource settings:
```
Resources:
  Limits:
    CPU: 100m (0.1 CPU cores)
    Memory: 128Mi
  Requests:
    CPU: 50m (0.05 CPU cores)
    Memory: 64Mi
```

Prompt the user:
```
The following default resource settings will be applied:

Limits:
  - CPU: 100m
  - Memory: 128Mi

Requests:
  - CPU: 50m
  - Memory: 64Mi

Would you like to:
A) Use these default settings
B) Customize resource limits/requests

Your choice (A or B):
```

If user chooses B (customize):
- Ask for custom CPU limit (e.g., "200m", "0.5", "1")
- Ask for custom Memory limit (e.g., "256Mi", "512Mi", "1Gi")
- Ask for custom CPU request (should be ≤ limit)
- Ask for custom Memory request (should be ≤ limit)

Validate that requests are not greater than limits.

### 6. Generate MCPServer YAML

Create an MCPServer resource with the following structure:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: <server-name>
  namespace: <namespace>
spec:
  image: <image>
  transport: <transport>
  proxyMode: <proxy-mode>  # Only for stdio transport (sse or streamable-http)
  port: <port>  # Only for streamable-http/sse transport, or stdio with proxy
  targetPort: <targetPort>  # Only if different from port
  args:  # Only if specified in registry configuration
    - <arg1>
    - <arg2>
  resources:
    limits:
      cpu: "<cpu-limit>"
      memory: "<memory-limit>"
    requests:
      cpu: "<cpu-request>"
      memory: "<memory-request>"
  env:  # For non-sensitive vars
    - name: <ENV_VAR_NAME>
      value: <value>
  secrets:  # For sensitive vars stored in secrets
    - name: <secret-name>
      key: <secret-key>
      targetEnvName: <ENV_VAR_NAME>
  permissionProfile:  # Only if network permissions needed
    type: builtin
    name: network
```

#### 6.1 Create Kubernetes Secret (if option A was chosen in step 4.1)

If user chose to create a new secret, create it before applying the MCPServer:

```bash
kubectl create secret generic <server-name>-secrets \
  -n <namespace> \
  --from-literal=<KEY1>=<VALUE1> \
  --from-literal=<KEY2>=<VALUE2>
```

Verify the secret was created:
```bash
kubectl get secret <server-name>-secrets -n <namespace>
```

### 7. Deploy to Kubernetes

1. Show the user the generated YAML for review
2. Ask for confirmation to proceed
3. Apply the MCPServer resource:
   ```bash
   kubectl apply -f - <<EOF
   <generated-yaml>
   EOF
   ```
4. Wait for the associated pod to become ready:
   ```bash
   kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=<server-name> -n <namespace> --timeout=60s
   ```
5. Show the deployment status:
   ```bash
   kubectl get mcpserver <server-name> -n <namespace>
   kubectl get pods -n <namespace> -l app.kubernetes.io/name=<server-name>
   ```

### 8. Verify Deployment

Check the MCPServer and Pod status:
```bash
# Check MCPServer resource
kubectl get mcpserver <server-name> -n <namespace> -o yaml

# Check Pod status (MCPServers do not expose a Ready condition, check the Pod instead)
kubectl get pods -n <namespace> -l app.kubernetes.io/name=<server-name>
```

Look for in the MCPServer:
- `status.phase`: Should be "Running"
- `status.url`: The URL where the server is accessible

Look for in the Pod:
- `status.phase`: Should be "Running"
- `status.conditions`: The Ready condition should be "True"

If there are issues:
1. Check pod status: `kubectl get pods -n <namespace> -l app.kubernetes.io/name=<server-name>`
2. Check pod logs: `kubectl logs -n <namespace> -l app.kubernetes.io/name=<server-name>`
3. Describe the pod: `kubectl describe pod -n <namespace> -l app.kubernetes.io/name=<server-name>`
4. Describe the MCPServer: `kubectl describe mcpserver <server-name> -n <namespace>`

## Error Handling

Handle these common scenarios:

1. **Server not found in registry**: Suggest using the **Manage MCP Registries** skill to explore available servers, or use `thv search <query>` for local registry
2. **Registry not accessible**: Use the **Manage MCP Registries** skill to check registry status and troubleshoot
3. **Missing required environment variables**: List all required vars and prompt user
4. **Deployment fails**: Show error messages and suggest remediation
5. **Permission issues**: Ensure user has proper RBAC permissions for the namespace

## Best Practices

- Use the **Manage MCP Registries** skill to explore available servers and registries before deploying
- Always validate that the server exists in the registry before attempting deployment
- Use Kubernetes Secrets for sensitive environment variables
- Apply sensible resource limits to prevent resource exhaustion
- Use the `network` permission profile when the server needs external network access
- Verify the deployment is healthy before completing the skill
- Clean up port-forwards after retrieving server configuration

## Example Usage

User: "Deploy the fetch server to Kubernetes"