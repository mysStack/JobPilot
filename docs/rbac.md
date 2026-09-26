# JobPilot 授权与 Kubernetes RBAC

## 1. 两道独立授权门禁

JobPilot 对每个请求依次执行两道授权：

```text
GitLab OIDC 身份
  → JobPilot 产品角色与 Namespace 授权（每个请求）
  → JobPilot ServiceAccount RBAC
  → Kubernetes API
```

ServiceAccount 是后端服务凭据，权限可能高于单个 Viewer；它绝不等同于登录用户权限。包括列表接口和 SSE 在内的所有资源访问，都必须经过应用级 Namespace 授权。

`jobpilot-controller` 使用独立 ServiceAccount，不承载 Web/API 用户身份，也不绕过产品角色；它只在配置允许的 managed Namespace 中读取 Deployment，并更新带 `jobpilot.io/follow-workload="true"` 的 CronJob target image。Image Admission Guard 使用同一 Controller ServiceAccount 只读校验 CronJob/Deployment；Controller 权限与登录用户的产品权限是两条独立边界。

## 2. 产品角色

角色从低到高为 `viewer < operator < admin`。同一身份可以匹配多条绑定；在同一个 Namespace 内取最高角色。通配绑定只在显式绑定之后参与匹配，但不会自动提升角色。

| 能力 | Viewer | Operator | Admin |
| --- | :---: | :---: | :---: |
| 查看 CronJob、Job、执行历史、Pod、Event | 是 | 是 | 是 |
| 查看、下载、实时流式查看 Pod 日志 | 是 | 是 | 是 |
| 手动触发 CronJob | 否 | 是 | 是 |
| 重跑失败 Job | 否 | 是 | 是 |
| 暂停 CronJob | 否 | 否 | 是 |
| 恢复 CronJob | 否 | 否 | 是 |
| 查看产品审计日志 | 否 | 否 | 仅通过平台日志系统；v0.1 无审计 API |
| 在线管理 Namespace 绑定 | 否 | 否 | 否；v0.1 由 Git/Helm 管理 |

`admin` 不包含删除、YAML 编辑、Pod Exec、Secret 读取、任意 Kubernetes 代理，或从非授权模板创建任意工作负载的权限。

## 3. Namespace 授权策略

Namespace 策略由 Helm ConfigMap 提供，经 GitOps 评审。应用启动时读取，策略变更通过重新部署或重启生效：

```yaml
access:
  namespaceBindings:
    - groups: [wms-developer]
      namespaces: [wms-test, wms-uat]
      role: operator
    - groups: [wms-observer]
      namespaces: [wms-prod]
      role: viewer
    - groups: [ops]
      namespaces: ["*"]
      role: admin
```

匹配规则：

1. Group 字符串按配置精确匹配；不得通过显示名或邮箱域名推断成员身份。
2. URL 中的 Namespace 必须命中显式授权 Namespace 或 `*`。
3. 无任何绑定的用户在 `/me` 中可看到自己的身份和空授权集合；集合资源返回空列表，指定资源返回 `404`，避免泄露资源是否存在。
4. 跨 Namespace 列表只枚举已授权 Namespace，不能先全群 `list` 再在内存中过滤。
5. `GET /api/v1/namespaces` 只返回配置允许且 ServiceAccount 当前可发现的 Namespace 交集。

应急身份不使用 GitLab group claim，必须使用单独、显式的 `breakGlassBindings`。示例中的 `ops-breakglass-1` 只在 SSO 故障时使用；生产环境应按职责限定 Namespace，不能默认使用 `*`：

```yaml
access:
  breakGlassBindings:
    - subject: ops-breakglass-1
      namespaces: [wms-prod, report]
      role: admin
```

break-glass provider 只跳过外部 OIDC，不跳过本表、Namespace policy、CSRF、资源归属检查或 Kubernetes RBAC。

## 4. 接口授权规则

