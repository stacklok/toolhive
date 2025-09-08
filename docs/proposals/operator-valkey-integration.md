# Valkey Integration for ToolHive Operator

## Overview

This proposal outlines a secure-by-default approach for integrating Valkey (Redis-compatible) session storage into the ToolHive operator. The design prioritizes ease of use with automatic security configuration, enabling distributed session storage for horizontal scaling of MCP servers.

## Design Philosophy

1. **Secure by default** - All security features enabled automatically
2. **Zero configuration** - Works out of the box with sensible defaults
3. **Simple sizing** - Just pick small/medium/large
4. **Automatic management** - Operator handles all complexity

## Proposed Architecture: SessionStorage CRD

A minimal CRD that automatically configures everything:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: SessionStorage
metadata:
  name: shared-storage
  namespace: toolhive-system
spec:
  # Just pick a size - everything else is automatic
  size: medium  # small, medium, or large
  
  # Optional: Use external Redis instead of managed Valkey
  external:
    url: redis://external-redis:6379  # Optional
    secretRef: external-redis-auth    # Optional
    
status:
  phase: Ready
  connectionSecret: shared-storage-connection  # Auto-generated
  message: "Storage is ready and secured"
```

## Automatic Security Implementation

### How It Works

When you create a SessionStorage resource, the operator automatically:

1. **Generates strong authentication** - Creates random 32-character password
2. **Configures network isolation** - Only ToolHive proxies can connect
3. **Enables persistence** - Data survives pod restarts (except "small" size)
4. **Sets up monitoring** - Exports metrics for observability
5. **Creates connection secret** - MCPServers just reference this

### Size Presets

```go
// Simple presets that configure everything automatically
var sizePresets = map[string]Config {
    "small": {  // Development
        Replicas:   1,
        Memory:     "256Mi",
        CPU:        "100m",
        Disk:       "1Gi",
        Persistent: false,  // Ephemeral for dev
    },
    "medium": {  // Staging/Small Production
        Replicas:   1,
        Memory:     "512Mi",
        CPU:        "250m", 
        Disk:       "5Gi",
        Persistent: true,
    },
    "large": {  // Production HA
        Replicas:   3,  // Automatic HA cluster
        Memory:     "1Gi",
        CPU:        "500m",
        Disk:       "10Gi",
        Persistent: true,
    },
}
```

### Automatic Security Features

The operator automatically configures:

#### 1. Authentication
```go
func (r *SessionStorageReconciler) ensureSecurity(ctx context.Context, 
    storage *mcpv1alpha1.SessionStorage) error {
    
    // Generate auth secret if it doesn't exist
    authSecret := r.generateAuthSecret(storage)
    
    // Configure Valkey with auth
    valkeyConfig := fmt.Sprintf(`
        requirepass %s
        maxmemory-policy allkeys-lru
        save ""  # Disable RDB snapshots, use AOF only
        appendonly yes
        appendfsync everysec
    `, authSecret.Password)
    
    // Create ConfigMap with Valkey config
    return r.createValkeyConfig(ctx, storage, valkeyConfig)
}
```

#### 2. Network Isolation
```yaml
# Automatically created NetworkPolicy
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {storage-name}-valkey
spec:
  podSelector:
    matchLabels:
      toolhive.stacklok.dev/storage: {storage-name}
  ingress:
  - from:
    - podSelector:
        matchLabels:
          toolhive.stacklok.dev/component: proxy
    ports:
    - port: 6379
```

#### 3. Connection Secret
```yaml
# Automatically created for MCPServers to use
apiVersion: v1
kind: Secret
metadata:
  name: {storage-name}-connection
