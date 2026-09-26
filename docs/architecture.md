# JobPilot v0.1 Architecture

## 1. 范围与不可变约束

JobPilot 是 Kubernetes Job / CronJob 的管理层。Kubernetes 是唯一的调度和执行引擎；JobPilot 重启、升级或不可用不得影响已有 Job/Pod 运行。P0 的 Image Admission Guard 采用 fail-closed，因此其不可用会拒绝新的受管 Job；这是“不得以旧 image 启动任务”的明确取舍。

v0.1 固定为单集群、独立 `jobpilot-controller` Deployment、无数据库、无 Redis/MQ、无 CRD。Controller 同步 Deployment 到关联 CronJob 的 image，并由 Image Admission Guard 拒绝旧 image Job；它不参与 Cron 调度、Job 创建或任务执行。Web/API 可后续独立部署。执行历史是 API 查询时 Kubernetes **仍然存在**的 Job；Job 被 TTL 或 history limit 清理后，JobPilot 不保留副本。

```text
Browser (Vue 3)
  │ HTTPS: REST / SSE, same origin
  ▼
JobPilot Web/API（后续，Go + Gin）
  ├─ OIDC/session + app RBAC + Namespace guard
  ├─ DTO/status/schedule/service layer
  ├─ typed client-go clients
  └─ zap audit/operational logs + /metrics
JobPilot Controller（P0，controller-runtime）
  ├─ Workload Sync Controller (Deployment → CronJob image)
  └─ Image Admission Guard (reject stale Job image)
                     │
                     ▼
Kubernetes API ─── CronJob → Job → Pod → Pod log
```

## 2. 运行时组件和依赖方向

| 组件 | 职责 | 允许依赖 | 不允许承担 |
| --- | --- | --- | --- |
| `web` | 路由、页面、状态展示、操作确认、SSE 消费 | API contract | Kubernetes client、权限判断 |
| `internal/api` | Gin handler、request decode、response/error encode | service、auth context | 业务状态计算、client-go 细节 |
| `internal/auth` | GitLab OIDC、cookie session、CSRF | config | Kubernetes 授权 |
| `internal/rbac` | 角色和 Namespace 授权 | config、identity | HTTP 或 Kubernetes 调用 |
| `internal/service` | 用例编排、DTO、操作审计 | kubernetes、rbac、audit | Gin 类型 |
| `internal/kubernetes` | typed client-go 访问、资源复制、日志 reader | client-go、model | 用户身份/HTTP |
| `internal/controller/workloadsync` | 解析 FollowWorkload、索引 Deployment 引用、收敛 target image、bootstrap suspend、写 Event | controller-runtime、Kubernetes API types、config、zap | HTTP、产品用户身份、Job/Pod 创建、cron 计算 |
| `internal/admission/imageguard` | 校验受管 Job 的 owner CronJob、Deployment source image 与 Job target image | admission API、typed client-go、zap | 修改资源、创建 Job、业务身份 |
| `internal/model` | 纯 DTO、状态、时间、cron 计算 | Kubernetes API types、stdlib、robfig/cron | client/clientset |
| `internal/audit` | 结构化安全操作日志 | zap | 持久化审计库 |

依赖方向只能从 handler 向 service，再向 kubernetes/model。`model` 不依赖 Gin、clientset 或配置 I/O，以便在不启动 Kubernetes 的情况下单测。

## 3. 建议目录

```text
jobpilot/
├── web/
│   └── src/
│       ├── api/          # typed REST/SSE client
│       ├── components/   # StatusBadge, Duration, LogViewer, ConfirmAction
│       ├── layouts/
│       ├── router/
│       ├── stores/       # auth, namespace filter, table state
│       ├── types/        # mirrors public DTO only
│       └── views/        # dashboard, cronjobs, jobs, executions, settings
├── server/
│   ├── cmd/
│   │   ├── controller/main.go
│   │   └── jobpilot/main.go     # 后续 Web/API
│   └── internal/
│       ├── api/{router,handler,response,error}.go
│       ├── auth/{oidc,session,csrf,identity}.go
│       ├── audit/logger.go
│       ├── config/config.go
│       ├── kubernetes/{client,cronjobs,jobs,pods,logs,errors}.go
│       ├── controller/workloadsync/{controller,reconciler,resolver,indexer}.go
│       ├── admission/imageguard/{handler,resolver,response}.go
│       ├── model/{dto,status,schedule,duration,metadata}.go
│       ├── rbac/{policy,authorizer}.go
│       └── service/{dashboard,cronjobs,jobs,executions,logs}.go
├── deploy/helm/
│   ├── jobpilot-controller/
│   └── jobpilot-jobs-catalog/
└── docs/
```

