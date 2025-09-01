# Catalog System Design for Kubernetes Registry

## Overview

A **Catalog** is essentially a **special registry with trusted servers** that have undergone validation and approval processes. This document explores implementing catalogs as enhanced MCPRegistry resources, though a dedicated MCPCatalog CRD may be more appropriate depending on the complexity of approval workflows and governance requirements.

## Catalog as Special Registry

Catalogs follow the same promotion workflow:
```
Raw Registry → Staging Registry → Catalog (Trusted Registry) → Production Deployment
```

### Trust Levels
- **Raw Registry**: All servers from upstream sources (trust-level: raw)
- **Staging Registry**: Filtered and tested subset (trust-level: tested)  
- **Catalog Registry**: Validated and approved servers (trust-level: trusted)

### Unified Model Benefits
- **Consistent API**: Same MCPRegistry CRD for all trust levels
- **Natural Promotion**: Clear path from raw → tested → trusted
- **Simplified Tooling**: No separate catalog vs registry commands
- **Environment Mapping**: Dev uses raw/tested, Production uses trusted catalogs

## MCPRegistry with Catalog Features

### Enhanced MCPRegistry for Catalogs

```yaml
apiVersion: toolhive.stacklok.io/v1alpha1
kind: MCPRegistry
metadata:
  name: production-catalog
  namespace: toolhive-system
spec:
  displayName: "Production Approved MCP Servers"
  description: "Trusted servers for production deployment"
  format: toolhive
  
  # Trust level determines validation requirements
  trustLevel: trusted  # raw, tested, trusted
  
  # Source from staging registry
  source:
    type: registry
    registry:
      url: "http://staging-registry-api:8080/api/v1/servers"
  
  # Enhanced validation for trusted registries
  validationPolicy:
    approvalRequired: true
    allowedTiers: ["Official", "Verified"]
    blockedPatterns: ["*-experimental", "*-alpha"]
    securityScanRequired: true
    
  # Promotion criteria for catalog inclusion
  promotionCriteria:
    successfulDeployments: 10
    approvedBy: ["security-team", "platform-team"]
    testValidationPassed: true

status:
  # Standard registry status plus catalog-specific fields
  catalogState:
    approvedServers: 12
    pendingPromotion: 3
    rejectedServers: 1
    lastValidation: "2024-01-15T10:30:00Z"
```

## Key Catalog Features

### 1. **Trust-Based Validation**
- **Automatic Promotion**: Servers move from raw → tested → trusted based on criteria
- **Policy Enforcement**: Only approved servers reach trusted catalogs
- **Security Integration**: Vulnerability scanning and compliance checks
- **Approval Workflows**: Human approval gates for production promotion

### 2. **Unified Deployment Interface**
```bash
# Same command, different trust levels
thv install postgres-server --registry upstream-raw      # Development
thv install postgres-server --registry staging-tested   # QA/Testing  
thv install postgres-server --registry production-catalog # Production (trusted)
```

### 3. **OCI Distribution**
- **Registry Packaging**: Export trusted registries as OCI artifacts
- **Version Control**: Semantic versioning of catalog releases
- **Signature Verification**: Signed artifacts for supply chain security
- **Cross-Cluster Sharing**: Distribute catalogs between environments

## Registry Promotion Workflow

### Data Flow
```
External Sources → Raw Registry → Staging Registry → Trusted Registry (Catalog) → Production
```

### Promotion Pipeline
- **Source Registration**: Raw registries sync from external sources
- **Testing Phase**: Staging registries receive filtered subset for validation
- **Approval Gate**: Manual/automated promotion to trusted catalogs
- **Production Deployment**: Only trusted catalog servers reach production

### Validation Pipeline Requirements

The trust-based promotion requires a **validation pipeline** that needs deeper design:

#### Essential Validations
- **Security Scanning**: Vulnerability and compliance checks
- **Functional Testing**: MCP protocol compatibility and performance
- **Approval Workflows**: Human gates for production promotion
- **Audit Trails**: Complete promotion history and compliance tracking

*Note: Comprehensive validation pipeline design is outside scope of this proposal.*

## Review and Approval Process

### Approval Workflow Requirements

The transition from staging to production catalogs requires a **human review and approval process**:

#### **Review Queue Management**
- **Pending Servers**: Track servers awaiting approval with metadata (test results, deployment history)
- **Reviewer Assignment**: Route servers to appropriate teams based on category/risk level
- **Review Status**: Track review progress (submitted, under-review, approved, rejected)
- **Approval History**: Maintain audit trail of who approved what and when

#### **Integration Points**
- **External Systems**: JIRA, ServiceNow, GitHub PR workflows for approval tracking
- **Notification Systems**: Alert reviewers when servers are ready for approval
- **CLI Tools**: `thv review list/approve/reject` commands for reviewer workflow
- **Dashboard Integration**: Web UI for reviewing server details and approval status

#### **Approval Criteria**
- **Automated Gates**: Security scans, performance benchmarks, successful deployment count
- **Manual Assessment**: Code quality review, documentation completeness, business impact
- **Multi-Team Approval**: Require sign-off from security, platform, and business teams
- **Escalation Rules**: Auto-approve after timeout or escalate to management

### Server Approval Status Tracking

Each server in the promotion pipeline needs detailed status tracking:

```yaml
serverApprovalStatus:
  - serverName: "postgres-server"
    version: "1.3.0"
    currentStatus: "under-review"          # pending, under-review, approved, rejected
    submittedAt: "2024-01-15T10:00:00Z"
    assignedReviewers: ["security-team", "platform-team"]
    approvalProgress:
      securityTeam: "approved"
      platformTeam: "pending"
    automatedChecks:
      securityScan: "passed"
      performanceTest: "passed"
      deploymentCount: 15
    reviewComments:
      - author: "alice@security"
        timestamp: "2024-01-16T09:00:00Z"
        comment: "Security review completed - approved"
    externalTickets:
      - system: "JIRA"
        ticketId: "SEC-12345"
        status: "Approved"
```

This approval tracking ensures **governance transparency** and **audit compliance** while enabling automated promotion when all criteria are met.

## Initial Implementation

### Simple Approach: Pre-approved Registry
Instead of complex validation pipelines, start with **manual curation**:

```yaml
apiVersion: toolhive.stacklok.io/v1alpha1
kind: MCPRegistry
metadata:
  name: production-catalog
spec:
  displayName: "Production Approved Servers"
  trustLevel: trusted
  source:
    type: url
    url:
      url: "https://catalog.company.com/approved-servers.json"
  # JSON contains only manually approved servers
```

### Migration Path
1. **Phase 1**: Manual JSON curation (immediate)
2. **Phase 2**: Add trust levels to MCPRegistry CRD
3. **Phase 3**: Build automated validation pipeline
4. **Phase 4**: Full promotion workflow automation

## Conclusion

Catalogs are **trusted registries** that provide governance and curation for production environments. By treating catalogs as enhanced MCPRegistry resources with trust levels, the system maintains consistency while enabling sophisticated validation and promotion workflows. This unified approach simplifies tooling and provides a natural path from development (raw registries) to production (trusted catalogs).