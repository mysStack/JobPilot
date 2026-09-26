# JobPilot v0.1 详细设计

## 1. 核心设计决策

| 决策 | 选择 | 不采用的方案 | 原因 |
| --- | --- | --- | --- |
| 事实来源 | Kubernetes API | PostgreSQL、Redis | Job/CronJob/Pod/日志已经由 Kubernetes 管理；v0.1 不需要长期留存 |
| 调度 | Kubernetes CronJob Controller | JobPilot 自建 Scheduler、robfig/cron 执行器 | JobPilot 不能进入执行链路 |
| K8s 访问 | typed `client-go` | shell 调 `kubectl`、通用 API Proxy | 类型安全、易测、暴露面小 |
| 刷新 | 请求时 List + 前端轮询；日志单独 SSE | 用 controller-runtime 缓存 UI 列表、WebSocket | Workload Sync Controller 使用 controller-runtime，但 UI 读取路径仍保持请求期 List |
| Workload 镜像同步 | 独立 `jobpilot-controller`：Deployment → 关联 CronJob target image；Image Admission Guard 拒绝旧 image Job | 自建 Scheduler、CronJob → Deployment 反向写入、复制完整 PodTemplate | 解决同镜像任务的版本同步；Kubernetes 仍调度/执行 |
| 下次执行 | 服务端 `robfig/cron/v3` | 前端自行解释 cron | 时区与解析规则需要一致 |
| 执行历史 | Kubernetes 当前保留的 Job | 数据库副本 | 历史保留策略由 Kubernetes 决定 |
| 认证 | GitLab OIDC 授权码流程 + 加密 Session | Basic Auth、SPA 保存 Bearer Token | 浏览器不保存可用访问令牌 |

## 2. 文档职责与设计覆盖

`plan.md` 是总需求和路线图；本文件记录跨模块决策。实现契约按职责拆分，避免把计划文档变成模糊的伪设计。

| 设计范围 | 权威文档 |
| --- | --- |
| 系统架构、模块、Go package、Vue 页面/组件、部署 | [architecture.md](architecture.md) |
| REST/SSE 路径、请求响应、分页、幂等性、公开错误 | [api.md](api.md) |
| 产品角色、Namespace 授权、ServiceAccount RBAC | [rbac.md](rbac.md) |
| 参考项目与 License 边界 | [reference-analysis.md](reference-analysis.md) |
| 资源映射、状态、Trigger、Retry、日志、OIDC、审计、配置、测试 | 本文件 |

Vue 页面包括 Overview、CronJob 列表/详情、Job 列表/详情、Execution History、Settings。共享组件包括 `StatusBadge`、`DurationText`、`NamespaceFilter`、`ExecutionTable`、`PodTable`、`ContainerPicker`、`LogViewer`、`ConfirmAction`；只消费 `api.md` 定义的 DTO，不能直接渲染 Kubernetes 原始对象。

## 3. 标识与 JobPilot 元数据

所有资源 URL 用 Kubernetes 的 namespace/name。CronJob 派生关系以 `(namespace, uid)` 为准，不能只按名称关联。

JobPilot 只能写入下列 metadata；必须保留 JobTemplate 的原有 labels/annotations，且不得覆盖 `jobpilot.io/` 之外的键：

```yaml
metadata:
  labels:
    jobpilot.io/source-cronjob: <cronjob name>
    jobpilot.io/source-cronjob-uid: <cronjob UID>
    jobpilot.io/trigger-type: manual | retry
  annotations:
    jobpilot.io/triggered-by: <OIDC preferred_username or subject>
    jobpilot.io/triggered-at: <RFC3339 UTC timestamp>
    jobpilot.io/retry-from: <source Job name> # 仅 retry
```

创建 Job 使用 `GenerateName: <normalized-cronjob-name>-manual-` 或 `-retry-`，禁止固定 `name`。唯一后缀由 API Server 生成；前缀必须截断到 Kubernetes 名称限制以内。

### 3.1 Workload Sync Annotation Contract

FollowWorkload CronJob 由 Helm 声明以下 Annotation，JobPilot API 只读展示，Workload Sync Controller 据此管理 image：

