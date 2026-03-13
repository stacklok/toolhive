# ToolHive Attack Tree

This attack tree models the potential attack vectors against the ToolHive platform across its different deployment modes and components. It serves as a structured approach to understanding security threats and implementing appropriate countermeasures.

## Root Goal: Compromise ToolHive Platform

### High-Level Attack Vectors

This overview shows the main attack categories. Click through to detailed sections below for specific attack paths.

```mermaid
graph LR
    ROOT[Compromise ToolHive Platform] --> DEPLOY{OR: Attack Vector by Deployment Mode}
    
    DEPLOY --> LOCAL[Attack Local Deployment]
    DEPLOY --> K8S[Attack Kubernetes Deployment]
    DEPLOY --> REMOTE[Attack Remote MCP Servers]
    DEPLOY --> SUPPLY[Supply Chain Attack]
    DEPLOY --> CROSS[Cross-Component Attacks]
    
    LOCAL --> LOCAL_SUB["See: Local Deployment Detail"]
    K8S --> K8S_SUB["See: Kubernetes Deployment Detail"]
    REMOTE --> REMOTE_SUB["See: Remote MCP Detail"]
    SUPPLY --> SUPPLY_SUB["See: Supply Chain Detail"]
    CROSS --> CROSS_SUB["See: Cross-Component Detail"]
    
    classDef highRisk fill:#ff6b6b,stroke:#c92a2a,stroke-width:3px
    classDef mediumRisk fill:#ffd93d,stroke:#f59f00,stroke-width:2px
    classDef detailLink fill:#e3f2fd,stroke:#1976d2,stroke-width:2px,stroke-dasharray: 5 5
    
    class LOCAL,K8S,SUPPLY highRisk
    class REMOTE,CROSS mediumRisk
    class LOCAL_SUB,K8S_SUB,REMOTE_SUB,SUPPLY_SUB,CROSS_SUB detailLink
```

---

## Detailed Attack Trees by Category

### 1. Local Deployment Attacks

Attacks targeting CLI and Desktop UI deployments running on user workstations.

**ToolHive-Specific Elements**: RunConfig manipulation, MCP proxy abuse, permission profile bypass, ToolHive API exploitation

**Generic Infrastructure Elements**: Container runtime vulnerabilities, OS-level secret theft (apply to any containerized app)

```mermaid
graph LR
    LOCAL[Attack Local Deployment] --> LOCAL_OR{OR: Local Attack Vectors}
    LOCAL_OR --> CLI[Attack thv CLI]
    LOCAL_OR --> RUNCONFIG[Manipulate RunConfig]
    LOCAL_OR --> SECRETS_LOCAL[Steal Local Secrets]
    LOCAL_OR --> DESKTOP[Attack ToolHive Studio UI]
    LOCAL_OR --> CONTAINER[Container Runtime Generic]
    
    CLI --> CLI_VULN_OR{OR: thv CLI Exploitation}
    CLI_VULN_OR --> CLI_INJECT[Command Injection via --args]
    CLI_VULN_OR --> CLI_PATH[Path Traversal in --from-config]
    CLI_VULN_OR --> CLI_SECRET[Secret Injection via --secret]
    CLI_VULN_OR --> CLI_RUNTIME[Abuse Runtime Socket Access]
    
    RUNCONFIG --> RC_OR{OR: RunConfig Attacks}
    RC_OR --> RC_TAMPER[Modify Exported RunConfig]
    RC_OR --> RC_PRIVPROFILE[Disable Permission Profile]
    RC_OR --> RC_NETWORK[Disable Network Isolation]
    RC_OR --> RC_VOLUME[Add Malicious Volume Mounts]
    
    CONTAINER --> CONTAINER_OR{OR: Container Runtime}
    CONTAINER_OR --> DOCKER_SOCKET[Docker Socket Abuse - Generic]
    CONTAINER_OR --> DOCKER_API[Runtime API Exploit - Generic]
    
    SECRETS_LOCAL --> SECRETS_LOCAL_OR{OR: Local Secret Theft}
    SECRETS_LOCAL_OR --> KEYRING[Extract from OS Keyring]
    SECRETS_LOCAL_OR --> SECRET_FILE[Read Encrypted Secret File]
    SECRETS_LOCAL_OR --> ENV_VAR[Sniff Environment Variables]
    SECRETS_LOCAL_OR --> MEMORY[Extract from Process Memory]
    
    DESKTOP --> DESKTOP_OR{OR: ToolHive Studio Attacks}
    DESKTOP_OR --> STUDIO_IPC[Abuse thv serve API]
    DESKTOP_OR --> STUDIO_RENDERER[XSS in Server List/Logs]
    DESKTOP_OR --> ELECTRON_VULN[Electron CVE - Generic]
    DESKTOP_OR --> UPDATE_HIJACK[Update Hijack - Generic]
    
    classDef highRisk fill:#ff6b6b,stroke:#c92a2a,stroke-width:3px
    classDef mediumRisk fill:#ffd93d,stroke:#f59f00,stroke-width:2px
    classDef lowRisk fill:#a8dadc,stroke:#1864ab,stroke-width:1px
    classDef toolhive fill:#e1f5fe,stroke:#01579b,stroke-width:2px
    classDef generic fill:#f5f5f5,stroke:#616161,stroke-width:1px
    
    class SECRETS_LOCAL,CLI_INJECT,RC_PRIVPROFILE,CLI_RUNTIME highRisk
    class RUNCONFIG,DESKTOP,STUDIO_IPC mediumRisk
    class ENV_VAR,MEMORY,ELECTRON_VULN lowRisk
```