`jobpilot-controller` 入口负责加载 Kubernetes REST config，启动 Workload Sync Controller manager 与 Image Admission Guard，注册健康检查、指标和 Webhook Service，并优雅关闭。Workload Sync 使用 leader election；Image Admission Guard 在两个副本上同时提供服务。后续 Web/API 独立启动 Gin 与 Vue 静态资源，不与 Controller 共用 leader。

## 4. Kubernetes 资源映射

| JobPilot 概念 | Kubernetes 来源 | 读取方式 | 写入方式 |
| --- | --- | --- | --- |
| 任务 | `batch/v1 CronJob` | get/list | API 不创建；Workload Sync 仅更新受管 image |
| Workload 来源 | `apps/v1 Deployment` | get/list/watch | Workload Sync 读取 source container image |
| 执行 | `batch/v1 Job` | get/list，按 owner UID/name 或 JobPilot label 归属 | 从 template 创建 Job |
| 调度执行 | Job 的 `ownerReferences(kind=CronJob, controller=true)` | 归属识别 | 不修改 |
| 手动执行 | `JobTemplate.Spec` + `jobpilot.io/source-cronjob` | 归属识别 | create Job |
| 重试执行 | 当前 CronJob template；若来源已不存在则 sanitized 原 Job spec | 读取 Job/CronJob | create Job |
| Pod | `core/v1 Pod`，`job-name=<job>` | get/list | 不创建、不 exec |
| 日志 | `pods/log` subresource | GET，follow 可选 | 无 |
| 事件 | `core/v1 Event` | Job/Pod 详情按需 list | 无 |

CronJob 对 Job 的关联必须优先按 OwnerReference 的 UID 匹配；只用名称会在 CronJob 被删除并同名重建时串联历史。手动和重试 Job 还必须带 `jobpilot.io/source-cronjob-uid`，来源 CronJob 尚存在时与其 UID 比较；无 UID 或不匹配时只在全局 Job 页保留，不并入新 CronJob 的执行历史。

## 5. 读路径

1. handler 从 session 提取 identity，并通过 `Authorizer` 取得可读 Namespace 集合。
2. service 拒绝未授权的路径 Namespace；全局列表只逐个读取允许的 Namespace，绝不先列全群再过滤。
3. `internal/kubernetes` 使用 `batchv1.CronJobs(ns).List`、`Jobs(ns).List` 与 `Pods(ns).List`，设定调用超时。
4. service 将资源映射为 DTO，再由 `model` 计算状态、时长、下次执行时间和执行来源。
5. 对多个 Namespace 的结果在内存中排序、过滤和分页；每个响应均标注这是查询时快照，不承诺跨资源的事务一致性。

v0.1 使用浏览器 15–30 秒刷新列表、详情 5–10 秒刷新运行态。日志是唯一实时通道。后续若引入 SharedInformerFactory，仍只能作为只读缓存/推送优化，不能成为执行状态的唯一事实来源。

## 6. Workload Sync 路径与保护

Controller 只处理带 `jobpilot.io/follow-workload="true"` 的 CronJob。它读取同 Namespace 的 Deployment 和指定 source container，再仅更新 CronJob JobTemplate 中指定 target container 的 image、一次 bootstrap suspend 与自己的同步状态 Annotation。关联缺失、Deployment 不存在、source/target container 不存在或更新冲突时，Controller 不修改 CronJob，只写 Warning Event 和结构化日志。

Deployment 事件通过 `workload-name` 索引映射到关联 CronJob；CronJob 自身的 Annotation/image 变更也会触发收敛。image 已一致时不得写入资源，防止自身更新形成循环。Controller 不 watch、创建或修改 Job、Pod、Secret、ConfigMap，不使用任何 cron 库。

Deployment PodTemplate 的 image 是 FollowWorkload 的期望版本事实来源。一次 KubeSphere 发布先改变 Deployment 期望 image，再由 Controller 更新 CronJob 模板；运行中的 Job 保持旧 image。Guard 在 Job Create 时以直接 API Reader 比较 Job image 与当前来源 image：一致才放行，不一致则拒绝。Guard 不可用时，`failurePolicy: Fail` 拒绝新的受管 Job；已有 Job/Pod 不受影响。

