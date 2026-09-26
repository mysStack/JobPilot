# JobPilot 项目计划

## 1. 项目名称

**JobPilot**

英文定位：

> Kubernetes Job & CronJob Management Center

中文定位：

> Kubernetes 原生任务管理中心

JobPilot 用于替代现有 XXL-Job 的任务管理体验，但不重新实现一套任务调度器。

核心原则：

> Kubernetes 负责调度与执行，JobPilot 负责管理、查看、操作和审计。

---

# 2. 项目背景

现有业务使用 XXL-Job 管理定时任务。

团队决定逐步迁移为 Kubernetes 原生：

```text
CronJob
   ↓
Job
   ↓
Pod
```

Kubernetes 已经能够完成：

* Cron 调度
* Job 生命周期管理
* Pod 调度
* 重试
* 并发控制
* 超时控制
* 失败状态
* 日志输出

因此不再需要额外维护：

```text
XXL-Job Admin
XXL-Job Database
Executor 注册
Executor 心跳
调度 RPC
调度中心
```

但 Kubernetes / KubeSphere 原生 Job UI 更偏 Kubernetes 资源管理，对于开发人员日常使用不够友好。

因此开发 JobPilot。

---

# 开发前置：参考项目调研

在开始 JobPilot develop-design 之前，必须先调研以下开源项目。调研不是可选项，也不能只停留在项目名称或 README；必须阅读 README、License，以及与 JobPilot 能力直接相关的目录和实现。

## 调研优先级

1. **kubernetes-job-ui**（最高）
   Repository: https://github.com/lhopki01/kubernetes-job-ui
   深挖 Go + Gin 项目结构、client-go 使用、CronJob → Job 手动触发、CronJob JobTemplate 转换、Job 执行历史、参数化执行、Kubernetes API 封装与 `internal/k8s` 模块。JobPilot 借鉴其 Kubernetes 操作逻辑；不得沿用旧 React 前端。
2. **Kronic**（高）
   Repository: https://github.com/mshade/kronic
   研究 CronJob 管理 UI、多 Namespace 任务展示、Trigger、Suspend / Resume、CronJob → Job → Pod 页面下钻、Job History 与任务操作交互。JobPilot 借鉴产品设计与 UI/UX；不得采用 Flask + AlpineJS 技术栈。
3. **CronJob Guardian**（高）
   Repository: https://github.com/iLLeniumStudios/cronjob-guardian
   研究 Go 项目分层、controller-runtime、CronJob / Job 状态分析、analyzer、metrics、missed schedule、duration、SLA 与 failure detection。它是后续监控和状态分析的设计参考；v0.1 不要求完整实现其监控能力。
4. **Onax**（中）
   Repository: https://github.com/varaxlabs/onax
   研究 client-go、controller-runtime、robfig/cron、Prometheus metrics、next schedule、active jobs、missed schedules 和 success rate，用于后续 Metrics 与 Cron 计算设计参考。
5. **kubecron**（中）
   Repository: https://github.com/danielec7/kubecron
   研究 client-go 的轻量实现，以及 CronJob run、suspend、unsuspend，作为 Trigger / Suspend / Resume 的实现参考。

## 参考项目使用原则

JobPilot 不直接 Fork 任意一个项目作为基础。采用组合式借鉴：

```text
kubernetes-job-ui  → 后端 Kubernetes 操作逻辑
Kronic             → UI / UX 和任务管理流程
CronJob Guardian   → 状态分析和监控架构
Onax               → Prometheus Metrics 和 Cron 计算
kubecron           → Trigger / Suspend / Resume 的轻量实现
```

直接复用源码前必须核对 License；默认方式是理解设计后重新实现。

---

# 3. JobPilot 产品定位

JobPilot 不是：

```text
Kubernetes Dashboard
KubeSphere 替代品
XXL-Job Scheduler
Workflow Engine
CI/CD 平台
```

JobPilot 是：

```text
Kubernetes Job / CronJob
        ↓
业务友好的任务管理 UI
```

开发人员看到：

```text
库存同步
订单超时任务
每日结算
数据同步
报表生成
```

而不是首先看到：

```text
Pod
ReplicaSet
Deployment
Container
YAML
```

---

# 4. 核心架构原则

必须遵守以下原则。

## 4.1 Kubernetes 是唯一执行引擎

JobPilot 不实现 Scheduler。

Cron 调度：

```text
Kubernetes CronJob Controller
```

任务执行：

```text
Kubernetes Job Controller
```

容器执行：

```text
Kubernetes Pod
```

JobPilot 仅通过 Kubernetes API 操作资源。

---

## 4.2 v0.1 不使用数据库

第一版本不引入：

```text
MySQL
PostgreSQL
Redis
MQ
ElasticSearch
```

数据来源全部来自 Kubernetes API。

例如：

```text
CronJob
Job
Pod
Pod Log
Event
```

JobPilot 本身尽量保持 Stateless。

后续如果需要长期：

```text
操作审计
长期任务历史
统计报表
用户行为
任务备注
告警记录
```

再考虑 PostgreSQL。

---

# 5. 总体技术架构

```text
                    Browser
                       │
                       ▼
                    Vue 3
                       │
                  REST / SSE
                       │
                       ▼
                  JobPilot API
                    Go + Gin
                       │
        ┌──────────────┼──────────────┐
        │              │              │
        ▼              ▼              ▼
    GitLab OIDC   Kubernetes API    Metrics
                       │
          ┌────────────┼─────────────┐
          │            │             │
          ▼            ▼             ▼
       CronJob         Job           Pod
                                     │
                                     ▼
                                    Logs
```

JobPilot 不位于任务执行链路中。

即：

```text
JobPilot 挂掉
```