```yaml
metadata:
  annotations:
    jobpilot.io/follow-workload: "true"
    jobpilot.io/workload-kind: "Deployment"
    jobpilot.io/workload-name: "wms"
    jobpilot.io/source-container-name: "wms"
    jobpilot.io/target-container-name: "inventory-sync"
```

MVP 只接受同 Namespace 的 `Deployment`。source/target container 必须分别明确，不能假定在线服务容器名和任务容器名相同。Controller 唯一允许写入 `spec.jobTemplate.spec.template.spec.containers[target].image`、一次 bootstrap `suspend` 解除和自己的同步状态 Annotation；它不创建 CronJob/Job、不修改 Deployment、不复制 env、volume、resources、ServiceAccount 或 Secret。独立 Image Admission Guard 只校验受管 Job 的 image，不修改资源。

Deployment 事件通过 `workload-name` 索引映射到关联 CronJob；CronJob 的 Annotation 或 target image 被修改也触发 reconcile。缺少关联字段、Deployment/source container/target container 不存在、或发生资源版本冲突时不修改资源，只发 Warning Event。image 一致时 no-op，避免 Controller 自己的更新循环。

FollowWorkload 的 source of truth 是 Deployment `spec.template.spec.containers[source].image`，即 KubeSphere 发布后的期望镜像。首次由 Kyverno 生成的 CronJob 必须保持 `suspend: true`，Controller 同步成功后解除 bootstrap suspend。Controller 不等待 Deployment rollout 完成；若发布回滚，下一次 Deployment image 变更会同步回 CronJob。运行中 Job 永远不修改，旧 image Job 在 Admission Guard 中被拒绝，后续同步后的 Job 使用新 image。

## 4. 状态计算规则

### 4.1 Job 状态

公开状态固定为 `Pending`、`Running`、`Success`、`Failed`、`Unknown`，按如下优先级计算：

1. `JobFailed=True` condition → `Failed`；
2. `JobComplete=True` condition → `Success`；
3. `status.active > 0` → `Running`；
4. 有 `status.startTime` 且未终态 → `Running`；
5. 有 creation time，但无 start/终态 → `Pending`；
6. 其他情况 → `Unknown`。

终态 condition 优先于计数器。不能仅因失败 Pod 数大于零就判定 `Failed`，因为 Job 仍可能重试。condition 的 reason/message 仅作为安全摘要；Kubernetes 原始错误只写服务端日志。

`startedAt` 来自 `status.startTime`，仅在排序展示时才回退到 `creationTimestamp`。终态 `completedAt` 来自 `status.completionTime`。`durationSeconds` 为完成时间减开始时间；运行中为当前时间减开始时间；Pending/Unknown 为 `null`。API 一律输出 UTC RFC3339，前端按用户时区格式化。

### 4.2 CronJob 状态

关联到该 CronJob 的可见 Job 按有效开始时间（再按创建时间）倒序。状态优先级为：

1. `spec.suspend=true` → `Suspended`；
2. 任一关联 Job 为 `Running` → `Running`；
3. 没有关联 Job → `NeverRun`；
4. 最新终态 Job → `Success` 或 `Failed`；
5. 其他 → `Unknown`。

`activeJobs` 来自 `status.active` 中仍能关联到的 Job。暂停 CronJob 后已有 Job 不会被终止，因此它可以显示为 `Suspended` 且 `activeJobs > 0`；UI 必须正确表达该含义。

### 4.3 执行归属与触发来源

调度 Job 只有在 controller OwnerReference 的 `kind=CronJob`、`controller=true` 且 UID 匹配时才归属目标 CronJob。手动/重试 Job 必须同时匹配 JobPilot 写入的来源名称和 UID。

| 判定 | `triggerType` | `triggeredBy` |
| --- | --- | --- |
| `jobpilot.io/trigger-type=manual` | `Manual` | `jobpilot.io/triggered-by` |
| `jobpilot.io/trigger-type=retry` | `Retry` | `jobpilot.io/triggered-by` |
| 匹配的 controller OwnerReference | `Schedule` | `system` |
| 无匹配 | `Unknown` | null |

metadata 只用于关联和审计展示，不能用于授予权限或改变角色。

## 5. Cron 与时区

