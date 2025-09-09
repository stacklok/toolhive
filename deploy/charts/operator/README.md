# ToolHive Operator Helm Chart

![Version: 0.2.9](https://img.shields.io/badge/Version-0.2.9-informational?style=flat-square)
![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)

A Helm chart for deploying the ToolHive Operator into Kubernetes.

---

## TL;DR

```console
helm upgrade -i toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator -n toolhive-system --create-namespace
```

Or for a custom values file:

```consoleCustom
helm upgrade -i toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator -n toolhive-system --create-namespace --values values-openshift.yaml
```

## Prerequisites

- Kubernetes 1.25+
- Helm 3.10+ minimum, 3.14+ recommended

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
| fullnameOverride | string | `"toolhive-operator"` | Provide a fully-qualified name override for resources |
| nameOverride | string | `""` | Override the name of the chart |
| operator | object | `{"affinity":{},"autoscaling":{"enabled":false,"maxReplicas":100,"minReplicas":1,"targetCPUUtilizationPercentage":80},"containerSecurityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true,"runAsUser":1000},"env":{},"features":{"experimental":false},"image":"ghcr.io/stacklok/toolhive/operator:v0.2.9","imagePullPolicy":"IfNotPresent","imagePullSecrets":[],"leaderElectionRole":{"binding":{"name":"toolhive-operator-leader-election-rolebinding"},"name":"toolhive-operator-leader-election-role","rules":[{"apiGroups":[""],"resources":["configmaps"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":["coordination.k8s.io"],"resources":["leases"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":[""],"resources":["events"],"verbs":["create","patch"]}]},"livenessProbe":{"httpGet":{"path":"/healthz","port":"health"},"initialDelaySeconds":15,"periodSeconds":20},"nodeSelector":{},"podAnnotations":{},"podLabels":{},"podSecurityContext":{"runAsNonRoot":true},"ports":[{"containerPort":8080,"name":"metrics","protocol":"TCP"},{"containerPort":8081,"name":"health","protocol":"TCP"}],"proxyHost":"0.0.0.0","rbac":{"allowedNamespaces":[],"scope":"cluster"},"readinessProbe":{"httpGet":{"path":"/readyz","port":"health"},"initialDelaySeconds":5,"periodSeconds":10},"replicaCount":1,"resources":{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}},"serviceAccount":{"annotations":{},"automountServiceAccountToken":true,"create":true,"labels":{},"name":"toolhive-operator"},"tolerations":[],"toolhiveRunnerImage":"ghcr.io/stacklok/toolhive/proxyrunner:v0.2.9","volumeMounts":[],"volumes":[]}` | All values for the operator deployment and associated resources |
| operator.affinity | object | `{}` | Affinity settings for the operator pod |
| operator.autoscaling | object | `{"enabled":false,"maxReplicas":100,"minReplicas":1,"targetCPUUtilizationPercentage":80}` | Configuration for horizontal pod autoscaling |
| operator.autoscaling.enabled | bool | `false` | Enable autoscaling for the operator |
| operator.autoscaling.maxReplicas | int | `100` | Maximum number of replicas |
| operator.autoscaling.minReplicas | int | `1` | Minimum number of replicas |
| operator.autoscaling.targetCPUUtilizationPercentage | int | `80` | Target CPU utilization percentage for autoscaling |
| operator.containerSecurityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true,"runAsUser":1000}` | Container security context settings for the operator |
| operator.env | object | `{}` | Environment variables to set in the operator container |
| operator.image | string | `"ghcr.io/stacklok/toolhive/operator:v0.2.9"` | Container image for the operator |
| operator.imagePullPolicy | string | `"IfNotPresent"` | Image pull policy for the operator container |
| operator.imagePullSecrets | list | `[]` | List of image pull secrets to use |
| operator.leaderElectionRole | object | `{"binding":{"name":"toolhive-operator-leader-election-rolebinding"},"name":"toolhive-operator-leader-election-role","rules":[{"apiGroups":[""],"resources":["configmaps"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":["coordination.k8s.io"],"resources":["leases"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":[""],"resources":["events"],"verbs":["create","patch"]}]}` | Leader election role configuration |
| operator.leaderElectionRole.binding.name | string | `"toolhive-operator-leader-election-rolebinding"` | Name of the role binding for leader election |
| operator.leaderElectionRole.name | string | `"toolhive-operator-leader-election-role"` | Name of the role for leader election |
| operator.leaderElectionRole.rules | list | `[{"apiGroups":[""],"resources":["configmaps"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":["coordination.k8s.io"],"resources":["leases"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":[""],"resources":["events"],"verbs":["create","patch"]}]` | Rules for the leader election role |
| operator.livenessProbe | object | `{"httpGet":{"path":"/healthz","port":"health"},"initialDelaySeconds":15,"periodSeconds":20}` | Liveness probe configuration for the operator |
| operator.nodeSelector | object | `{}` | Node selector for the operator pod |
| operator.podAnnotations | object | `{}` | Annotations to add to the operator pod |
| operator.podLabels | object | `{}` | Labels to add to the operator pod |
| operator.podSecurityContext | object | `{"runAsNonRoot":true}` | Pod security context settings |
| operator.ports | list | `[{"containerPort":8080,"name":"metrics","protocol":"TCP"},{"containerPort":8081,"name":"health","protocol":"TCP"}]` | List of ports to expose from the operator container |
| operator.proxyHost | string | `"0.0.0.0"` | Host for the proxy deployed by the operator |
| operator.rbac | object | `{"allowedNamespaces":[],"scope":"cluster"}` | RBAC configuration for the operator |
| operator.rbac.allowedNamespaces | list | `[]` | List of namespaces that the operator is allowed to have permissions to manage. Only used if scope is set to "namespace". |
| operator.rbac.scope | string | `"cluster"` | Scope of the RBAC configuration. - cluster: The operator will have cluster-wide permissions via ClusterRole and ClusterRoleBinding. - namespace: The operator will have permissions to manage resources in the namespaces specified in `allowedNamespaces`.   The operator will have a ClusterRole and RoleBinding for each namespace in `allowedNamespaces`. |
| operator.readinessProbe | object | `{"httpGet":{"path":"/readyz","port":"health"},"initialDelaySeconds":5,"periodSeconds":10}` | Readiness probe configuration for the operator |
| operator.replicaCount | int | `1` | Number of replicas for the operator deployment |
| operator.resources | object | `{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | Resource requests and limits for the operator container |
| operator.serviceAccount | object | `{"annotations":{},"automountServiceAccountToken":true,"create":true,"labels":{},"name":"toolhive-operator"}` | Service account configuration for the operator |
| operator.serviceAccount.annotations | object | `{}` | Annotations to add to the service account |
| operator.serviceAccount.automountServiceAccountToken | bool | `true` | Automatically mount a ServiceAccount's API credentials |
| operator.serviceAccount.create | bool | `true` | Specifies whether a service account should be created |
| operator.serviceAccount.labels | object | `{}` | Labels to add to the service account |
| operator.serviceAccount.name | string | `"toolhive-operator"` | The name of the service account to use. If not set and create is true, a name is generated. |
| operator.tolerations | list | `[]` | Tolerations for the operator pod |
| operator.toolhiveRunnerImage | string | `"ghcr.io/stacklok/toolhive/proxyrunner:v0.2.9"` | Image to use for Toolhive runners |
| operator.volumeMounts | list | `[]` | Additional volume mounts on the operator container |
| operator.volumes | list | `[]` | Additional volumes to mount on the operator pod |