不能影响：

```text
CronJob 调度
Job 执行
Pod 运行
```

---

# 6. 技术栈

## 6.1 Frontend

采用：

```text
Vue 3
TypeScript
Vite
Vue Router
Pinia
Element Plus
Axios
ECharts
```

日志实时输出：

```text
SSE / EventSource
```

UI 定位：

```text
企业内部管理平台
简洁
高信息密度
任务状态优先
```

不需要复杂动画。

---

# 6.2 Backend

采用：

```text
Go
Gin
client-go
zap
OIDC
SSE
robfig/cron
```

暂时不需要：

```text
复杂 ORM
数据库框架
消息队列
微服务拆分
```

---

# 7. 项目结构

建议：

```text
jobpilot/
│
├── web/
│   ├── src/
│   │   ├── api/
│   │   ├── assets/
│   │   ├── components/
│   │   ├── layouts/
│   │   ├── router/
│   │   ├── stores/
│   │   ├── types/
│   │   └── views/
│   │       ├── dashboard/
│   │       ├── tasks/
│   │       ├── cronjobs/
│   │       ├── jobs/
│   │       ├── executions/
│   │       └── settings/
│   │
│   └── package.json
│
├── server/
│   ├── cmd/
│   │   ├── controller/
│   │   │   └── main.go
│   │   └── jobpilot/
│   │       └── main.go          # 后续 Web/API
│   │
│   ├── internal/
│   │   ├── api/
│   │   ├── auth/
│   │   ├── config/
│   │   ├── kubernetes/
│   │   ├── service/
│   │   ├── model/
│   │   ├── rbac/
│   │   ├── audit/
│   │   ├── middleware/
│   │   ├── controller/
│   │   │   └── workloadsync/
│   │   └── admission/
│   │       └── imageguard/
│   │
│   └── go.mod
│
├── deploy/
│   └── helm/
│       ├── jobpilot-controller/
│       └── jobpilot-jobs-catalog/
│
├── docs/
│   ├── architecture.md
│   ├── api.md
│   ├── rbac.md
│   └── design.md
│
├── Dockerfile
├── Makefile
├── README.md
└── plan.md
```

---

# 8. 部署模型

Vue 编译：

```text
Vue
 ↓
dist/
```

然后通过 Go：

```go
embed.FS
```

嵌入最终二进制。

P0 首先发布 Controller 镜像：

```text
jobpilot-controller:<version>
```

运行：

```text
Deployment
   │
   ├── Workload Sync Controller
   └── Image Admission Guard
```

后续 Web/API 可单独发布 `jobpilot-web:<version>`，并嵌入 Vue 静态资源；不得将 Web UI 的发布、OIDC 和产品 RBAC 作为 P0 镜像一致性链路的前置条件。

`jobpilot-controller` Kubernetes 资源：

```text
Deployment
Service（Webhook）
ServiceAccount
ClusterRole / Role
ClusterRoleBinding / RoleBinding
ConfigMap
Secret
ValidatingWebhookConfiguration
PodDisruptionBudget
```

Controller 保持：

```text
一个 Controller 镜像
一个双副本 Deployment
一个 Service
```

---

# 9. MVP 范围

JobPilot v0.1 必须完成以下能力。

## 9.1 CronJob 列表

展示：

```text
任务名称
Namespace
Schedule
TimeZone
Suspend
ConcurrencyPolicy
最近执行时间
最近执行结果
最近执行耗时
下次执行时间
当前状态
```

支持：

```text
Namespace 筛选
状态筛选
任务名称搜索
分页
```

状态建议：

```text
Running
Success
Failed
Suspended
NeverRun
Unknown
```

---

# 10. 任务列表 UI

建议主页面：

```text
任务名称        Namespace     调度          状态       最近执行    耗时     下次执行
--------------------------------------------------------------------------------
inventory-sync  wms-prod      */5 * * * *   Success    10:20      12s      10:25
order-timeout   wms-prod      */1 * * * *   Failed     10:23      31s      10:24
daily-report    report        0 2 * * *      Running    02:00      3m       Tomorrow
```

操作：

```text
立即执行
暂停
恢复
执行历史
查看详情
```

---

# 11. CronJob 详情页

页面：

```text
订单超时处理
```

基础信息：

```text
Namespace
Schedule
TimeZone
Suspend
ConcurrencyPolicy
StartingDeadlineSeconds
SuccessfulJobsHistoryLimit
FailedJobsHistoryLimit
```

运行信息：

```text
最近运行
最近状态
最近耗时
下次运行
Active Jobs
```

操作：

```text
立即执行
暂停
恢复
```

Tabs：

```text
Overview
Executions
Configuration
```

第一版本不要提供完整 YAML 在线编辑。

任务配置应继续通过：

```text
Git
Helm
GitOps
```

维护。

---

# 12. Execution 执行历史

核心体验必须类似 XXL-Job。

例如：

```text
执行时间          状态       耗时       触发方式        执行人
-----------------------------------------------------------------
10:23:00          Failed     31s        Schedule        system
10:22:00          Success    11s        Schedule        system
10:21:00          Success    12s        Manual          bruce
```

操作：

```text
查看日志
查看 Pod
重新执行
```

---

# 13. Job 状态判断

统一抽象：

```text
Pending
Running
Success
Failed
Unknown
```

参考：

```text
status.active
status.succeeded
status.failed
status.conditions
status.startTime
status.completionTime
```

耗时：

```text
completionTime - startTime
```

运行中：

```text
currentTime - startTime
```

---

# 14. 手动执行 CronJob

API：

```text
POST
/api/v1/namespaces/{namespace}/cronjobs/{name}/trigger
```

逻辑：