1. `spec.schedule` 不能用 Kubernetes 兼容的五字段 cron parser（含 descriptor）解析时，`nextRun=null`，绝不猜测时间。
2. 有 `spec.timeZone` 时使用 `time.LoadLocation`；无效时仅该 DTO 返回 `INVALID_CRONJOB_TIMEZONE`，不能导致列表整体失败。
3. 无 `spec.timeZone` 时用 `server.defaultTimeZone`。部署必须将它设为 kube-controller-manager 未设置 timezone 时实际使用的时区；Helm 默认 `UTC`。
4. 以 `status.lastScheduleTime` 为锚点；不存在则以 `metadata.creationTimestamp` 为锚点。调用 `schedule.Next(anchor.In(location))`，若结果不晚于 `now` 则逐次推进，最多 10,000 次；超过上限返回 `null` 并告警。
5. 暂停时仍可显示表达式，但 `nextRun=null` 且 `scheduleState="suspended"`，UI 不得承诺会执行。

下次执行是当前 spec 的预览，不承诺 Controller 精确创建时间。`startingDeadlineSeconds`、时钟偏差、Controller 可用性和 `concurrencyPolicy` 都会影响真实行为。v0.1 不计算 missed schedule。

## 6. Trigger 流程

```text
POST trigger
  → 认证 + CSRF + Operator/Admin + Namespace 授权
  → GET CronJob
  → 资源不存在或不可见：404
  → 深拷贝 JobTemplate.Spec 与允许的 template metadata
  → 构造带 GenerateName 和 JobPilot metadata 的 batch/v1 Job
  → CREATE Job
  → 写结构化审计日志
  → 返回 201 execution DTO
```

- CronJob 已暂停时仍允许 Trigger；它创建独立一次性 Job，不修改 `spec.suspend`，确认框必须提示。
- `JobTemplate.Spec` 原样复制；不得修改镜像、命令、env、volume、工作负载身份、ownerReferences、UID、resourceVersion、managedFields 或 status。
- 手动 Job 不设置 CronJob OwnerReference，避免被 Controller 作为调度子任务处理；归属由 JobPilot metadata 识别。
- Trigger 是非幂等操作，前端和 API client 不得自动重试。Create 超时可能已创建 Job，返回 `KUBERNETES_TIMEOUT` 后用户只能刷新检查。

## 7. Retry 流程

Retry 只针对当前计算为 `Failed` 的 Job，且调用者必须是目标 Namespace 的 Operator/Admin：

1. 读取 Job 并重新计算状态；非失败 Job 返回 `JOB_NOT_RETRYABLE`。
2. 通过 owner UID 或 JobPilot 来源 name+UID 查找来源 CronJob。
3. 来源 CronJob 仍匹配时，复制其**当前** `JobTemplate.Spec`，有意使用当前声明的工作负载配置。
4. 来源不存在时深拷贝原 Job `Spec`，并清除 `name`、`generateName`、`namespace`、`uid`、`resourceVersion`、`generation`、时间戳、`managedFields`、`ownerReferences`、`finalizers` 和 status；删除旧 `jobpilot.io/*` 后只保留安全 labels/annotations。
5. 写入 retry metadata 和 `GenerateName`，创建 Job，记录审计，返回 `201`。

响应包含 `retryTemplateSource: "cronjob" | "original-job"`。任何 Retry 都不修改旧失败 Job。

## 8. Suspend / Resume 流程

前端详情页读取 `resourceVersion`，写请求 body 必须带该值。服务端 GET CronJob 并比较版本，然后设置 `spec.suspend` 为目标值，用 Kubernetes optimistic concurrency 执行 Update。版本冲突返回 `409 RESOURCE_VERSION_CONFLICT`，不自动覆盖或重试旧用户意图；前端刷新并要求再次确认。

已暂停的 suspend、已运行的 resume 返回 `200` 与 `changed:false`，并作为 `result:"noop"` 记审计日志。接口是设定状态而不是 toggle，避免竞态。

## 9. Pod 与 SSE 日志

先按 `job-name=<job>` 查询 Pod，再验证其 controller OwnerReference 与目标 Job UID 匹配，防止仅凭 label 碰撞泄露日志。

