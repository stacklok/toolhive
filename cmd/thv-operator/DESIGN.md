# Design & Decisions

This document aims to help fill in gaps of any decision that are made around the design of the ToolHive Operator.

## CRD Attribute vs `PodTemplateSpec`

When building operators, the decision of when to use a `podTemplateSpec` and when to use a CRD attribute is always disputed. For the ToolHive Operator we have a defined rule of thumb.

### Use Dedicated CRD Attributes For:
- **Business logic** that affects your operator's behavior
- **Validation requirements** (ranges, formats, constraints)  
- **Cross-resource coordination** (affects Services, ConfigMaps, etc.)
- **Operator decision making** (triggers different reconciliation paths)

```yaml
spec:
  version: "13.4"           # Affects operator logic
  replicas: 3               # Affects scaling behavior  
  backupSchedule: "0 2 * * *"  # Needs validation
```

### Use PodTemplateSpec For:
- **Infrastructure concerns** (node selection, resources, affinity)
- **Sidecar containers** 
- **Standard Kubernetes pod configuration**
- **Things a cluster admin would typically configure**

```yaml
spec:
  podTemplate:
    spec:
      nodeSelector:
        disktype: ssd
      containers:
      - name: sidecar
        image: monitoring:latest
```

## Quick Decision Test:
1. **"Does this affect my operator's reconciliation logic?"** -> Dedicated attribute
2. **"Is this standard Kubernetes pod configuration?"** -> PodTemplateSpec  
3. **"Do I need to validate this beyond basic Kubernetes validation?"** -> Dedicated attribute

This gives you a clean API for core functionality while maintaining flexibility for infrastructure concerns.