```text
读取 CronJob
      ↓
读取 spec.jobTemplate
      ↓
生成新的 Job
      ↓
设置 metadata
      ↓
创建 Kubernetes Job
```

禁止通过 shell 调：

```text
kubectl create job
```

必须直接使用：

```text
client-go
```

生成 Job Name：

```text
<cronjob-name>-manual-<timestamp/random>
```

增加 Label：

```yaml
jobpilot.io/source-cronjob: order-timeout
jobpilot.io/trigger-type: manual
```

Annotation：

```yaml
jobpilot.io/triggered-by: bruce
jobpilot.io/triggered-at: "..."
```

这样即使 v0.1 没数据库，也可以知道：

```text
谁执行了任务
手动还是自动
来源哪个 CronJob
```

---

# 15. Scheduled Job 识别

Kubernetes CronJob 自动创建的 Job 可以通过：

```text
ownerReferences
```

识别所属 CronJob。

JobPilot 创建的 Manual Job 使用：

```text
jobpilot.io/source-cronjob
```

关联。

因此：

```text
CronJob
   │
   ├── Scheduled Job
   ├── Scheduled Job
   ├── Manual Job
   └── Retry Job
```

全部展示到同一 Execution History。

---

# 16. 失败任务重跑

API：

```text
POST
/api/v1/namespaces/{namespace}/jobs/{name}/retry
```

如果 Job 来源是 CronJob：

优先根据原 CronJob：

```text
spec.jobTemplate
```

重新创建 Job。

否则复制原 Job Spec。

新的 Job 必须：

```text
生成新名称
清理 UID
清理 ResourceVersion
清理 ManagedFields
清理 Status
```

增加：

```yaml
jobpilot.io/trigger-type: retry
jobpilot.io/retry-from: old-job-name
jobpilot.io/triggered-by: username
```

---

# 17. 暂停 / 恢复

暂停：

```text
PATCH CronJob

spec.suspend = true
```

恢复：

```text
spec.suspend = false
```

API：

```text
POST /cronjobs/{name}/suspend

POST /cronjobs/{name}/resume
```

---

# 18. Job 页面

需要全局 Job 页面。

展示：

```text
Job
Namespace
Source CronJob
Status
Start Time
Duration
Completions
Failed
Trigger Type
Triggered By
```

支持：

```text
Running
Failed
Success
```

筛选。

---

# 19. 日志

流程：

```text
Job
 ↓
Pod
 ↓
Container
 ↓
Logs
```

API：

```text
GET
/api/v1/namespaces/{namespace}/jobs/{job}/pods
```

日志：

```text
GET
/api/v1/namespaces/{namespace}/pods/{pod}/logs
```

实时日志：

```text
GET
/api/v1/namespaces/{namespace}/pods/{pod}/logs/stream
```

推荐：

```text
SSE
```

前端支持：

```text
自动滚动
暂停滚动
下载日志
选择 Container
实时输出
```

第一版本日志直接来自 Kubernetes API。

后续可以增加：

```text
VictoriaLogs
```

查询历史日志。

---

# 20. Dashboard

Dashboard 不要做成 Kubernetes Dashboard。

只展示任务信息。

例如：

```text
CronJobs                68
Running Jobs             4
Failed Jobs              3
Suspended CronJobs       2
```

下面：

```text
最近失败任务
当前运行任务
最近手动执行
```

注意：

由于 v0.1 不使用数据库，统计数据仅基于 Kubernetes 当前仍保留的 Job。

不能把：

```text
24h Success Rate
30 Days SLA
```

这类长期数据做成误导性的指标。

长期统计以后使用：

```text
Prometheus
VictoriaMetrics
PostgreSQL
```

解决。

---

# 21. Cron Schedule

使用：

```text
robfig/cron
```

计算：

```text
Next Schedule
```

必须考虑 Kubernetes：

```yaml
spec.timeZone
```

如果没有配置：

按照 Kubernetes CronJob 行为处理。

UI 显示：

```text
*/5 * * * *
Every 5 minutes

Next:
10:25
10:30
10:35
```

---

# 22. Authentication

使用：

```text
GitLab OIDC
```

登录流程：

```text
JobPilot
   ↓
GitLab Login
   ↓
OIDC Callback
   ↓
User / Groups
   ↓
JobPilot RBAC
```

Session 推荐：

```text
Secure Cookie
HttpOnly
SameSite
```

OIDC 必须使用：

```text
state
nonce
PKCE（如果适用）
```

---

# 23. RBAC

JobPilot 自己实现应用级权限。

建议角色：

## Viewer

可以：

```text
查看 CronJob
查看 Job
查看执行历史
查看 Pod
查看日志
```

不能：

```text
执行
暂停
恢复
重跑
```

## Operator

增加：

```text
手动执行
失败重跑
```

## Admin

增加：

```text
暂停 CronJob
恢复 CronJob
管理 Namespace 授权
查看完整审计
```

第一版本不要允许：

```text
删除 CronJob
删除 Job
在线编辑完整 YAML
编辑 Secret
Pod Exec
Shell Terminal
```

---

# 24. Namespace 权限

JobPilot Backend 使用 Kubernetes ServiceAccount。

但：

> 不能因为 Backend 有 ClusterRole，就让所有登录用户看到整个集群。

必须增加 JobPilot 应用级 Namespace 授权。

例如：

```text
GitLab Group:

wms-developer
     ↓
Namespaces:
wms-test
wms-uat
```

管理员：

```text
ops
     ↓
*
```

后端每次 API 调用必须验证：

```text
User
 ↓
Role
 ↓
Allowed Namespaces
 ↓
Kubernetes API
```

不能只依赖前端隐藏菜单。

---

# 25. Kubernetes ServiceAccount RBAC