**Related Documentation**:

- [Architecture: Deployment Modes](../arch/01-deployment-modes.md)
- [Secrets Management Architecture](../arch/04-secrets-management.md)
- [RunConfig and Permissions](../arch/05-runconfig-and-permissions.md)

### 2. Kubernetes Deployment Attacks

Attacks targeting Kubernetes operator deployments in cluster environments.

**ToolHive-Specific Elements**: MCPServer CRD manipulation, thv-operator exploitation, thv-proxyrunner abuse, ToolHive RBAC

**Generic Infrastructure Elements**: etcd access, generic RBAC misconfig, pod security (apply to any K8s operator)

```mermaid
graph LR
    K8S[Attack Kubernetes Deployment] --> K8S_OR{OR: K8s Attack Vectors}
    K8S_OR --> OPERATOR[Attack thv-operator]
    K8S_OR --> PROXY_RUNNER[Attack thv-proxyrunner]
    K8S_OR --> CRD[Manipulate MCPServer CRDs]
    K8S_OR --> K8S_SECRETS[K8s Secrets - Generic]
    K8S_OR --> RBAC[RBAC Misconfig - Generic]
    
    OPERATOR --> OPERATOR_OR{OR: thv-operator Exploitation}
    OPERATOR_OR --> OP_CRD_INJECT[Malicious MCPServer Spec]
    OPERATOR_OR --> OP_REGISTRY[Poison MCPRegistry CRD]
    OPERATOR_OR --> OP_RECONCILE[Reconciliation Logic Flaw]
    OPERATOR_OR --> OP_WEBHOOK[Admission Webhook Bypass - Generic]
    
    CRD --> CRD_OR{OR: MCPServer CRD Attacks}
    CRD_OR --> CRD_PRIVILEGED[Set Privileged: true]
    CRD_OR --> CRD_VOLUME[Mount Host Filesystem]
    CRD_OR --> CRD_SECRET[Reference Wrong Secrets]
    CRD_OR --> CRD_IMAGE[Use Backdoored Image]
    
    PROXY_RUNNER --> PROXY_OR{OR: thv-proxyrunner Attacks}
    PROXY_OR --> PROXY_MIDDLEWARE[Bypass Middleware Chain]
    PROXY_OR --> PROXY_STATEFUL[Create Malicious StatefulSet]
    PROXY_OR --> PROXY_K8S_API[Abuse K8s API Permissions]
    
    K8S_SECRETS --> K8S_SECRETS_OR{OR: K8s Secret Theft}
    K8S_SECRETS_OR --> ETCD_ACCESS[Direct etcd - Generic]
    K8S_SECRETS_OR --> RBAC_ABUSE[RBAC Abuse - Generic]
    K8S_SECRETS_OR --> POD_MOUNT[Access MCP Server Secrets]
    
    classDef highRisk fill:#ff6b6b,stroke:#c92a2a,stroke-width:3px
    classDef mediumRisk fill:#ffd93d,stroke:#f59f00,stroke-width:2px
    classDef lowRisk fill:#a8dadc,stroke:#1864ab,stroke-width:1px
    
    class CRD_PRIVILEGED,CRD_VOLUME,PROXY_MIDDLEWARE,OP_CRD_INJECT highRisk
    class CRD,PROXY_RUNNER,OP_REGISTRY,PROXY_STATEFUL mediumRisk
    class OP_RECONCILE,RBAC_ABUSE lowRisk
```