data:
  REDIS_URL: base64(redis://:password@service:6379)
  REDIS_PASSWORD: base64(generated-password)
  SESSION_STORAGE_TYPE: base64(redis)
```

## Usage Examples

### 1. Simple Development Setup

```yaml
# Just this - operator handles everything else
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: SessionStorage
metadata:
  name: dev-storage
spec:
  size: small
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-server
spec:
  image: my-mcp:latest
  sessionStorageRef: dev-storage  # That's it!
```

### 2. Production Setup

```yaml
# Still simple - just pick "large" for HA
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: SessionStorage
metadata:
  name: prod-storage
  namespace: toolhive-system
spec:
  size: large  # Automatic 3-node HA cluster
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: production-server
spec:
  image: my-mcp:latest
  sessionStorageRef:
    name: prod-storage
    namespace: toolhive-system
```

### 3. Using External Redis

```yaml
# For when you have existing Redis infrastructure
apiVersion: v1
kind: Secret
metadata:
  name: external-redis-auth
data:
  password: base64(your-redis-password)
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: SessionStorage
metadata:
  name: external-storage
spec:
  external:
    url: redis://my-redis.example.com:6379
    secretRef: external-redis-auth
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-server
spec:
  image: my-mcp:latest
  sessionStorageRef: external-storage
```

## Implementation Details

### SessionStorage Controller

```go
func (r *SessionStorageReconciler) Reconcile(ctx context.Context, 
    req ctrl.Request) (ctrl.Result, error) {
    
    storage := &mcpv1alpha1.SessionStorage{}
    if err := r.Get(ctx, req.NamespacedName, storage); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
    
    // Handle external storage differently
    if storage.Spec.External != nil {
        return r.reconcileExternal(ctx, storage)
    }
    
    // For managed Valkey, do everything automatically
    steps := []func(context.Context, *mcpv1alpha1.SessionStorage) error{
        r.ensureAuthSecret,      // Generate password
        r.ensureNetworkPolicy,   // Create network isolation
        r.ensureValkeyConfig,    // Create config with auth
        r.ensureValkeyDeployment,// Deploy Valkey
        r.ensureValkeyService,   // Create service
        r.ensureConnectionSecret,// Create connection info
        r.updateStatus,          // Update CRD status
    }
    
    for _, step := range steps {
        if err := step(ctx, storage); err != nil {
            return ctrl.Result{}, err
        }
    }
    
    return ctrl.Result{}, nil
}
```

### MCPServer Integration

MCPServers automatically get configured with the connection:

```go
func (r *MCPServerReconciler) injectSessionStorage(ctx context.Context,
    mcpServer *mcpv1alpha1.MCPServer, deployment *appsv1.Deployment) error {
    
    if mcpServer.Spec.SessionStorageRef == nil {
        return nil  // No session storage configured
    }
    
    // Get the connection secret created by SessionStorage controller
    secretName := fmt.Sprintf("%s-connection", mcpServer.Spec.SessionStorageRef)
    
    // Add environment variables from secret
    container := &deployment.Spec.Template.Spec.Containers[0]
    container.EnvFrom = append(container.EnvFrom, corev1.EnvFromSource{
        SecretRef: &corev1.SecretEnvSource{
            LocalObjectReference: corev1.LocalObjectReference{
                Name: secretName,
            },
        },
    })
    
    return nil
}
```

## Benefits

1. **Zero Learning Curve** - Just set size: small/medium/large
2. **Production Ready** - Secure by default, no manual configuration
3. **Automatic Updates** - Operator handles version upgrades
4. **Cost Efficient** - Share storage across multiple MCPServers
5. **Flexible** - Support both managed and external storage

## Resilience and Scaling Benefits

By externalizing session storage from the proxy pods, we enable:

### Proxy Resilience
- **Stateless Proxies**: Proxy pods can be terminated, restarted, or rescheduled without losing sessions
- **Rolling Updates**: Deploy new proxy versions with zero downtime - sessions persist in Valkey
- **Crash Recovery**: If a proxy crashes, users reconnect to any other proxy and continue their session
- **Horizontal Pod Autoscaling**: Scale proxy replicas up/down based on load without session disruption

### Example Scaling Scenario
```yaml
# HPA for proxy deployment - sessions remain intact during scaling
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: mcp-proxy-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: github-server  # MCPServer deployment
  minReplicas: 2
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
```

When load increases:
1. HPA scales proxy pods from 2 → 10 replicas
2. New pods connect to the same Valkey instance
3. Sessions are immediately available to all pods
4. Load balancer distributes traffic across all proxies
5. Users experience no interruption

When load decreases:
1. HPA scales down from 10 → 2 replicas
2. Pods are gracefully terminated
3. Sessions remain in Valkey
4. Remaining pods continue serving all sessions

## Security Features (All Automatic)

- ✅ Strong password authentication (32 characters, random)
- ✅ Network isolation (NetworkPolicy)
- ✅ Secure defaults (no default user, restricted commands)
- ✅ Automatic secret rotation (operator can handle this)
- ✅ Least privilege (Valkey only accessible by proxies)
- ✅ Persistence encryption (when StorageClass supports it)

## Migration Path

### Phase 1: MVP
- Basic SessionStorage CRD
- Automatic auth and network policies
- Support for small/medium/large sizes

### Phase 2: Production Features
- Automatic backups
- Metrics and monitoring
- Secret rotation

### Phase 3: Advanced Features
- Multi-region support
- Automatic scaling based on load
- Integration with cloud Redis services

## FAQ

**Q: What if I need custom Valkey configuration?**
A: Use external mode with your own Redis/Valkey instance.

**Q: How secure is the automatic setup?**
A: Very secure - uses strong passwords, network isolation, and follows Redis security best practices.

**Q: Can I see the generated password?**
A: Yes, it's in the secret `{storage-name}-auth` but you don't need it - MCPServers use it automatically.

**Q: What happens if a Valkey pod crashes?**
A: For medium/large sizes, data is persisted and will be restored. For small (dev), data is ephemeral.

**Q: Can multiple MCPServers share one SessionStorage?**
A: Yes! That's the recommended pattern for production.

## Conclusion

This design makes distributed session storage as easy as setting `size: medium` while maintaining production-grade security. The operator handles all the complexity automatically, letting developers focus on their MCP servers instead of infrastructure configuration.

Most importantly, by decoupling session state from proxy pods, we transform the ToolHive proxy layer into a truly stateless, resilient system that can scale elastically in response to load. Proxies can crash, restart, or scale from 1 to 100 replicas without any impact on user sessions. This architecture enables cloud-native deployment patterns like rolling updates, auto-scaling, and multi-region deployments while maintaining session continuity.

By providing secure defaults and automatic management, we enable both development simplicity and production readiness without requiring deep Redis/Valkey expertise.