只申请 JobPilot 必需权限。

主要资源：

```text
batch/cronjobs
batch/jobs
pods
pods/log
events
namespaces
apps/deployments
```

主要权限：

```text
get
list
watch
create Job
patch CronJob
```

`jobpilot-controller` 的额外最小权限：

```text
apps/deployments: get, list, watch
batch/cronjobs: get, list, watch, patch
core/events: create, patch
coordination/leases: get, list, watch, create, update, patch
```

Image Admission Guard 只读取受管 Namespace 内的 CronJob 与 Deployment；它不写 Kubernetes 资源。`ValidatingWebhookConfiguration` 由 Helm/KubeSphere 的安装身份创建，Controller 的运行时 ServiceAccount 不应拥有修改它的权限。

尽量不要申请：

```text
delete
secrets
exec
cluster-admin
```

遵循最小权限原则。

---

# 26. API 设计

API Prefix：

```text
/api/v1
```

基础：

```text
GET /healthz
GET /readyz
GET /api/v1/me
```

Namespace：

```text
GET /api/v1/namespaces
```

CronJob：

```text
GET  /api/v1/cronjobs
GET  /api/v1/namespaces/:namespace/cronjobs/:name

POST /api/v1/namespaces/:namespace/cronjobs/:name/trigger
POST /api/v1/namespaces/:namespace/cronjobs/:name/suspend
POST /api/v1/namespaces/:namespace/cronjobs/:name/resume
```

Job：

```text
GET  /api/v1/jobs
GET  /api/v1/namespaces/:namespace/jobs/:name
POST /api/v1/namespaces/:namespace/jobs/:name/retry
```

Execution：

```text
GET
/api/v1/namespaces/:namespace/cronjobs/:name/executions
```

Pod：

```text
GET
/api/v1/namespaces/:namespace/jobs/:job/pods
```

Logs：

```text
GET
/api/v1/namespaces/:namespace/pods/:pod/logs

GET
/api/v1/namespaces/:namespace/pods/:pod/logs/stream
```

---

# 27. API Response

统一 Response Schema。

成功：

```json
{
  "data": {},
  "requestId": "xxx"
}
```

失败：

```json
{
  "error": {
    "code": "CRONJOB_NOT_FOUND",
    "message": "CronJob not found"
  },
  "requestId": "xxx"
}
```

不要把 Kubernetes 原始错误直接暴露给前端。

---

# 28. 审计

v0.1 不使用数据库。

所有危险操作输出 Structured Log：

```json
{
  "type": "audit",
  "user": "bruce",
  "action": "cronjob.trigger",
  "namespace": "wms-prod",
  "resource": "order-timeout",
  "result": "success"
}
```

操作包括：

```text
trigger
retry
suspend
resume
login
```

以后可以直接由 VictoriaLogs 收集。

---

# 29. Kubernetes Watch / Informer

CronJob/Job 管理 UI 第一版本采用：

```text
List API + 前端定时 Refresh
```

UI 读取路径后端架构需要预留：

```text
Informer
SharedInformerFactory
```

后续用于：

```text
CronJob 状态实时刷新
Job 状态实时刷新
减少 API Server 请求
```

第一版只引入一个受限的 JobPilot Workload Sync Controller：Watch Deployment/CronJob，按 `jobpilot.io/follow-workload` 关联同步 image。它不用于 UI 刷新、不运行 scheduler、不 watch Job/Pod、不创建 Job。为保证 P0 的强一致性，`jobpilot-controller` 同时提供独立的 Image Admission Guard，在受管 Job 创建时拒绝与来源 Deployment image 不一致的 Job；其他 controller 能力仍不进入 v0.1。

严格模式使用 `failurePolicy: Fail`：Controller/Webhook 不可用时，新的受管 Job 会被拒绝，换取不执行旧 image 的保证；已有 Job/Pod 不受影响。因此 `jobpilot-controller` 必须至少两个副本、PDB、readiness 和 Webhook 告警。不能同时承诺“JobPilot 故障不影响新任务启动”和“绝不允许旧 image 执行”。

---

# 30. Prometheus

JobPilot 不重复实现 kube-state-metrics 已经提供的 Kubernetes Job Metrics。

JobPilot 自己只需要暴露：

```text
HTTP Request
API Latency
Kubernetes API Error
OIDC Error
Trigger Count
Retry Count
```

例如：

```text
jobpilot_http_requests_total

jobpilot_k8s_api_errors_total

jobpilot_job_trigger_total
```

长期任务监控交给：

```text
Prometheus / VictoriaMetrics
Grafana
```

---

# 31. 与现有监控体系关系

架构：

```text
                 JobPilot
                    │
                    │ 管理
                    ▼
             Kubernetes API
                    │
          ┌─────────┴──────────┐
          │                    │
          ▼                    ▼
 kube-state-metrics          Pod Logs
          │                    │
          ▼                    ▼
 Prometheus / VM          VictoriaLogs
          │
          ▼
       Grafana
```

职责：

```text
JobPilot
操作 / 排障

Grafana
趋势 / 统计

VictoriaLogs
长期日志
```

---

# 32. Job History

必须认识到：

Kubernetes Job 不一定永久保留。

CronJob：

```yaml
successfulJobsHistoryLimit:
failedJobsHistoryLimit:
```

以及：

```yaml
ttlSecondsAfterFinished:
```

都可能清理 Job。

因此 JobPilot v0.1 的 Execution History 定义为：

> Kubernetes 当前仍然存在的 Job 历史。

UI 可以显示：

```text
历史记录由 Kubernetes Job 保留策略决定。
```

JobPilot v0.1 不私自改变用户 CronJob History Limit。

---

# 33. 安全边界