普通日志最多读取 10,000 行，启用时间戳，必须选择 `container`；仅有一个普通容器时可默认选择。`previous=true` 仅允许已明确选择的容器。init container 日志必须显式选择并验证名称存在。

`GET logs/stream` 不用 WebSocket。它设置 `Content-Type: text/event-stream`，通过 client-go `PodLogOptions{Follow:true,Timestamps:true,Container:...}` 按行读取并发送：

```text
event: log
id: <connection-local line number>
data: <one log line>

event: end
data: {"reason":"pod_terminated"}
```

开始发送 `ready`，每 15 秒发送 `: ping`，在客户端断开、日志 EOF、15 分钟连接上限或 context 超时时关闭。v0.1 不支持 `Last-Event-ID` 回放；重连会重新打开 Kubernetes follow log，可能出现重复行，前端必须明确提示。

## 10. GitLab OIDC、Session 与应急登录

1. `/auth/login` 创建随机 `state`、`nonce`、PKCE verifier，将哈希和同源 return path 放入短时加密 HttpOnly Cookie/Session。
2. 重定向到 GitLab 授权端点，使用 authorization code flow、`openid profile email` scope 和已配置的 group claim。
3. `/auth/callback` 先校验 state，再交换 code；验证 issuer、audience、签名、过期时间、nonce，标准化 subject、username 和允许的 groups。
4. 服务端创建加密签名 Session Cookie，包含身份和授权配置版本。Cookie 使用 `Secure`、`HttpOnly`、`SameSite=Lax`、Path `/`；生产必须 TLS。
5. 每个 API 请求都从部署配置重新计算 role/Namespace grants。Session 有 idle 与 absolute 两个过期时间，且请求期间不向 GitLab 做 token introspection；因此 GitLab 短暂故障不影响已登录用户，直至其本地 Session 自然过期。

OIDC access/refresh token 不暴露给 Vue、不写日志、也不用作 Kubernetes 凭据。GitLab 缺少所需 group claim 且不存在显式 subject/user binding 时，必须 fail closed 并返回 `AUTHORIZATION_DENIED`。

当 GitLab OIDC 不可用且需要新登录时，使用默认关闭的 break-glass provider，而不是引入普通本地账号体系：

- Helm 显式启用 `auth.breakGlass.enabled` 后才注册 `POST /auth/break-glass/login`；未启用时返回 404。
- 最多配置两个固定的应急身份。用户名、Argon2id 密码哈希和 TOTP secret 位于独立 Kubernetes Secret；明文密码与恢复码仅保存于受控密码库，不写入 Git、日志或镜像。
- body 必须包含 `username`、`password`、`otp` 与长度受限的 `reason`；失败统一返回 `401 BREAK_GLASS_AUTH_FAILED`，不枚举账号、密码或 OTP 的失败原因。
- 应急身份通过独立 `breakGlassBindings` 映射到明确 Namespace 和角色，不自动授予 `*` 或无限 Admin；仍然经过产品 RBAC、CSRF、ServiceAccount RBAC 与审计。
- 应急 Session 使用比 OIDC 更短的 idle/absolute 时限；成功、失败、登出都写审计日志，并增加 `jobpilot_break_glass_logins_total` 和安全告警。
- 正常登录页保留 GitLab OIDC 为默认入口；应急入口不因 OIDC 失败自动触发。故障结束后由值班人员禁用 provider、轮换密码/OTP 恢复材料，并完成演练记录。

## 11. Error Model 与审计

所有 JSON 错误格式为 `{ "error": { "code", "message", "details?" }, "requestId" }`。`details` 仅用于字段校验和安全资源标识，绝不包含 kubeconfig、token、Kubernetes 原始响应、OIDC token 或日志正文。每个响应带 `X-Request-ID`。

写操作和 login/logout 产出 zap JSON 审计日志：`timestamp`、`type:"audit"`、`requestId`、`actor.subject`、`actor.username`、`action`、`namespace`、`resource.kind`、`resource.name`、`result`、`errorCode`、`sourceIP`。v0.1 审计落点是平台日志系统；留存和访问控制不由 JobPilot 自己管理。

## 12. Configuration、Helm 与 Metrics

