# 迁移到自建 Kubernetes 集群指南

本指南说明如何将 Toolhive Operator 从 Google GKE（使用 LoadBalancer）迁移到自建 Kubernetes 集群（使用 Ingress）。

## 概述

### 原架构（GKE）
- 使用 LoadBalancer 类型的 Service
- 依赖 Google Cloud Load Balancer
- 通过 LoadBalancer IP 提供外部访问

### 新架构（自建 K8s）
- 使用 ClusterIP 类型的 Service
- 通过 Ingress 提供外部访问
- 支持自定义域名和 TLS

## 主要变更

### 1. Service 类型变更
- **之前**: `ServiceTypeLoadBalancer` + Google Cloud 注解
- **现在**: `ServiceTypeClusterIP`

### 2. 外部访问方式
- **之前**: 通过 LoadBalancer IP 直接访问
- **现在**: 通过 Ingress 域名访问

### 3. URL 生成逻辑
- **之前**: `http://<LoadBalancer-IP>:<port>/sse`
- **现在**: `https://<domain><path>/sse`

## 配置说明

### 基本配置

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-mcp-server
  namespace: toolhive-system
spec:
  image: "your-mcp-server-image"
  transport: "sse"
  port: 8080
  
  # Ingress 配置
  ingress:
    enabled: true                    # 启用 Ingress
    host: "mcp.yourdomain.com"      # 域名
    path: "/"                       # 路径（可选，默认 "/"）
    pathType: "Prefix"              # 路径类型（可选，默认 "Prefix"）
    ingressClassName: "nginx"       # Ingress 控制器类名（可选）
```

### 高级配置

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: advanced-mcp-server
  namespace: toolhive-system
spec:
  image: "your-mcp-server-image"
  transport: "sse"
  port: 8080
  
  ingress:
    enabled: true
    host: "mcp.yourdomain.com"
    path: "/api/mcp"
    pathType: "Prefix"
    ingressClassName: "nginx"
    
    # TLS 配置
    tls:
      enabled: true
      secretName: "mcp-tls-secret"
    
    # 自定义注解
    annotations:
      nginx.ingress.kubernetes.io/rewrite-target: "/"
      nginx.ingress.kubernetes.io/ssl-redirect: "true"
      cert-manager.io/cluster-issuer: "letsencrypt-prod"
      nginx.ingress.kubernetes.io/cors-allow-origin: "*"
```

## 部署步骤

### 1. 更新 Operator
```bash
# 应用更新的 CRD
kubectl apply -f deploy/operator/crds/

# 更新 Operator 部署
kubectl apply -f deploy/operator/operator.yaml
```

### 2. 配置 Ingress 控制器
确保你的集群已安装 Ingress 控制器（如 NGINX Ingress Controller）：

```bash
# 安装 NGINX Ingress Controller（示例）
kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.8.2/deploy/static/provider/cloud/deploy.yaml
```

### 3. 配置 DNS
确保你的域名正确解析到 Ingress Controller 的外部 IP：

```bash
# 获取 Ingress Controller 的外部 IP
kubectl get svc -n ingress-nginx ingress-nginx-controller

# 配置 DNS A 记录
# mcp.yourdomain.com -> <EXTERNAL-IP>
```

### 4. 创建 MCPServer
```bash
# 应用 MCPServer 配置
kubectl apply -f your-mcpserver-config.yaml

# 检查状态
kubectl get mcpserver -n toolhive-system
kubectl describe mcpserver your-server-name -n toolhive-system
```

## TLS 配置

### 方式一：使用 cert-manager（推荐）

```yaml
ingress:
  enabled: true
  host: "mcp.yourdomain.com"
  tls:
    enabled: true
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
```

### 方式二：手动证书

```yaml
# 创建 TLS Secret
apiVersion: v1
kind: Secret
metadata:
  name: mcp-tls-secret
  namespace: toolhive-system
type: kubernetes.io/tls
data:
  tls.crt: <base64-encoded-certificate>
  tls.key: <base64-encoded-private-key>

---
# MCPServer 配置
spec:
  ingress:
    enabled: true
    host: "mcp.yourdomain.com"
    tls:
      enabled: true
      secretName: "mcp-tls-secret"
```

## 故障排查

### 检查 MCPServer 状态
```bash
kubectl get mcpserver -n toolhive-system
kubectl describe mcpserver <server-name> -n toolhive-system
```

### 检查生成的资源
```bash
# 检查 Service
kubectl get svc -n toolhive-system -l toolhive=true

# 检查 Ingress
kubectl get ingress -n toolhive-system

# 检查 Ingress 详情
kubectl describe ingress -n toolhive-system
```

### 检查 URL 状态
MCPServer 的状态中会显示生成的外部访问 URL：

```bash
kubectl get mcpserver <server-name> -n toolhive-system -o jsonpath='{.status.url}'
```

### 常见问题

1. **Ingress 没有外部 IP**
   - 检查 Ingress Controller 是否正常运行
   - 检查 LoadBalancer Service 是否有外部 IP

2. **域名无法访问**
   - 检查 DNS 配置
   - 检查防火墙规则

3. **TLS 证书问题**
   - 检查 cert-manager 是否正常工作
   - 检查证书状态：`kubectl describe certificate -n toolhive-system`

## 迁移现有部署

如果你有现有的 MCPServer 部署，需要：

1. **备份现有配置**
2. **添加 Ingress 配置**到 MCPServer spec
3. **重新应用配置**
4. **更新客户端连接 URL**

```bash
# 获取现有配置
kubectl get mcpserver <server-name> -n toolhive-system -o yaml > backup.yaml

# 编辑配置添加 ingress 部分
# 重新应用
kubectl apply -f updated-config.yaml
```

## 最佳实践

1. **使用 cert-manager** 自动管理 TLS 证书
2. **配置适当的 Ingress annotations** 优化性能和安全性
3. **使用不同的路径** 为不同的 MCPServer 实例
4. **监控 Ingress Controller** 的性能和可用性
5. **配置适当的资源限制** 避免资源耗尽

## 性能优化

### Ingress 注解示例
```yaml
annotations:
  nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
  nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
  nginx.ingress.kubernetes.io/server-snippet: |
    client_max_body_size 100m;
```

### 资源配置
```yaml
resources:
  requests:
    cpu: "100m"
    memory: "128Mi"
  limits:
    cpu: "1000m"
    memory: "1Gi"
```

现在你已经成功将 Toolhive Operator 迁移到支持自建 Kubernetes 集群的 Ingress 架构！ 