v0.1 明确不支持：

```text
Pod Exec
Web Terminal
Secret Viewer
ConfigMap Editor
完整 YAML Editor
任意 Kubernetes API Proxy
创建任意 Pod
创建任意 Container Image
```

否则 JobPilot 会逐渐变成一个 Kubernetes Dashboard。

---

# 34. Reference Projects

开发前研究以下开源项目。

## kubernetes-job-ui

Repository：

```text
lhopki01/kubernetes-job-ui
```

重点研究：

```text
Go + Gin
client-go
CronJob → Job
手动触发
Job History
参数运行
internal/k8s
```

不要直接复制旧前端。

---

## Kronic

Repository：

```text
mshade/kronic
```

重点研究：

```text
CronJob UI
跨 Namespace
Trigger
Suspend
Resume
CronJob → Job → Pod
```

主要借鉴：

```text
产品交互
页面结构
```

---

## CronJob Guardian

Repository：

```text
iLLeniumStudios/cronjob-guardian
```

重点研究：

```text
controller
analyzer
metrics
Job 状态分析
Missed Schedule
SLA
Duration
```

主要作为未来监控能力参考。

---

## Onax

Repository：

```text
varaxlabs/onax
```

研究：

```text
client-go
controller-runtime
Prometheus
next_schedule
success_rate
missed_schedule
active_jobs
```

---

## kubecron

研究：

```text
run
suspend
unsuspend
```

作为简单 Kubernetes CronJob 操作实现参考。

---

## P0 Controller 补充参考项目

下列项目用于实现 `jobpilot-controller`，优先借鉴工程模式而非复制业务代码：

```text
kubernetes-sigs/controller-runtime + kubernetes-sigs/kubebuilder
  → Watch、索引、leader election、Webhook、envtest 的官方工程骨架

stakater/Reloader
  → 源资源变更映射到带 Annotation 的关联目标资源；不采用其滚动重启行为

mondoohq/mondoo-operator
  → CronJob 局部字段收敛、Event/Condition、幂等更新；仅参考设计

mittwald/kubernetes-replicator
  → 跨 Namespace 资源分发中的标签、选择器与循环规避；不作为 CronJob 同步器

openkruise/kruise
  → 生产级 Controller 的状态、事件和兼容性组织；不引入其工作负载体系
```

License 结论：`controller-runtime`、`kubebuilder`、Reloader、kubernetes-replicator 当前为 Apache-2.0；任何源码复用仍须在锁定版本时复核。Mondoo Operator 当前源码为 BUSL-1.1，禁止复制其源码。OpenKruise 的 GitHub License 元数据未被识别，直接复用前必须人工核验。上述项目均不提供可直接安装的 `Deployment → CronJob image` 跟随 Controller。

---

# 35. 开源代码使用要求

可以参考：

```text
架构
设计思想
数据模型
API 使用方式
UI 交互
```

如果计划直接复制代码：

必须首先检查对应 Repository License。

优先：

> 理解实现后重新实现。

不要未经 License 检查大量复制第三方源码。

---

# 36. MVP 页面

左侧导航建议：

```text
JobPilot

Overview

Tasks
  ├── CronJobs
  ├── Jobs
  └── Executions

System
  └── Settings
```

不要出现：

```text
Deployment
StatefulSet
Service
Ingress
PVC
ConfigMap
Secret
```

JobPilot 必须保持任务中心定位。

---

# 37. v0.1 明确不做

以下功能放到后续：

```text
多集群
数据库
长期 Job 历史
复杂 Dashboard
SLA
任务依赖 DAG
Workflow
审批
HTTP Job
Shell Job
Database Job
完整任务编辑器
在线 YAML 编辑
告警中心
任务参数模板
复杂通知
```

---

# 38. v0.2

计划增加：

```text
GitLab Group → Namespace RBAC 完善
操作审计增强
任务负责人
任务描述
任务标签
Cron 表达式解析
运行参数
VictoriaLogs 日志查询
```

---

# 39. v0.3

增加：

```text
Prometheus Metrics
成功率
失败率
P50
P95
P99
Missed Schedule
异常耗时
Grafana Dashboard
任务告警
```

---

# 40. v0.4+

考虑：

```text
Multi Cluster
长期 History
PostgreSQL
Audit Database
Notifications
Slack/Webhook
任务 SLA
```

---

# 41. 开发顺序

建议按照下面顺序开发。

Phase 0：

```text
jobpilot-controller Helm 骨架
Workload Sync Controller
Image Admission Guard
Kyverno Catalog PoC
```

验收：Catalog 首次生成的 CronJob 在 Controller 同步当前 Deployment image 前保持 suspend；Deployment 更新窗口中的旧 image Job 被 Guard 拒绝；同步后新 image Job 被放行。

Phase 1：

```text
项目骨架
Vue
Go
Gin
client-go
Docker
Helm
```

Phase 2：

```text
Kubernetes Connection
Namespace
CronJob List
Job List
```

Phase 3：

```text
CronJob Detail
Job Detail
Execution History
Status Parser
Duration
Next Schedule
```

Phase 4：

```text
Trigger
Retry
Suspend
Resume
```

Phase 5：

```text
Pod
Logs
SSE
```

Phase 6：

```text
GitLab OIDC
RBAC
Namespace Authorization
```

Phase 7：

```text
Audit
Metrics
Tests
Helm Production Deployment
```

---

# 42. Testing

后端必须至少覆盖：

```text
CronJob status parser

Job status parser

Duration calculation

Next schedule

CronJob → manual Job

Retry Job

Namespace authorization

RBAC

Kubernetes API errors

Workload Sync Controller 的 Deployment image 变更、多个 CronJob 映射、Annotation/容器缺失、局部 Patch 与幂等性

Image Admission Guard 的允许、旧 image 拒绝、来源不存在拒绝、Webhook 不可用 fail-closed
```