**Related Documentation**:

- [Kubernetes Operator README](../../cmd/thv-operator/README.md)
- [Operator Architecture](../arch/09-operator-architecture.md)
- [MCPServer CRD API Reference](../operator/crd-api.md)

### 3. Remote MCP Server Attacks

Attacks targeting ToolHive's OAuth/OIDC authentication flows and remote MCP server connections.

**ToolHive-Specific Elements**: RFC 9728 discovery exploitation, dynamic registration abuse, resource parameter manipulation

**Generic Infrastructure Elements**: Standard OAuth vulnerabilities (apply to any OAuth client)

```mermaid
graph LR
    REMOTE[Attack Remote MCP Servers] --> REMOTE_OR{OR: Remote MCP Attack Vectors}
    REMOTE_OR --> OAUTH[Attack ToolHive OAuth Flow]
    REMOTE_OR --> DISCOVERY[Exploit RFC 9728 Discovery]
    REMOTE_OR --> DYNAMIC_REG[Abuse Dynamic Registration]
    REMOTE_OR --> TOKEN_THEFT[Token Theft - Generic]
    
    OAUTH --> OAUTH_OR{OR: ToolHive OAuth Exploitation}
    OAUTH_OR --> PKCE_BYPASS[Bypass PKCE Enforcement]
    OAUTH_OR --> REDIRECT_HIJACK[localhost Callback Hijack]
    OAUTH_OR --> RESOURCE_PARAM[Resource Parameter Manipulation]
    
    DISCOVERY --> DISC_OR{OR: Discovery Attacks}
    DISC_OR --> WELLKNOWN_SPOOF[Spoof .well-known Endpoint]
    DISC_OR --> METADATA_POISON[Poison Resource Metadata]
    DISC_OR --> ISSUER_SPOOF[Fake Authorization Server]
    
    DYNAMIC_REG --> DYN_OR{OR: Dynamic Registration}
    DYN_OR --> REG_FLOOD[Register Many Clients]
    DYN_OR --> REG_ABUSE[Malicious Redirect URIs]
    
    TOKEN_THEFT --> TOKEN_OR{OR: Token Theft Methods}
    TOKEN_OR --> TOKEN_MEMORY[Extract from thv Memory]
    TOKEN_OR --> TOKEN_LEAK[Token in Logs/Errors]
    TOKEN_OR --> TOKEN_PHISH[Phishing - Generic]
    
    classDef highRisk fill:#ff6b6b,stroke:#c92a2a,stroke-width:3px
    classDef mediumRisk fill:#ffd93d,stroke:#f59f00,stroke-width:2px
    classDef lowRisk fill:#a8dadc,stroke:#1864ab,stroke-width:1px
    
    class PKCE_BYPASS,WELLKNOWN_SPOOF,RESOURCE_PARAM,METADATA_POISON highRisk
    class DISCOVERY,DYNAMIC_REG,ISSUER_SPOOF mediumRisk
    class TOKEN_LEAK,TOKEN_PHISH lowRisk
```

**Related Documentation**:

- [Remote MCP Authentication](../remote-mcp-authentication.md)
- [Authorization Framework](../authz.md)

### 4. Supply Chain Attacks

Attacks targeting ToolHive's software supply chain, from MCP registries to build pipelines.