## 7. 写路径与保护

所有写请求执行相同顺序：认证 → CSRF → 产品角色 → Namespace 授权 → 读取目标资源 → 资源版本/来源校验 → Kubernetes 写入 → 审计日志 → DTO 响应。

| 操作 | 目标 | 关键保护 |
| --- | --- | --- |
| Trigger | CronJob → 新 Job | `GenerateName`、只复制允许的 template metadata、加入来源/操作者 annotation |
| Retry | Job → 新 Job | 只允许已失败的 Job；优先当前来源 CronJob template；清理 server fields |
| Suspend/Resume | CronJob `spec.suspend` | body 提供 `resourceVersion`；冲突后返回 `RESOURCE_VERSION_CONFLICT`，不静默覆盖 |

操作不接受通用 YAML、镜像、命令、ServiceAccount 或 env 覆盖。Trigger/Retry 是非幂等操作，API 客户端不得自动重试；前端在请求发送后禁用按钮，直到收到成功或明确失败。

## 8. 前端信息架构

```text
Overview
Tasks
  ├── CronJobs              /cronjobs
  ├── Jobs                  /jobs
  └── Executions            /cronjobs/:namespace/:name#executions
System
  └── Settings              /settings  (当前用户、可见 Namespace、版本；非配置编辑器)
```

- **Overview**：CronJob 总数、运行 Job、当前可见失败 Job、暂停 CronJob，及最近失败/运行/手动执行列表。统计仅基于当前快照。
- **CronJobs**：跨可见 Namespace 表格，含状态、schedule、timezone、最近执行、duration、next run、镜像策略和动作。FollowWorkload 行显示所属 Deployment、source image 与同步状态。
- **CronJob detail**：概览、执行历史、只读配置摘要与 Workload 关联摘要；执行历史继续下钻 Job detail。UI 不编辑关联关系、任务镜像或 Helm 管理字段。
- **Jobs**：全局执行视图，包含没有父 CronJob 的 Job，但只能对有来源的失败 Job 提供 Retry。
- **Job detail**：状态、Pod 列表、容器选择和日志；不展示 shell、YAML 编辑或 Secret。

每个危险动作使用明确文案确认框：资源名、Namespace、影响说明。Suspend/Resume 与 Trigger/Retry 只在 role 与 Namespace 同时满足时出现；后端仍强制校验。

## 9. 配置和部署

配置从 Helm values/ConfigMap/Secret 注入，不使用运行时数据库：

```yaml
server:
  publicURL: https://jobpilot.example.com
  defaultTimeZone: UTC # 必须与 kube-controller-manager 未设置 spec.timeZone 时的时区一致
oidc:
  issuerURL: https://gitlab.example.com
  clientID: jobpilot
  clientSecretRef: jobpilot-oidc
access:
  namespaceBindings:
    - groups: [wms-developer]
      namespaces: [wms-test, wms-uat]
      role: operator
    - groups: [ops]
      namespaces: ["*"]
      role: admin
  breakGlassBindings:
    - subject: ops-breakglass-1
      namespaces: [wms-prod]
      role: admin
auth:
  breakGlass:
    enabled: false
    credentialsSecretRef: jobpilot-break-glass
workloadSync:
  enabled: true
  managedNamespaces: [wms-test, wms-uat]
  leaderElection:
    enabled: true
    leaseName: jobpilot-workload-sync
```

`jobpilot-controller` 使用两个副本、Webhook Service、ServiceAccount、最小 RBAC、PDB、TLS 配置与健康检查。Workload Sync 只在 `managedNamespaces` 中工作；Guard 只校验带 FollowWorkload 的 Job。Web/API 的 Ingress、OIDC 与 break-glass Secret 后续独立管理。应用配置变更通过 Git/Helm 发布；v0.1 没有“管理员在线编辑 Namespace 授权”功能，避免绕过 GitOps 与无数据库约束。

## 10. 明确不做

不提供 Scheduler、Webhook 调度、任务 DSL、任意 YAML 编辑、资源删除、Pod Exec、终端、Secret/ConfigMap 浏览、任意 API proxy、多集群、持久化审计、长期执行历史、SLA/告警或 CRD。P0 Controller 仅提供 Deployment → CronJob 的受限 image 同步和旧 image Job Admission 拒绝。