| 接口类别 | 最低角色 | Namespace 处理 |
| --- | --- | --- |
| `/me`、健康检查 | 已登录／健康检查无需登录 | 无资源 Namespace |
| `/namespaces`、`/cronjobs`、`/jobs` 列表 | Viewer | 仅枚举授权范围 |
| 指定 CronJob、Job、Pod、日志接口 | Viewer | 读取 Kubernetes 前先校验路径 Namespace |
| `trigger`、`retry` | Operator | 目标 Namespace 内为 Operator 或 Admin |
| `suspend`、`resume` | Admin | 目标 Namespace 内必须为 Admin |

handler 在加载指定资源前，必须调用统一的 `RequireNamespace(ctx, namespace, minimumRole)`。service 同时接收已授权的 Namespace 范围，防止内部调用意外绕过门禁。

## 5. Kubernetes ServiceAccount 权限

当授权范围是少量固定 Namespace 时，使用每个 Namespace 的 Role/RoleBinding；只有确有动态或集群级授权范围时才使用 ClusterRole/ClusterRoleBinding。无论哪种模式，均不得授予 `delete`、`exec`、Secret、ConfigMap、通配资源或通配动词。

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: jobpilot
rules:
  - apiGroups: [batch]
    resources: [cronjobs]
    verbs: [get, list, watch, update, patch]
  - apiGroups: [apps]
    resources: [deployments]
    verbs: [get, list, watch]
  - apiGroups: [batch]
    resources: [jobs]
    verbs: [get, list, watch, create]
  - apiGroups: [""]
    resources: [pods]
    verbs: [get, list, watch]
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
  - apiGroups: [""]
    resources: [events]
    verbs: [get, list, watch, create, patch]
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, list, watch, create, update, patch]
  - apiGroups: [""]
    resources: [namespaces]
    verbs: [get, list]
```

`update cronjobs` 用于带 `resourceVersion` 的 suspend/resume。`patch cronjobs` 仅供 Workload Sync Controller 局部更新受管 target container 的 image、一次 bootstrap suspend 与自己的同步状态 Annotation，禁止用于 API 的通用资源编辑。Controller 只能在 `workloadSync.managedNamespaces` 对满足 FollowWorkload Annotation 的 CronJob 调用该能力；实现中必须再次校验 Annotation、Namespace 和唯一可写 JSON path，RBAC 本身不能表达这些条件。

`deployments` 只读权限用于获取 source container image；Controller 不拥有 Deployment 的 patch/update 权限。`leases` 仅用于 leader election，`events` 的 create/patch 仅用于报告同步成功或关联错误。`jobpilot-controller` 与 Web/API 已经是不同 Deployment，不能复用 Web/API ServiceAccount。Webhook 的 `ValidatingWebhookConfiguration` 由 Helm 安装身份管理，Controller 运行时 ServiceAccount 不拥有其写权限。

ServiceAccount 使用 projected token；如果集群支持，应采用短期令牌。它只挂载在 JobPilot Pod 中。`automountServiceAccountToken: true` 是 client-go 所需配置，其他工作负载不得复用此 ServiceAccount。

## 6. 日志与事件隐私

日志可能含业务数据。Viewer 的日志权限只能授予本就允许查看该工作负载运行输出的人员。日志请求按以下顺序校验：Namespace 角色 → Pod 存在 → Pod 归属指定 Job → 所选 Container 存在。不得仅凭用户知道 Pod 名称就允许读取日志。

事件仅返回同一 Job/Pod 的相关 Event，不返回整个 Namespace 的事件。v0.1 不对应用日志内容做脱敏；如有组织级脱敏要求，应在集群日志管道中单独设计并启用。

## 7. 认证失败与可观测性

- 缺失或过期 Session：`401 AUTHENTICATION_REQUIRED`。
- Session 有效但缺少角色/Namespace：集合资源过滤；指定资源使用 `404 RESOURCE_NOT_FOUND`；对用户可见资源的越权写操作返回 `403 AUTHORIZATION_DENIED`。
- Cookie 鉴权的写请求未通过 CSRF：`403 CSRF_VALIDATION_FAILED`。
- Kubernetes 拒绝 ServiceAccount：`502 KUBERNETES_FORBIDDEN`，附 request ID；不得误报为用户没有权限。

认证决策、被拒绝的写操作和成功写操作都输出结构化审计日志。记录用户 subject 与配置角色，不记录 access token、session cookie 或 Authorization header。