**ToolHive-Specific Elements**: MCP registry manipulation, MCPRegistry CRD poisoning, protocol builds (uvx://, npx://, go://)

**Generic Infrastructure Elements**: Standard supply chain attacks (apply to any software)

```mermaid
graph LR
    SUPPLY[Supply Chain Attack] --> SUPPLY_OR{OR: Supply Chain Vectors}
    SUPPLY_OR --> TH_REGISTRY[Poison ToolHive Registry]
    SUPPLY_OR --> MCP_IMAGE[Backdoored MCP Server Image]
    SUPPLY_OR --> PROTOCOL_BUILD[Malicious Protocol Build]
    SUPPLY_OR --> DEPENDENCY[Malicious Dependency - Generic]
    SUPPLY_OR --> BUILD[Build Pipeline - Generic]
    
    TH_REGISTRY --> REGISTRY_OR{OR: ToolHive Registry Attacks}
    REGISTRY_OR --> REG_JSON[Modify registry.json]
    REGISTRY_OR --> REG_GIT[Compromise Git Registry Source]
    REGISTRY_OR --> REG_CONFIGMAP[Modify MCPRegistry ConfigMap]
    REGISTRY_OR --> REG_MITM[MITM Registry Fetch]
    
    MCP_IMAGE --> IMAGE_OR{OR: MCP Image Attacks}
    IMAGE_OR --> IMAGE_TYPO[Typosquat MCP Server Name]
    IMAGE_OR --> IMAGE_TROJAN[Trojanized Popular MCP Server]
    IMAGE_OR --> IMAGE_UPDATE[Compromise Image in Registry]
    
    PROTOCOL_BUILD --> PROTO_OR{OR: Protocol Build Attacks}
    PROTO_OR --> UVX_POISON[Poison uvx:// Package]
    PROTO_OR --> NPX_POISON[Poison npx:// Package]
    PROTO_OR --> GO_POISON[Malicious go:// Module]
    
    classDef highRisk fill:#ff6b6b,stroke:#c92a2a,stroke-width:3px
    classDef mediumRisk fill:#ffd93d,stroke:#f59f00,stroke-width:2px
    classDef lowRisk fill:#a8dadc,stroke:#1864ab,stroke-width:1px
    
    class REG_JSON,IMAGE_TROJAN,UVX_POISON,NPX_POISON highRisk
    class TH_REGISTRY,MCP_IMAGE,PROTOCOL_BUILD mediumRisk
    class REG_MITM,DEPENDENCY lowRisk
```

**Related Documentation**:

- [Registry System Architecture](../arch/06-registry-system.md)
- [Registry Documentation](../registry/)
- [MCPRegistry CRD](../../cmd/thv-operator/REGISTRY.md)

### 5. Cross-Component Attacks

Attacks that span multiple components, including ToolHive's middleware, MCP tool abuse, and network isolation bypass.

**ToolHive-Specific Elements**: Cedar policy exploitation, MCP tool permission abuse, ToolHive egress proxy bypass

**Generic Infrastructure Elements**: Standard auth bypass, generic network attacks

#### 5.1 Middleware Chain Attacks

```mermaid
graph LR
    MIDDLEWARE[Attack ToolHive Middleware] --> MW_OR{OR: Middleware Attacks}
    MW_OR --> AUTH_BYPASS[Bypass JWT Auth]
    MW_OR --> AUTHZ_BYPASS[Bypass Cedar Authorization]
    MW_OR --> AUDIT_TAMPER[Tamper Audit Logs]
    MW_OR --> MW_ORDER[Exploit Middleware Order]
    
    AUTH_BYPASS --> AUTH_OR{OR: JWT Auth Bypass}
    AUTH_OR --> JWT_FORGE[Forge JWT Token]
    AUTH_OR --> JWT_WEAK[Exploit Weak JWT Secret]
    AUTH_OR --> JWT_SKIP[Skip JWT Middleware]
    
    AUTHZ_BYPASS --> AUTHZ_OR{OR: Cedar Authz Bypass}
    AUTHZ_OR --> CEDAR_POLICY[Exploit Cedar Policy Logic]
    AUTHZ_OR --> CEDAR_CONTEXT[Cedar Context Injection]
    AUTHZ_OR --> IDOR_MCP[IDOR on MCP Tools/Resources]
    AUTHZ_OR --> TOOL_FILTER[Bypass Tool Filter]
    
    classDef highRisk fill:#ff6b6b,stroke:#c92a2a,stroke-width:3px
    classDef mediumRisk fill:#ffd93d,stroke:#f59f00,stroke-width:2px
    classDef lowRisk fill:#a8dadc,stroke:#1864ab,stroke-width:1px
    
    class CEDAR_POLICY,CEDAR_CONTEXT,JWT_SKIP,IDOR_MCP highRisk
    class AUTH_BYPASS,AUTHZ_BYPASS,TOOL_FILTER mediumRisk
    class AUDIT_TAMPER,MW_ORDER lowRisk
```

**Related Documentation**:

- [Middleware Architecture](../middleware.md)
- [Authorization Framework (Cedar)](../authz.md)

#### 5.2 Data Exfiltration via MCP Tools

```mermaid
graph LR
    EXFIL[Data Exfiltration via MCP]
    
    EXFIL --> EXFIL_OR{OR: Exfiltration Methods}
    EXFIL_OR --> MCP_ABUSE[Abuse MCP Tool Permissions]
    EXFIL_OR --> VOLUME_ACCESS[Read Mounted Volumes]
    EXFIL_OR --> NETWORK_EXFIL[Bypass Network Isolation]
    EXFIL_OR --> LOGS_EXFIL[Extract Data via Logs]
    
    MCP_ABUSE --> MCP_OR{OR: MCP Tool Abuse}
    MCP_OR --> TOOL_FETCH[Fetch MCP Server]
    MCP_OR --> TOOL_FS[Filesystem MCP Server]
    MCP_OR --> TOOL_EXEC[Command Exec MCP Server]
    MCP_OR --> TOOL_CUSTOM[Overprivileged Custom Tool]
    
    classDef highRisk fill:#ff6b6b,stroke:#c92a2a,stroke-width:3px
    classDef mediumRisk fill:#ffd93d,stroke:#f59f00,stroke-width:2px
    classDef lowRisk fill:#a8dadc,stroke:#1864ab,stroke-width:1px
    
    class MCP_ABUSE,TOOL_EXEC,TOOL_CUSTOM highRisk
    class VOLUME_ACCESS,NETWORK_EXFIL mediumRisk
    class LOGS_EXFIL lowRisk
```

**Related Documentation**:

- [RunConfig and Permissions](../arch/05-runconfig-and-permissions.md)

#### 5.3 ToolHive Network Isolation Bypass

```mermaid
graph LR
    NET_BYPASS[Bypass ToolHive Network Isolation]
    
    NET_BYPASS --> NET_OR{OR: Isolation Bypass}
    NET_OR --> PROXY_BYPASS[Bypass ToolHive Egress Proxy]
    NET_OR --> DNS_BYPASS[Bypass ToolHive DNS]
    NET_OR --> SQUID_VULN[Exploit Squid in Proxy]
    NET_OR --> NO_PROXY[Set NO_PROXY Variable]
    NET_OR --> PROTOCOL_TUNNEL[Protocol Tunneling - Generic]
    
    PROXY_BYPASS --> PB_OR{OR: Proxy Bypass Methods}
    PB_OR --> DIRECT_CONNECT[Hardcoded IP Address]
    PB_OR --> NONHTTP[Non-HTTP/HTTPS Protocol]
    PB_OR --> ACL_BYPASS[Exploit ACL Misconfiguration]
    
    classDef highRisk fill:#ff6b6b,stroke:#c92a2a,stroke-width:3px
    classDef mediumRisk fill:#ffd93d,stroke:#f59f00,stroke-width:2px
    classDef lowRisk fill:#a8dadc,stroke:#1864ab,stroke-width:1px
    
    class PROXY_BYPASS,NO_PROXY,SQUID_VULN highRisk
    class DNS_BYPASS,ACL_BYPASS mediumRisk
    class PROTOCOL_TUNNEL lowRisk
```

**Related Documentation**:

- [Runtime Implementation Guide (Network Isolation)](../runtime-implementation-guide.md)

---

## Legend

### Node Types

- **Root Node**: Main objective of attack (Compromise ToolHive Platform)
- **{OR}**: Any one child path is sufficient to achieve parent goal
- **{AND}**: All child paths must succeed to achieve parent goal
- **(Leaf Nodes)**: Specific attack techniques or actions

### Attack Specificity

- **ToolHive-Specific**: Attacks that exploit ToolHive's unique features, architecture, or implementation
  - Examples: MCPServer CRD manipulation, Cedar policy bypass, RunConfig tampering, RFC 9728 discovery exploitation, protocol builds (uvx://, npx://, go://)
- **Generic Infrastructure**: Standard attacks applicable to any system using similar technology (labeled with "- Generic" suffix in diagrams)
  - Examples: etcd access (any K8s app), Docker socket abuse (any container platform), standard OAuth phishing

### Risk Classification

- 🔴 **High Risk (Red)**: Critical impact, leads to full system compromise or secret exposure
- 🟡 **Medium Risk (Yellow)**: Significant impact, may lead to partial compromise or privilege escalation
- 🔵 **Low Risk (Blue)**: Limited impact, requires additional exploitation steps

## Attack Cost Estimates (ToolHive-Specific)

The following table provides estimated costs (attacker effort) and potential impact for key **ToolHive-specific** attack paths. Generic infrastructure attacks (e.g., etcd access, container escape) are excluded.

| Attack Path | Cost | Impact | Target Asset | Prerequisites |
|-------------|------|--------|--------------|---------------|
| **RunConfig Manipulation** | | | | |
| Modify Exported RunConfig | Low | High | Workload configuration | File system access to exported config |
| Disable Permission Profile | Low | Critical | MCP server restrictions | Access to RunConfig before `thv run` |
| Add Malicious Volume Mounts | Low | Critical | Host file system | Ability to modify RunConfig |
| Disable Network Isolation | Low | High | Network restrictions | Access to RunConfig |
| **ToolHive CLI Exploitation** | | | | |
| Command Injection via --args | Medium | Critical | Code execution in container | Craft malicious CLI arguments |
| Path Traversal in --from-config | Medium | High | Read arbitrary files | Control config file path |
| Secret Injection via --secret | Low | Medium | Inject fake secrets | Craft malicious secret references |
| **MCPServer CRD Attacks** | | | | |
| Set Privileged: true in CRD | Low | Critical | Full node compromise | K8s API write for MCPServer CRD |
| Mount Host Filesystem via CRD | Low | Critical | Host data access | K8s API write for MCPServer CRD |
| Reference Wrong Secrets | Low | Medium | Cross-namespace secret access | K8s API write + RBAC misconfig |
| Use Backdoored MCP Image | Medium | Critical | Container compromise | Control image field in CRD |
| **thv-operator Exploitation** | | | | |
| Malicious MCPServer Spec Injection | Medium | Critical | Deploy malicious workload | K8s API write for MCPServer |
| Poison MCPRegistry CRD | Medium | High | Distribute malware | K8s API write for MCPRegistry |
| Reconciliation Logic Flaw | Very High | Medium | Bypass validation | Find operator bug |
| **thv-proxyrunner Attacks** | | | | |
| Bypass Middleware Chain | High | Critical | Skip auth/authz/audit | Exploit proxy logic flaw |
| Create Malicious StatefulSet | Medium | High | Deploy backdoored MCP server | Compromise proxy runner pod |
| Abuse K8s API Permissions | Medium | High | Cluster-wide access | Exploit proxy RBAC permissions |
| **Cedar Authorization Bypass** | | | | |
| Exploit Cedar Policy Logic | Medium | High | Access unauthorized tools | Find policy logic flaw |
| Cedar Context Injection | High | Critical | Forge authorization context | Inject claims/arguments |
| Bypass Tool Filter | Low | Medium | Access filtered tools | Exploit filter logic |
| IDOR on MCP Tools/Resources | Low | Medium | Access other users' MCP tools | Predictable tool IDs |
| **ToolHive OAuth/OIDC Attacks** | | | | |
| Bypass PKCE Enforcement | High | Critical | Session hijacking | Find PKCE validation bug |
| localhost Callback Hijack | Medium | High | Steal authorization code | Local network access |
| Resource Parameter Manipulation | Medium | Medium | Access wrong resources | Manipulate RFC 8707 parameter |
| Spoof .well-known Endpoint | High | Critical | Fake auth server | MITM or DNS control |
| Poison Resource Metadata | High | Critical | Redirect to malicious issuer | MITM RFC 9728 discovery |
| **ToolHive Registry Attacks** | | | | |
| Modify registry.json | Low | Critical | Distribute malware | File system or git access |
| Poison MCPRegistry ConfigMap | Low | Critical | K8s cluster-wide malware | K8s ConfigMap write access |
| Typosquat MCP Server Name | Medium | High | Trick users to install | Register similar name |
| Trojanize Popular MCP Server | High | Critical | Widespread compromise | Compromise popular image |
| **Protocol Build Attacks** | | | | |
| Poison uvx:// Package | Medium | Critical | Python package compromise | PyPI access or MITM |
| Poison npx:// Package | Medium | Critical | npm package compromise | npm registry access |
| Malicious go:// Module | Medium | High | Go module compromise | Control go module |
| **ToolHive Network Isolation** | | | | |
| Bypass ToolHive Egress Proxy | Medium | High | Unrestricted network | Non-HTTP protocol or direct IP |
| Set NO_PROXY Variable | Low | High | Disable proxy | Environment variable injection |
| Bypass ToolHive DNS | Medium | High | DNS resolution bypass | Hardcoded IPs in MCP server |
| Exploit Squid in ToolHive Proxy | High | Critical | Proxy compromise | Unpatched Squid CVE |
| **ToolHive Studio (Desktop UI)** | | | | |
| Abuse thv serve API | Medium | High | Control all local workloads | Access to API server port |
| XSS in Server List/Logs | Low | Medium | Client-side code execution | Inject HTML in server names/logs |

### Cost Levels

- **Low**: Hours to days, script kiddie capability
- **Medium**: Days to weeks, requires specialized knowledge
- **High**: Weeks to months, requires advanced expertise
- **Very High**: Months+, requires deep expertise and/or insider access

### Impact Levels

- **Medium**: Limited scope, affects single workload/user
- **High**: Affects multiple workloads/users, partial system compromise
- **Critical**: Full system compromise, complete data access, persistent control

## Key Attack Chains (ToolHive-Specific)

### Chain 1: RunConfig Tampering to Host Compromise

**ToolHive-Specific**: Exploits RunConfig portability and permission profiles

1. User exports MCP server config: `thv export server1 config.json`
2. Attacker modifies RunConfig to disable permission profile
3. Attacker adds volume mount: `"volumes": ["/:/host:rw"]`
4. User imports and runs: `thv run --from-config config.json`
5. MCP server has full host filesystem access

**Mitigations**:

- Validate RunConfig signatures before import
- Warn users when importing configs with privileged settings
- Implement RunConfig schema validation with security checks

### Chain 2: MCPRegistry Poisoning to Cluster Compromise

**ToolHive-Specific**: Exploits MCPRegistry CRD and auto-sync

1. Attacker gains write access to MCPRegistry ConfigMap or Git source
2. Modifies registry.json to point popular MCP server to backdoored image
3. MCPRegistry controller syncs poisoned data
4. Users run infected server: `thv run popular-mcp-server`
5. Malicious container deployed across cluster with normal permissions
6. Backdoor exfiltrates data or escalates privileges

**Mitigations**:

- Implement registry signing with Sigstore/Cosign
- ConfigMap write access tightly controlled (RBAC)
- Image scanning before deployment
- Git commit signing required for registry sources

### Chain 3: Cedar Policy Bypass to Unauthorized MCP Access

**ToolHive-Specific**: Exploits Cedar context injection

1. Attacker analyzes Cedar policies for authorization logic
2. Finds policy: `permit when { context.claim_role == "admin" }`
3. Crafts MCP request with injected context/claims
4. Exploits middleware ordering to skip JWT validation
5. Bypasses Cedar authorization checks
6. Accesses restricted MCP tools without valid auth

**Mitigations**:

- Validate all context sources in Cedar policies
- Immutable middleware chain ordering
- Never trust client-provided context without signature
- Policy testing framework for edge cases

### Chain 4: RFC 9728 Discovery Exploitation to MITM

**ToolHive-Specific**: Exploits ToolHive's RFC 9728 well-known URI discovery

1. User attempts to connect to remote MCP: `thv run https://mcp.example.com`
2. Attacker performs MITM on network
3. Intercepts `GET /.well-known/oauth-protected-resource`
4. Returns malicious metadata pointing to attacker's auth server
5. ToolHive performs OAuth flow with attacker's server
6. Attacker captures user credentials and tokens

**Mitigations**:

- Certificate pinning for well-known endpoints
- Require DNSSEC validation
- Warn users about untrusted OAuth issuers
- Manual issuer override: `--remote-auth-issuer` flag

### Chain 5: Protocol Build Supply Chain Attack

**ToolHive-Specific**: Exploits uvx://npx://go:// protocol builds

1. Attacker typosquats popular MCP server package
2. Uploads to PyPI: `uvx://mcp-servr` (note typo)
3. User runs: `thv run uvx://mcp-servr` (typo in command)
4. ToolHive builds container from malicious package
5. Malicious code executes during build or runtime
6. Backdoor establishes persistence and exfiltrates data

**Mitigations**:

- Package name validation and typosquat detection
- Sandbox protocol builds in separate environment
- Display package source prominently before build
- Warn on first-time package usage

### Chain 6: thv-proxyrunner to Cluster Escalation

**ToolHive-Specific**: Exploits thv-proxyrunner K8s API permissions

1. Attacker compromises thv-proxyrunner pod
2. Abuses K8s API permissions to create StatefulSets
3. Creates malicious StatefulSet in different namespace
4. StatefulSet mounts K8s service account with elevated permissions
5. Uses elevated permissions to modify other MCPServer CRDs
6. Deploys backdoored MCP servers cluster-wide

**Mitigations**:

- Namespace-scoped RBAC for thv-proxyrunner
- Admission webhooks validate all StatefulSets
- Network policies isolate proxy-runner pods
- Audit all StatefulSet creations by operator components

## Threat Actor Profiles

### Script Kiddie (Low Sophistication)

- **Capabilities**: Uses public exploits, basic tools
- **Targets**: Publicly exposed instances, default configurations
- **Effective Against**: Environment variable sniffing, IDOR, basic XSS
- **Mitigation Priority**: Secure defaults, input validation, basic hardening

### Malicious Insider (Medium Sophistication)

- **Capabilities**: Internal knowledge, legitimate access
- **Targets**: Secrets, data exfiltration, privilege escalation
- **Effective Against**: Secret theft, RBAC abuse, audit tampering
- **Mitigation Priority**: Least privilege, audit logging, separation of duties

### Advanced Persistent Threat (High Sophistication)

- **Capabilities**: Custom exploits, social engineering, supply chain attacks
- **Targets**: Long-term persistence, data exfiltration, infrastructure control
- **Effective Against**: All vectors, especially supply chain and 0-days
- **Mitigation Priority**: Defense in depth, monitoring, incident response

### Nation-State Actor (Very High Sophistication)

- **Capabilities**: 0-day exploits, hardware attacks, insider recruitment
- **Targets**: Critical infrastructure, intellectual property, strategic data
- **Effective Against**: All vectors including hardware/firmware
- **Mitigation Priority**: Assume breach, air gaps, hardware security modules

## References

- [MITRE ATT&CK Container Matrix](https://attack.mitre.org/matrices/enterprise/containers/)
- [NIST Container Security Guide](https://nvlpubs.nist.gov/nistpubs/SpecialPublications/NIST.SP.800-190.pdf)
- [Kubernetes Security Best Practices](https://kubernetes.io/docs/concepts/security/)
- [OWASP Container Security](https://owasp.org/www-community/vulnerabilities/Container_Security)

## Maintenance

This attack tree should be reviewed and updated:

- Quarterly by the security team
- After any significant architectural changes
- Following security incidents or near-misses
- When new threat intelligence emerges

**Last Updated**: 2025-11-19
**Next Review**: 2026-02-19