推荐：

```text
fake.Clientset
httptest
```

前端：

```text
关键组件 Unit Test
主要流程 E2E
```

主要 E2E：

```text
Login
   ↓
CronJob List
   ↓
CronJob Detail
   ↓
Trigger
   ↓
Job Running
   ↓
Success
   ↓
Logs
```

---

# 43. Definition of Done

JobPilot v0.1 完成标准：

在 Web/API 功能前，P0 控制平面必须先满足：

```text
Catalog 通过 Kyverno 首次生成目标 Namespace CronJob
首次生成的 CronJob 在 image 同步完成前保持 suspend
Deployment image 变更后，关联 CronJob 最终收敛到相同 image
同步窗口内的旧 image Job 被 Image Admission Guard 拒绝
Guard 不可用时受管 Job fail-closed；两个副本中任一可用时可继续校验
```

一个 Developer 登录 JobPilot 后，可以：

```text
查看自己有权限 Namespace 中的所有 CronJob

查看任务当前状态

查看最近一次执行

查看任务执行历史

查看 Job 状态

查看 Job 对应 Pod

查看实时日志

手动执行 CronJob

重跑失败 Job
```

Operator/Admin 可以：

```text
暂停 CronJob

恢复 CronJob
```

且：

```text
不需要 kubectl

不需要访问 KubeSphere

不需要 Kubernetes YAML 知识
```

即可完成任务日常管理。

---

# 44. 最重要的设计约束

开发过程中始终检查：

> 当前新增的功能是在增强 Job/CronJob 管理，还是正在把 JobPilot 做成另一个 Kubernetes Dashboard？

如果属于后者：

不要实现。

同时检查：

> 当前新增功能是不是正在重新实现 Kubernetes 已经存在的调度能力？

如果是：

不要实现。

最终架构始终保持：

```text
              JobPilot
                  │
          Management Layer
                  │
                  ▼
             Kubernetes
                  │
           Native CronJob
                  │
                  ▼
                 Job
                  │
                  ▼
                 Pod
```

---

# 45. Codex develop-design 任务

基于本文档执行 **develop-design**。

当前阶段首先完成设计，不要立即大规模生成业务代码。

需要输出：

```text
docs/design.md
docs/architecture.md
docs/api.md
docs/rbac.md
```

develop-design 必须包含：

```text
1. 系统架构
2. 模块划分
3. Go package 设计
4. Vue 页面与组件设计
5. API Contract
6. Kubernetes Resource Mapping
7. CronJob / Job 状态计算规则
8. Trigger 实现流程
9. Retry 实现流程
10. Logs Streaming 实现
11. GitLab OIDC 流程
12. RBAC 权限模型
13. Namespace Authorization
14. Kubernetes ServiceAccount RBAC
15. Error Model
16. Audit Model
17. Configuration Model
18. Helm Deployment
19. Test Strategy
20. MVP Milestone
```

对于关键设计决策需要说明：

```text
为什么这样设计
有哪些替代方案
为什么没有选择其他方案
```

特别需要确认：

```text
是否真的需要数据库
是否真的需要 controller-runtime
是否真的需要 WebSocket
是否真的需要多集群
```

默认答案应该倾向：

```text
v0.1 保持最小实现
```

不要提前过度设计。

完成 develop-design 后，再根据设计进入 implementation。

这个版本已经可以直接作为 Codex 的总需求输入。我建议让 Codex **先只做 develop-design，不马上写业务代码**，设计完成后我们再审一遍 API、目录和 K8s 权限，能避免后面大改。


---

# 46. 文档交付边界与 Codex 调研要求

`plan.md` 是项目总需求、范围边界、硬约束、阶段路线图和验收标准；它不是详细设计文档，也不能替代详细设计。

在本计划之后，Codex 必须先完成参考项目调研，随后再完成 develop-design。不得跳过调研直接生成大规模业务代码。

## 46.1 调研门禁

在进行 develop-design 前，必须：

1. 阅读五个参考项目的 README。
2. 检查每个项目的 License，并记录任何可能影响源码复用的限制。
3. 重点查看与 JobPilot 能力直接相关的目录和实现，而不是只阅读项目简介。
4. 总结每个项目值得借鉴的设计及不适合 JobPilot 的设计；不得机械复制任何一个项目架构。
5. 优先理解设计后重新实现；任何直接复用代码都必须先通过 License 审核。
6. 按优先级深挖 `kubernetes-job-ui`，它与 Go + Gin + client-go 路线最接近。

调研结果必须写入 `docs/reference-analysis.md`，并在 `docs/design.md` 中保留 **Open Source Reference Analysis** 一节，至少说明：

- kubernetes-job-ui 哪些 Kubernetes 操作逻辑值得借鉴；
- Kronic 哪些 UI / UX 和任务管理流程值得借鉴；
- CronJob Guardian 哪些状态模型值得借鉴；
- Onax 哪些 Metrics 与 Cron 计算值得借鉴；
- kubecron 哪些轻量操作实现值得借鉴；
- 哪些设计不适合 JobPilot；
- JobPilot 最终选择什么方案，以及原因。

## 46.2 develop-design 的独立交付物

调研通过后，必须输出：

```text
docs/reference-analysis.md
docs/design.md
docs/architecture.md
docs/api.md
docs/rbac.md
```

文档职责严格区分：

