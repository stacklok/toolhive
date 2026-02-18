# ToolHive Security Documentation

This directory contains comprehensive security documentation for the ToolHive platform, including threat models, attack trees, and security best practices.

## 📚 Documentation Index

### [Attack Tree](./attack-tree.md)
Visual representation of potential attack vectors against ToolHive across all deployment modes. Includes:
- **Attack chains** showing step-by-step compromise paths
- **Risk classifications** (High/Medium/Low) for each attack vector
- **Cost estimates** for attacker effort and prerequisites
- **Threat actor profiles** from script kiddies to nation-state actors
- **Key attack chains** with detailed mitigation strategies

**Use this when**: Planning defense-in-depth strategies, prioritizing security investments, or assessing threat exposure.

### [Threat Model](./threat-model.md)
STRIDE-based threat analysis of all ToolHive components. Includes:
- **Data flow diagrams** (DFDs) for Local, Kubernetes, and Remote MCP scenarios
- **STRIDE analysis** (Spoofing, Tampering, Repudiation, Information Disclosure, DoS, Privilege Escalation)
- **Critical asset inventory** with sensitivity classifications
- **Trust boundaries** between system components
- **Top 10 critical threats** with immediate action items
- **Security control recommendations** for each component

**Use this when**: Designing new features, reviewing architectural changes, or conducting security assessments.

## 🎯 Quick Reference

### Critical Security Assets (Priority Order)

| Asset | Location | Protection Mechanism |
|-------|----------|---------------------|
| 1. **Secrets (API Keys, Tokens)** | OS Keyring, K8s Secrets | AES-256-GCM, RBAC |
| 2. **Container Runtime Socket** | `/var/run/docker.sock` | Socket authentication, rootless mode |
| 3. **OAuth Access Tokens** | Memory, optional cache | PKCE, short TTL, HTTPS |
| 4. **JWT Signing Keys** | Config files, K8s Secrets | Strong algorithms (RS256/ES256), rotation |
| 5. **etcd Cluster** | Kubernetes control plane | Encryption at rest, network isolation |

### Top 5 Attack Vectors to Mitigate First

1. **Secrets Exposure** (Attack Tree: `SECRETS_LOCAL`, `K8S_SECRETS`)
   - Implement file permissions 0600 on encrypted secrets
   - Enable etcd encryption at rest
   - Audit secret access patterns
   
2. **Container Runtime Abuse** (Attack Tree: `DOCKER_SOCKET`)
   - Never mount Docker socket into containers
   - Use rootless containers where possible
   - Implement runtime authentication

3. **Privilege Escalation** (Threat Model: Elevation of Privilege)
   - Drop all container capabilities by default
   - Enforce Pod Security Standards in Kubernetes
   - Never allow privileged containers

4. **Supply Chain Compromise** (Attack Tree: `SUPPLY`)
   - Implement image signature verification
   - Scan dependencies and images regularly
   - Use registry allow-lists

5. **Authentication Bypass** (Threat Model: Spoofing)
   - Enforce strong JWT signing (RS256/ES256)
   - Implement PKCE for all OAuth flows
   - Validate issuer, audience, and signature

## 🛡️ Security by Deployment Mode

### Local Deployment (CLI/Desktop)

**Primary Threats:**
- Local secret theft from OS keyring
- Container escape via Docker socket
- Electron vulnerabilities in Desktop UI
- Process memory extraction

**Key Mitigations:**
- Enable OS keyring encryption
- Use rootless container runtime
- Keep Electron framework updated
- Implement code signing for binaries

**Documentation**: See [Threat Model §4.1, §4.2, §4.6](./threat-model.md)

### Kubernetes Deployment (Operator)

**Primary Threats:**
- RBAC misconfiguration allowing secret access
- CRD injection to deploy malicious workloads
- etcd direct access
- Operator privilege escalation

**Key Mitigations:**
- Implement least-privilege RBAC
- Enable admission webhooks
- Encrypt etcd at rest
- Use namespace isolation