配置只有三类来源：非敏感 Helm values → ConfigMap，OIDC client secret → Secret，in-cluster ServiceAccount token → projected volume。启动时校验 Kubernetes 连接、public URL、OIDC issuer/client ID、Cookie 加密/签名 key、默认时区和 Namespace binding；配置非法时 readiness 失败，只记录字段名，不记录 Secret。

`jobpilot-jobs-catalog` Chart 创建 CronJob Catalog 与 Kyverno Policy；`jobpilot-controller` Chart 创建两个副本的 Controller、Webhook Service、TLS 配置、ServiceAccount、最小 RBAC、PDB 和健康检查。Controller manager 使用 leader election 使只有一个副本执行 Workload Sync reconcile，但两个副本同时提供 Admission Guard。Web/API 可后续独立部署；不得创建 PVC、数据库迁移 Job、CRD 或自建 Scheduler。

`/metrics` 只暴露 JobPilot 自身指标：`jobpilot_http_requests_total`、`jobpilot_http_request_duration_seconds`、`jobpilot_k8s_api_errors_total`、`jobpilot_oidc_failures_total`、`jobpilot_break_glass_logins_total`、`jobpilot_job_trigger_total`、`jobpilot_job_retry_total`。标签必须低基数，例如 method、路由模板、status code、operation/result；不得使用 username、Job/Pod 名、request ID。Kubernetes 工作负载指标仍由 kube-state-metrics 和现有 Prometheus/VictoriaMetrics 负责。

## 13. MVP 里程碑

| 里程碑 | 交付物 | 验收证据 |
| --- | --- | --- |
| M1：骨架 | Go server、嵌入 Vue、Helm、健康检查、typed K8s client | 镜像能对 fake/local cluster 启动，无数据库依赖 |
| M2：读取体验 | Namespace policy、CronJob/Job 列表和详情、状态和下次执行 | Viewer 只看到授权 Namespace 和当前保留历史 |
| M3：执行流 | Trigger、Retry、Pod 列表、日志下载/SSE | Operator 无需 kubectl 即可创建 Job 并查看输出 |
| M4：控制与认证 | GitLab OIDC、CSRF、suspend/resume、审计 | Admin 操作正确处理 resource-version conflict 并记录操作者 |
| M5：P0 控制平面 | Catalog/Kyverno 首次分发、FollowWorkload、Deployment/CronJob watch、image Patch、bootstrap suspend、Admission Guard、Event、leader election | Deployment 更新后多个关联 CronJob 收敛；旧 image Job 被拒绝；运行 Job 不变；错误关联不写资源 |
| M6：生产就绪 | metrics、测试、Helm RBAC、错误体验 | 临时集群 E2E 与最小 RBAC 评审通过 |

M1–M5 顺序执行；监控/SLA、长期历史、用户在线编辑授权、多集群均明确排除在 v0.1 之外。

## 14. 测试策略

- unit：DTO mapping、OwnerReference 匹配、状态优先级、duration、cron timezone/非法输入、metadata sanitizer、Namespace matcher、Workload annotation 解析与 source/target container resolver；
- service：fake clientset 下的 Trigger/Retry/Suspend 错误映射与 audit event；
- API：`httptest` 校验 Cookie/auth/CSRF/role/Namespace guard、response schema、404 非披露；
- logs：`httptest` stream 产生 ready/log/end，且客户端取消时关闭；
- frontend：表格过滤/状态标签、操作可见性、确认 payload、SSE reconnect 提示；
- controller：fake client/envtest 覆盖 Deployment image 变更、多个 CronJob 映射、Annotation 缺失、容器缺失、no-op、资源版本冲突、bootstrap suspend、leader election 以及 Controller 不改 Job/Pod/非受管字段。
- admission：覆盖非受管 Job 放行、同步 image 放行、旧 image 拒绝、来源资源缺失拒绝和 `failurePolicy: Fail`。
- e2e：临时 kind/k3d 集群 + OIDC test provider，覆盖 Kyverno 首次生成 → image 同步 → 旧 image Job 被拒绝 → 新 image Job 放行，再覆盖 CronJob list → Trigger → Job/Pod → logs → failure retry → suspend/resume。

禁止任何测试依赖生产 Kubernetes 集群或真实 GitLab tenant。