```text
plan.md                    项目背景、范围、硬约束、阶段路线图、验收标准
docs/reference-analysis.md 调研证据、License 结论、借鉴/舍弃决策
docs/design.md              跨模块设计决策、资源映射、端到端流程
docs/architecture.md       组件边界、目录、Go package、Vue 页面与部署结构
docs/api.md                可实现的 REST / SSE Contract、样例、分页与 Error Model
docs/rbac.md               产品角色、Namespace 授权和 Kubernetes RBAC 映射
docs/xxl-job-plan.md        XXL-Job 任务盘点、迁移阶段、验收与回滚计划
docs/xxl-job-design.md     同镜像、Catalog 分发、Job Runner、Workload Sync Controller 与 Image Admission Guard 的技术设计
```

详细设计必须足以让实现者不再重新决定 API 行为、资源映射、状态计算、权限边界和失败处理。每个关键决策都应写明选择、备选方案、拒绝原因及其对 v0.1 范围的影响。

## 46.3 详细设计最低深度

- `docs/architecture.md` 必须给出 package 责任边界、依赖方向、每个 Kubernetes 资源的读取/写入路径，以及页面到 API 的映射。
- `docs/api.md` 必须给出每个端点的 method、path、鉴权、请求字段、成功响应、错误响应、分页/过滤/排序规则、幂等性要求和 SSE 断线重连行为。
- `docs/design.md` 必须给出 CronJob / Job 状态优先级、时间与时区规则、next schedule 计算输入、Trigger 与 Retry 从 Kubernetes 资源到创建结果的完整流程，以及日志流的授权与断连语义。
- `docs/rbac.md` 必须同时说明 JobPilot 产品角色、Namespace 授权来源、Kubernetes ServiceAccount 的最小 verbs/resources，并区分查看、触发、重试、暂停和恢复的权限。
- 所有设计必须明确 v0.1 的无数据库、无自建 Scheduler、单集群、单镜像、Stateless 边界，并说明 Kubernetes API 不可用、资源不存在、资源版本冲突和日志 Pod 不可用时的用户可见行为。

---

# 47. 多 Namespace CronJob 分发：Kyverno Generate Policy

