# EmbeddingServer Examples

This directory contains example configurations for deploying HuggingFace embedding inference servers using the EmbeddingServer custom resource.

## Overview

The EmbeddingServer CRD allows you to deploy and manage HuggingFace Text Embeddings Inference (TEI) servers in Kubernetes. These servers provide high-performance embedding generation for various NLP tasks.

## Examples

### 1. Basic Embedding Server

File: `basic-embedding.yaml`

A minimal configuration that deploys an embedding server with default settings:
- Uses `sentence-transformers/all-MiniLM-L6-v2` model
- Single replica
- Default port (8080)
- No persistent storage

```bash
kubectl apply -f basic-embedding.yaml
```

### 2. Embedding with Model Cache

File: `embedding-with-cache.yaml`

Configures persistent storage for downloaded models:
- Model cache enabled with 10Gi PVC
- Resource limits specified
- Environment variables configured
- Faster restarts after initial model download

```bash
kubectl apply -f embedding-with-cache.yaml
```

### 3. Embedding with Group Association

File: `embedding-with-group.yaml`

Shows how to organize embeddings using MCPGroup:
- Creates an MCPGroup named `ml-services`
- Associates the embedding server with the group
- Enables tracking and organization of related resources

```bash
kubectl apply -f embedding-with-group.yaml
```

### 4. Advanced Configuration

File: `embedding-advanced.yaml`

Demonstrates all available features:
- High availability with 2 replicas
- Custom arguments and environment variables
- Persistent model caching with custom storage class
- PodTemplateSpec for advanced pod customization:
  - Node selection
  - Tolerations
  - Affinity rules
  - Security contexts
- Resource overrides for metadata

```bash
kubectl apply -f embedding-advanced.yaml
```

## Supported Models

EmbeddingServer supports any HuggingFace model compatible with Text Embeddings Inference. Popular choices include:

- `sentence-transformers/all-MiniLM-L6-v2` - Fast, lightweight (384 dimensions)
- `sentence-transformers/all-mpnet-base-v2` - Good balance (768 dimensions)
- `BAAI/bge-large-en-v1.5` - High quality (1024 dimensions)
- `intfloat/e5-large-v2` - Instruction-based embeddings
- `thenlper/gte-large` - General text embeddings

## Accessing the Embedding Service

After deployment, the embedding service is accessible at:

```
http://<embedding-name>.<namespace>.svc.cluster.local:<port>
```

For example, with `basic-embedding` in the `toolhive-system` namespace:

```
http://basic-embedding.toolhive-system.svc.cluster.local:8080
```

### Using the Embedding Service

Generate embeddings using the REST API:

```bash
curl -X POST \
  http://basic-embedding.toolhive-system.svc.cluster.local:8080/embed \
  -H 'Content-Type: application/json' \
  -d '{"inputs": "Hello, world!"}'
```

## Configuration Options

### Required Fields

- `spec.model`: HuggingFace model identifier

### Optional Fields

- `spec.image`: Container image (default: `ghcr.io/huggingface/text-embeddings-inference:latest`)
- `spec.port`: Service port (default: 8080)
- `spec.replicas`: Number of replicas (default: 1)
- `spec.args`: Additional arguments for the embedding server
- `spec.env`: Environment variables
- `spec.resources`: CPU and memory limits/requests
- `spec.modelCache`: Persistent volume configuration for model caching
- `spec.podTemplateSpec`: Advanced pod customization
- `spec.resourceOverrides`: Metadata overrides for created resources
- `spec.groupRef`: Reference to an MCPGroup

## Model Caching

Enabling model caching provides several benefits:

1. **Faster Restarts**: Models are downloaded once and cached
2. **Reduced Network Usage**: No repeated downloads
3. **Improved Reliability**: Not dependent on external network for restarts

Configuration:

```yaml
spec:
  modelCache:
    enabled: true
    size: "10Gi"              # Adjust based on model size
    accessMode: "ReadWriteOnce"
    storageClassName: "fast-ssd"  # Optional
```

## Resource Planning

### CPU and Memory

Recommended resources based on model size:

| Model Type | CPU Request | CPU Limit | Memory Request | Memory Limit |
|------------|-------------|-----------|----------------|--------------|
| Small (< 500MB) | 500m | 2000m | 1Gi | 4Gi |
| Medium (500MB-2GB) | 1000m | 4000m | 2Gi | 8Gi |
| Large (> 2GB) | 2000m | 8000m | 4Gi | 16Gi |

### Storage

Model sizes vary significantly. Check the HuggingFace model page for size information:

- `all-MiniLM-L6-v2`: ~90MB
- `all-mpnet-base-v2`: ~420MB
- `bge-large-en-v1.5`: ~1.3GB

Recommended PVC sizes:
- Small models: 5Gi
- Medium models: 10Gi
- Large models: 20Gi+

## Monitoring

The embedding server exposes health endpoints:

- `/health`: Health check endpoint (used by Kubernetes probes)
- `/metrics`: Prometheus metrics (if enabled)

## Troubleshooting

### Model Download Issues

If pods are stuck in `Downloading` phase:

1. Check pod logs:
   ```bash
   kubectl logs -n toolhive-system <embedding-pod-name>
   ```

2. Verify network connectivity to HuggingFace Hub

3. Check if model exists and is accessible

### PVC Binding Issues

If PVC is not binding:

1. Check storage class availability:
   ```bash
   kubectl get storageclass
   ```

2. Verify PVC status:
   ```bash
   kubectl get pvc -n toolhive-system
   ```

3. Check PV availability or dynamic provisioning

### Resource Constraints

If pods are pending due to insufficient resources:

1. Check node resources:
   ```bash
   kubectl top nodes
   ```

2. Adjust resource requests in the EmbeddingServer spec

3. Consider node scaling or resource optimization

## Best Practices

1. **Enable Model Caching**: Always enable caching for production deployments
2. **Set Resource Limits**: Prevent resource contention with appropriate limits
3. **Use Groups**: Organize related embeddings with MCPGroup
4. **Monitor Performance**: Use Prometheus metrics for monitoring
5. **Plan Storage**: Allocate sufficient PVC size for your models
6. **Test Before Production**: Validate configuration in non-production first
7. **Version Pins**: Use specific image tags rather than `:latest` for production

## Additional Resources

- [HuggingFace Text Embeddings Inference](https://github.com/huggingface/text-embeddings-inference)
- [ToolHive Documentation](https://docs.toolhive.dev)
- [MCPGroup Documentation](../virtual-mcps/README.md)