**Documentation**: See [Threat Model §4.3, §4.4, §4.7](./threat-model.md)

### Remote MCP Servers

**Primary Threats:**
- OAuth/OIDC flow compromise
- PKCE bypass leading to session hijacking
- Man-in-the-middle attacks
- Token theft and replay

**Key Mitigations:**
- Enforce PKCE mandatory
- Implement certificate pinning
- Use short-lived tokens
- Validate issuer and audience

**Documentation**: See [Threat Model §4.10](./threat-model.md), [Remote MCP Authentication](../remote-mcp-authentication.md)

## 🔍 Security Review Checklist

Use this checklist when reviewing pull requests or new features:

### Authentication & Authorization
- [ ] JWT tokens use strong algorithms (RS256/ES256, not HS256)
- [ ] OAuth flows enforce PKCE
- [ ] Cedar policies follow least-privilege principle
- [ ] User inputs are validated before authentication checks
- [ ] Token expiry times are reasonable (access: 15m, refresh: 7d)

### Secrets Management
- [ ] Secrets never hardcoded in code or configs
- [ ] Secrets referenced by name, not embedded
- [ ] Secrets redacted in all logs
- [ ] File permissions 0600 on secret storage
- [ ] K8s secrets use SecretKeyRef, not direct values

### Container Security
- [ ] Containers run as non-root user
- [ ] All capabilities dropped, only required ones added
- [ ] No privileged containers allowed
- [ ] Resource limits (CPU, memory, PID) specified
- [ ] Network isolation enabled for untrusted workloads
- [ ] Volume mounts are read-only where possible

### Network Security
- [ ] All external connections use HTTPS/TLS
- [ ] Certificate validation enabled (no InsecureSkipVerify)
- [ ] Egress proxy enforces allow-list for isolated workloads
- [ ] No sensitive data in URLs or query parameters
- [ ] Rate limiting implemented on public endpoints

### Input Validation
- [ ] All user inputs validated against allow-list
- [ ] Path traversal checks for file operations
- [ ] Command injection prevention (no shell=true)
- [ ] JSON/YAML parsing uses safe libraries
- [ ] Maximum size limits on inputs

### Kubernetes
- [ ] RBAC follows least-privilege (no cluster-admin)
- [ ] Admission webhooks validate CRDs
- [ ] Pod Security Standards enforced
- [ ] Network policies restrict pod-to-pod traffic
- [ ] Secrets mounted as volumes, not environment variables

### Audit & Monitoring
- [ ] Security-relevant events logged (auth, authz, secret access)
- [ ] Logs include correlation IDs for tracing
- [ ] No sensitive data in logs (credentials, PII, tokens)
- [ ] Distributed tracing enabled (OpenTelemetry)
- [ ] Alerts configured for security events

## 📊 Risk Assessment Matrix

| Likelihood → <br> Impact ↓ | Low | Medium | High |
|---------------------------|-----|--------|------|
| **Critical** | Medium Risk | High Risk | **Critical Risk** |
| **High** | Low Risk | Medium Risk | High Risk |
| **Medium** | Low Risk | Low Risk | Medium Risk |
| **Low** | Acceptable | Low Risk | Low Risk |

### Risk Categories
- **Critical Risk**: Immediate action required, security incident likely
- **High Risk**: Address within current sprint, significant threat
- **Medium Risk**: Address within quarter, moderate threat
- **Low Risk**: Address as time permits, minimal threat
- **Acceptable**: No action needed, acceptable risk level

## 🔐 Security Best Practices

### For Developers

1. **Never commit secrets** to version control
   - Use `.gitignore` for config files with secrets
   - Scan commits with tools like `git-secrets` or `trufflehog`

2. **Validate all inputs** at the earliest possible point
   - Reject invalid inputs, don't try to sanitize
   - Use allow-lists, not deny-lists