当相同或近似的业务任务需要部署到多个业务 Namespace（例如 WMS、WES），可以采用 [Kyverno](https://github.com/kyverno/kyverno) 的 Generate Policy 分发 CronJob。

这不是 JobPilot 的功能，也不是新的 Scheduler：Kyverno 只负责把声明式 CronJob 模板生成到目标 Namespace；生成后的 CronJob 仍由 Kubernetes 调度，JobPilot 仍只负责运行期管理。

```text
Git + Helm
   │
   ├─ Kyverno ClusterPolicy（CronJob 模板）
   └─ 目标 Namespace label
            │
            ▼
      Kyverno Generate Policy
            │
            ▼
目标 Namespace 的 CronJob
            │
            ▼
Kubernetes CronJob Controller → Job → Pod
            │
            ▼
JobPilot（查看、触发、重跑、日志）
```

## 47.1 适用条件

适用于：

```text
多个 Namespace 需要相同的 CronJob 模板
任务在本 Namespace 调用同名或同约定的 Kubernetes Service
任务镜像、schedule、资源限制和运行方式基本一致
希望通过 Namespace label 自动纳入或移出任务分发范围
```

例如，目标 Namespace 标记为：

```yaml
metadata:
  labels:
    jobpilot.io/enable-jobs: "true"
```

Kyverno 根据 label 生成目标 Namespace 的 CronJob。任务 Pod 在同 Namespace 内可直接访问：

```text
http://wms-api:8080/...
```

不需要写 Namespace DNS；跨 Namespace 调用才使用完整 Service DNS 或明确配置。

## 47.2 职责边界

```text
Git / Helm
  维护 Kyverno Policy、模板、版本和 Namespace label

Kyverno
  在匹配 Namespace 首次创建 CronJob

Kubernetes
  依据 CronJob 创建 Job、调度 Pod、重试和清理历史

JobPilot
  只管理已经存在于目标 Namespace 的 CronJob / Job / Pod
```

JobPilot 不创建 Kyverno Policy、不复制 CronJob、不管理 Namespace label，也不替代 Kyverno 的同步状态。

## 47.3 同步模式与暂停冲突

Kyverno 的 `synchronize: true` 会将生成资源保持为中心定义的期望状态，目标 Namespace 的手工修改可能被覆盖。因此必须先明确 CronJob 的字段所有权：

```text
Catalog 策略所有字段：schedule、command、args、resources、ServiceAccount、retry、history limit
JobPilot 运行期字段：target image、bootstrap suspend 与同步状态 Annotation
```

P0 固定采用：

```text
synchronize: false
```

它只负责首次生成，保留 JobPilot 对 image、bootstrap suspend 和同步状态的控制权；中心模板升级则通过受控 GitOps 变更和迁移步骤处理。

`synchronize: true` 不属于 P0。若未来考虑启用，必须在测试集群验证以下行为后再启用：

```text
JobPilot suspend 后是否被 Kyverno 覆盖
JobPilot resume 后是否被 Kyverno 覆盖
中心模板更新是否意外触发或补跑任务
删除 Namespace label 后生成的 CronJob 是否被删除
目标 CronJob 的手工修改是否符合预期地被回滚
```

在 `synchronize: true` 且会覆盖 Controller 字段的情况下，必须重新划分字段所有权；不能同时由 Kyverno 与 JobPilot 写同一字段。

## 47.4 不适用场景

以下情况不使用 Kyverno 分发：

```text
每个 Namespace 的 schedule、镜像、参数或资源差异很大
每个 Namespace 需要独立升级、独立回滚或独立维护 CronJob
任务必须跨 Namespace 调用不同 Service，且没有稳定配置模型
需要复杂工作流、任务分片或动态编排
```

这些情况仍使用每 Namespace 独立 Helm Release，或使用 Argo CD ApplicationSet / Flux 批量生成 Release。

## 47.5 安全与上线要求

Kyverno background controller 需要创建目标 Namespace CronJob 的权限，因此必须单独审核其 ClusterRole，不能把 JobPilot 的 ServiceAccount 权限复用给 Kyverno。

首次上线采用 allow-list Namespace label，先在非生产 Namespace 验证生成、Service DNS、日志、暂停、删除 label 和回滚行为；确认后再逐步标记生产 Namespace。

---

# 48. 同镜像、Catalog 分发与 Workload Sync

方案 A 的业务服务与任务均使用**同一个业务镜像**，但业务应用、任务 Catalog 与运行期 Controller 必须分开管理，而不是把 CronJob 塞进业务应用 Chart：

```text
wms-app.tgz
  ├── Deployment / Service / Ingress
  └── image: eco-harbor.yfsj.cc/wms/wms:v3.6.1

jobpilot-jobs-catalog.tgz
  ├── CronJob Catalog（初始 suspend: true）
  └── Kyverno ClusterPolicy（只首次分发到带 label 的 Namespace）

jobpilot-controller.tgz
  ├── Workload Sync Controller
  └── Image Admission Guard
```

在线 Deployment 使用默认 server mode；CronJob 使用同一镜像的 `--mode=job --job=<stable-name>` 进入一次性 Job mode。拆的是 Kubernetes 发布模型，不是业务代码或镜像。

```text
wms-app Helm
  定义在线服务和业务版本（Deployment 是 image Source of Truth）

jobpilot-jobs-catalog Helm
  定义任务集合、schedule、args、资源、重试和 workloadRef；由 Kyverno 首次生成目标 CronJob

jobpilot-controller
  将 Deployment 当前 image 同步到关联 CronJob 的 target container image，并拒绝旧 image Job

Kubernetes
  仍负责 CronJob → Job → Pod 调度和执行
```

`jobpilot-controller` 是 JobPilot 的增强控制平面，可以独立于未来 Web/API 发布。它不是 Scheduler：不创建 Job、不计算 cron、不读 Secret、不修改 Deployment。其 P0 仅包含 Workload Sync Controller 与 Image Admission Guard；分析、通知、清理等后续 Controller 不进入本期。

CronJob 采用 `jobpilot.io/follow-workload=true`、workload name、source container 和 target container Annotation 关联到同 Namespace Deployment。一个 Deployment 可以被多个 CronJob 引用。

Catalog 中的 image 仅为首次生成提供合法 bootstrap 值，不能成为长期版本事实来源。目标 CronJob 不由 Helm 持续管理，后续 image 由 JobPilot Controller 管理，因此不会出现 Helm upgrade 把旧 image 写回目标 CronJob 的冲突。首次生成时先 `suspend: true`，等 Controller 记录 `ImageSynchronized` 后再启用，避免首次调度使用过期 bootstrap image。

已运行 Job 保持创建时的 image；一次 KubeSphere Deployment 更新只影响同步完成后的后续 Job。严格模式下，Image Admission Guard 会拒绝同步窗口内的旧 image Job；Guard 不可用时同样拒绝新的受管 Job。已有 Job/Pod 不受影响。

ConfigMap、Secret、ServiceAccount 等 Job 运行环境由 Catalog 一并声明并分发到目标 Namespace，MVP 不从 Deployment 复制。Kyverno 只负责首次生成资源；FollowWorkload 的 image、bootstrap suspend 与同步状态字段由 JobPilot Controller 负责，二者的字段所有权必须隔离。

---

# 49. Git 分支与合并约束

采用简单的主干开发模式：

```text
main
├── feature/controller-p0
├── feature/admission-guard
├── feature/jobs-catalog
├── feature/web-api
└── docs/*
```

## 49.1 分支规则

| 分支 | 用途 | 合并目标 |
| --- | --- | --- |
| `main` | 稳定、可发布的主分支；GitLab 默认分支 | 无 |
| `feature/<scope>` | 单一功能或技术改动 | `main` |
| `docs/<scope>` | 文档或架构调整 | `main` |
| `hotfix/<scope>` | 已发布版本的紧急修复 | `main` |

不创建长期 `develop` 分支；当前项目规模使用 `main + 短生命周期分支`。一个分支只处理一个可独立评审的主题，不混入无关重构、格式化或功能。

P0 推荐按以下顺序创建功能分支：

```text
feature/controller-p0
feature/admission-guard
feature/jobs-catalog
```

`feature/admission-guard` 依赖 Controller 项目骨架，但可在 `feature/controller-p0` 合入 `main` 后再创建，避免长期分支互相依赖。

## 49.2 GitLab 保护规则

`main` 必须配置为 Protected Branch：

```text
禁止直接 Push
只允许通过 Merge Request 合并
至少 1 名 Reviewer 批准
合并前 Pipeline 必须成功
禁止 force push
```

创建 Merge Request 前，分支必须基于最新 `main` 处理冲突，并通过与改动范围相符的格式检查、单元测试和 Helm/Manifest 渲染校验。文档改动至少检查 Markdown 链接与格式。

## 49.3 提交与发布

提交信息使用简短约定：

```text
feat(controller): add workload image sync
feat(admission): reject stale Job image
feat(catalog): add Kyverno CronJob catalog
docs: refine xxl-job migration design
fix(controller): preserve manual CronJob suspension
```

已验证并合并到 `main` 的版本通过 Tag 发布：

```text
v0.1.0
v0.2.0
```

Tag 指向 `main` 的合并提交；不从未合并 feature 分支发布。紧急修复使用 `hotfix/<scope>` 合并回 `main` 后再发布新 Tag。
