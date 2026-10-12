# ToolHive Operator Helm Chart

![Version: 0.51.4](https://img.shields.io/badge/Version-0.51.4-informational?style=flat-square)
![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)

A Helm chart for deploying the ToolHive Operator into Kubernetes.

---

## TL;DR

```console
helm upgrade -i toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator -n toolhive-system --create-namespace
```

## Prerequisites

- Kubernetes 1.25+
- Helm 3.10+ (3.14+ recommended) or Helm 4

## Usage

### Installing from the Chart

Install one of the available versions:

```shell
helm upgrade -i <release_name> oci://ghcr.io/stacklok/toolhive/toolhive-operator --version=<version> -n toolhive-system --create-namespace
```

> **Tip**: List all releases using `helm list`

### Uninstalling the Chart

To uninstall/delete the `toolhive-operator` deployment:

```console
helm uninstall <release_name>
```

The command removes all the Kubernetes components associated with the chart and deletes the release. You will have to delete the namespace manually if you used Helm to create it.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| fullnameOverride | string | `"toolhive-operator"` | Override the generated name of the operator Deployment and related resources. |
| nameOverride | string | `""` | Override the chart name used in resource labels and generated names. |
| operator | object | `{"affinity":{},"autoscaling":{"enabled":false,"maxReplicas":100,"minReplicas":1,"targetCPUUtilizationPercentage":80},"containerSecurityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}},"defaultImagePullSecrets":[],"defaultRedis":{"addr":"","existingSecret":"","existingSecretKey":""},"env":[],"features":{"experimental":false,"storageVersionMigrator":true},"gc":{"gogc":75,"gomemlimit":"110MiB"},"image":"ghcr.io/stacklok/toolhive/operator:v0.51.4","imageDiscovery":{"enabled":false,"resources":{}},"imagePullPolicy":"IfNotPresent","imagePullSecrets":[],"leaderElectionRole":{"binding":{"name":"toolhive-operator-leader-election-rolebinding"},"name":"toolhive-operator-leader-election-role","rules":[{"apiGroups":[""],"resources":["configmaps"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":["coordination.k8s.io"],"resources":["leases"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":["events.k8s.io"],"resources":["events"],"verbs":["create","patch"]}]},"livenessProbe":{"httpGet":{"path":"/healthz","port":"health"},"initialDelaySeconds":15,"periodSeconds":20},"nodeSelector":{},"podAnnotations":{},"podLabels":{},"podSecurityContext":{"runAsNonRoot":true},"ports":[{"containerPort":8080,"name":"metrics","protocol":"TCP"},{"containerPort":8081,"name":"health","protocol":"TCP"}],"proxyHost":"0.0.0.0","rbac":{"allowedNamespaces":[],"scope":"cluster"},"readinessProbe":{"httpGet":{"path":"/readyz","port":"health"},"initialDelaySeconds":5,"periodSeconds":10},"replicaCount":1,"resources":{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}},"serviceAccount":{"annotations":{},"automountServiceAccountToken":true,"create":true,"labels":{},"name":"toolhive-operator"},"tolerations":[],"toolhiveRunnerImage":"ghcr.io/stacklok/toolhive/proxyrunner:v0.51.4","vmcpImage":"ghcr.io/stacklok/toolhive/vmcp:v0.51.4","volumeMounts":[],"volumes":[]}` | Settings for the operator Deployment and related resources. |
| operator.affinity | object | `{}` | Affinity rules for scheduling the operator pod. |
| operator.autoscaling | object | `{"enabled":false,"maxReplicas":100,"minReplicas":1,"targetCPUUtilizationPercentage":80}` | Horizontal Pod Autoscaler settings for the operator Deployment. |
| operator.autoscaling.enabled | bool | `false` | Create a Horizontal Pod Autoscaler for the operator Deployment. When enabled, the chart omits `replicaCount` from the Deployment. |
| operator.autoscaling.maxReplicas | int | `100` | Maximum number of operator replicas when autoscaling is enabled. |
| operator.autoscaling.minReplicas | int | `1` | Minimum number of operator replicas when autoscaling is enabled. |
| operator.autoscaling.targetCPUUtilizationPercentage | int | `80` | Target average CPU utilization as a percentage of requested CPU for autoscaling. |
| operator.containerSecurityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}}` | Security context for the operator container and image-discovery containers. |
| operator.defaultImagePullSecrets | list | `[]` | Default image pull Secrets for workloads created by the operator. Accepts Secret names or objects with a `name` field. Entries are merged with each resource's `imagePullSecrets`, with resource-level entries taking precedence for duplicate names. Secrets must exist in each workload's namespace. |
| operator.defaultRedis | object | `{"addr":"","existingSecret":"","existingSecretKey":""}` | Default Redis or Valkey session storage settings for workloads without `spec.sessionStorage` configured. |
| operator.defaultRedis.addr | string | `""` | Redis or Valkey address in `host:port` format. When empty, uses `global.redis.host` and `global.redis.port` (default `6379`) from a parent chart. No default session storage is configured when neither address is set. |
| operator.defaultRedis.existingSecret | string | `""` | Name of an existing Secret containing the Redis password. When empty, uses `global.redis.existingSecret`. The Secret must exist in each workload's namespace. Used only when a Redis address is configured. |
| operator.defaultRedis.existingSecretKey | string | `""` | Key containing the password in `existingSecret`. When empty, uses `global.redis.existingSecretKey`, then `redis-password`. |
| operator.env | list | `[]` | Additional environment variables for the operator container. Chart-managed variables take precedence for duplicate names. Set `TOOLHIVE_SKIP_UPDATE_CHECK` to `"true"` to disable periodic update checks and the usage metrics collected with them. |
| operator.features.experimental | bool | `false` | Enable experimental operator features. |
| operator.features.storageVersionMigrator | bool | `true` | Enable the storage version migration controller to rewrite resources in the current storage version and remove obsolete entries from CRD `status.storedVersions`. Applies to ToolHive CRDs with the `toolhive.stacklok.dev/auto-migrate-storage-version` label set to `"true"`. Requires `operator.rbac.scope=cluster`; set this to `false` for namespace-scoped installations. |
| operator.gc | object | `{"gogc":75,"gomemlimit":"110MiB"}` | Go runtime memory and garbage collection settings for the operator container. |
| operator.gc.gogc | int | `75` | Target heap growth percentage between garbage collections, supplied as `GOGC`. The Go runtime default is `100`. |
| operator.gc.gomemlimit | string | `"110MiB"` | Go runtime soft memory limit, supplied as `GOMEMLIMIT`. |
| operator.image | string | `"ghcr.io/stacklok/toolhive/operator:v0.51.4"` | Container image for the operator. |
| operator.imageDiscovery | object | `{"enabled":false,"resources":{}}` | Image discovery settings for manifest scanners used to mirror images into air-gapped environments. |
| operator.imageDiscovery.enabled | bool | `false` | Render a zero-replica Deployment with `image:` entries for `operator.toolhiveRunnerImage`, `operator.vmcpImage`, and `registryAPI.image`. This lets manifest scanners discover images otherwise supplied to the operator through environment variables. The Deployment runs no pods. |
| operator.imageDiscovery.resources | object | `{}` | Resource requests and limits for image-discovery containers. Admission policies can require these values even when the Deployment has zero replicas. |
| operator.imagePullPolicy | string | `"IfNotPresent"` | Image pull policy for the operator container and image-discovery containers. |
| operator.imagePullSecrets | list | `[]` | Image pull Secrets for the operator pod, as objects with a `name` field. Secrets must exist in the release namespace. |
| operator.leaderElectionRole | object | `{"binding":{"name":"toolhive-operator-leader-election-rolebinding"},"name":"toolhive-operator-leader-election-role","rules":[{"apiGroups":[""],"resources":["configmaps"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":["coordination.k8s.io"],"resources":["leases"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":["events.k8s.io"],"resources":["events"],"verbs":["create","patch"]}]}` | Namespaced Role and RoleBinding settings for operator leader election. |
| operator.leaderElectionRole.binding.name | string | `"toolhive-operator-leader-election-rolebinding"` | Name of the RoleBinding used for operator leader election. |
| operator.leaderElectionRole.name | string | `"toolhive-operator-leader-election-role"` | Name of the Role used for operator leader election. |
| operator.leaderElectionRole.rules | list | `[{"apiGroups":[""],"resources":["configmaps"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":["coordination.k8s.io"],"resources":["leases"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":["events.k8s.io"],"resources":["events"],"verbs":["create","patch"]}]` | Permission rules for the operator leader election Role. |
| operator.livenessProbe | object | `{"httpGet":{"path":"/healthz","port":"health"},"initialDelaySeconds":15,"periodSeconds":20}` | Liveness probe for the operator container. The default probe uses the named `health` port. |
| operator.nodeSelector | object | `{}` | Node selector for scheduling the operator pod. |
| operator.podAnnotations | object | `{}` | Annotations to add to the operator pod. |
| operator.podLabels | object | `{}` | Labels to add to the operator pod. |
| operator.podSecurityContext | object | `{"runAsNonRoot":true}` | Security context for the operator pod and image-discovery pod template. |
| operator.ports | list | `[{"containerPort":8080,"name":"metrics","protocol":"TCP"},{"containerPort":8081,"name":"health","protocol":"TCP"}]` | Ports declared on the operator container. The default `metrics` and `health` names identify the metrics and health-check ports. |
| operator.proxyHost | string | `"0.0.0.0"` | Bind address for proxy runners in managed MCPServer and MCPRemoteProxy workloads. |
| operator.rbac | object | `{"allowedNamespaces":[],"scope":"cluster"}` | Role-based access control (RBAC) settings for the operator. |
| operator.rbac.allowedNamespaces | list | `[]` | Namespaces to watch and manage when `scope` is `namespace`. Each namespace receives a RoleBinding to the operator ClusterRole. |
| operator.rbac.scope | string | `"cluster"` | Permission scope for the operator. `cluster` creates a ClusterRoleBinding for cluster-wide access. `namespace` creates a RoleBinding to the operator ClusterRole in each entry of `allowedNamespaces` and limits watches to those namespaces. Namespace scope requires `operator.features.storageVersionMigrator=false`. |
| operator.readinessProbe | object | `{"httpGet":{"path":"/readyz","port":"health"},"initialDelaySeconds":5,"periodSeconds":10}` | Readiness probe for the operator container. The default probe uses the named `health` port. |
| operator.replicaCount | int | `1` | Number of operator replicas when autoscaling is disabled. Leader election selects one active controller replica. |
| operator.resources | object | `{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | Resource requests and limits for the operator container. |
| operator.serviceAccount | object | `{"annotations":{},"automountServiceAccountToken":true,"create":true,"labels":{},"name":"toolhive-operator"}` | ServiceAccount settings for the operator. |
| operator.serviceAccount.annotations | object | `{}` | Annotations to add to the chart-created ServiceAccount. |
| operator.serviceAccount.automountServiceAccountToken | bool | `true` | Automatically mount Kubernetes API credentials for pods using the chart-created ServiceAccount. Used only when `create` is `true`. |
| operator.serviceAccount.create | bool | `true` | Create the operator ServiceAccount. |
| operator.serviceAccount.labels | object | `{}` | Labels to add to the chart-created ServiceAccount. |
| operator.serviceAccount.name | string | `"toolhive-operator"` | Name of the ServiceAccount used by the operator. When empty, uses the generated resource name if `create` is `true`, or the namespace's `default` ServiceAccount if `create` is `false`. |
| operator.tolerations | list | `[]` | Tolerations for scheduling the operator pod. |
| operator.toolhiveRunnerImage | string | `"ghcr.io/stacklok/toolhive/proxyrunner:v0.51.4"` | Container image for ToolHive proxy runners created by the operator. |
| operator.vmcpImage | string | `"ghcr.io/stacklok/toolhive/vmcp:v0.51.4"` | Container image for Virtual MCP Server (vMCP) deployments created by the operator. |
| operator.volumeMounts | list | `[]` | Additional volume mounts for the operator container. |
| operator.volumes | list | `[]` | Additional volumes for the operator pod. |
| registryAPI | object | `{"image":"ghcr.io/stacklok/thv-registry-api:v1.5.2"}` | Settings for the registry API workloads created by the operator. |
| registryAPI.image | string | `"ghcr.io/stacklok/thv-registry-api:v1.5.2"` | Container image for registry API workloads created by the operator. |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on contributing to this chart.