3. **Fail securely** when errors occur
   - Default to deny access on error
   - Log security-relevant errors
   - Don't expose internal details in error messages

4. **Use security linters**
   - `gosec` for Go code
   - `bandit` for Python code
   - `eslint-plugin-security` for JavaScript

5. **Write security tests**
   - Test authentication bypass scenarios
   - Test authorization with different user roles
   - Test input validation with fuzzing

### For Operators

1. **Keep systems patched**
   - Enable Dependabot/Renovate for dependencies
   - Subscribe to security mailing lists
   - Test patches in staging before production

2. **Monitor security events**
   - Set up alerts for failed authentication
   - Monitor for unusual secret access patterns
   - Track container escape attempts

3. **Practice principle of least privilege**
   - Grant minimum required RBAC permissions
   - Use namespace isolation
   - Regular access reviews

4. **Backup and disaster recovery**
   - Regular backups of secrets and configs
   - Test restore procedures
   - Document incident response plan

5. **Security training**
   - Regular security awareness training
   - Threat modeling workshops
   - Tabletop exercises for incident response

## 🚨 Reporting Security Vulnerabilities

**DO NOT** open public GitHub issues for security vulnerabilities.

Instead, follow our [Security Policy](../../SECURITY.md):

1. Email security@stacklok.com with details
2. Include proof-of-concept if available
3. Wait for response before public disclosure
4. Coordinate disclosure timeline with security team

We typically respond within 48 hours and aim to patch critical issues within 7 days.

## 📖 Related Documentation

### ToolHive Architecture
- [Architecture Overview](../arch/00-overview.md)
- [Deployment Modes](../arch/01-deployment-modes.md)
- [Secrets Management](../arch/04-secrets-management.md)
- [RunConfig and Permissions](../arch/05-runconfig-and-permissions.md)

### Security Features
- [Authorization Framework](../authz.md) - Cedar policies
- [Remote MCP Authentication](../remote-mcp-authentication.md) - OAuth/OIDC
- [Middleware](../middleware.md) - Auth/Authz/Audit chain
- [Runtime Implementation Guide](../runtime-implementation-guide.md) - Security mapping

### Operational Security
- [Kubernetes Integration](../kubernetes-integration.md)
- [Operator Documentation](../../cmd/thv-operator/README.md)
- [Observability](../observability.md) - Logging and monitoring

## 🔄 Maintenance

### Review Schedule

| Activity | Frequency | Owner | Next Due |
|----------|-----------|-------|----------|
| Threat model review | Quarterly | Security Team | 2026-02-19 |
| Attack tree update | Quarterly | Security Team | 2026-02-19 |
| Penetration testing | Annually | External Auditor | TBD |
| Security training | Bi-annually | All Teams | TBD |
| Incident response drill | Quarterly | DevOps + Security | TBD |

### Change Management

When to update these documents:
- ✅ New features that handle secrets or authentication
- ✅ Changes to RBAC or permission models
- ✅ New deployment modes or components
- ✅ After security incidents or near-misses
- ✅ New threat intelligence or attack patterns
- ❌ Minor bug fixes without security impact
- ❌ Documentation-only changes
- ❌ Performance optimizations

### Version History

| Version | Date | Changes | Author |
|---------|------|---------|--------|
| 1.0 | 2025-11-19 | Initial release with attack tree and threat model | Security Team |

## 🤝 Contributing

Security improvements are always welcome! When contributing:

1. **For new features**: Update threat model with STRIDE analysis
2. **For security fixes**: Reference threat model sections addressed
3. **For architectural changes**: Update attack tree with new vectors
4. **For incident learnings**: Document in threat model and attack tree

See [CONTRIBUTING.md](../../CONTRIBUTING.md) for general contribution guidelines.

## 📝 License

These security documents are part of the ToolHive project and are licensed under [Apache 2.0](../../LICENSE).

---

**Questions or concerns?** Contact security@stacklok.com or open a discussion in our [Discord](https://discord.gg/stacklok).

