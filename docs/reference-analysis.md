# Open Source Reference Analysis

> 调研日期：2026-09-20。下列结论来自各仓库 README、License、GitHub 默认分支及列出的源码文件；它们用于设计借鉴，不构成对第三方代码的复制授权。

## 结论摘要

JobPilot v0.1 采用 **Go + Gin + client-go/controller-runtime 的无状态管理层**。它直接读取和操作 Kubernetes 的 `CronJob`、`Job`、`Pod` 与 `PodLog`，不创建 CRD、不保存执行历史，也不参与调度；P0 以独立 `jobpilot-controller` 部署职责受限的 Workload Sync Controller 和 Image Admission Guard，分别同步 Deployment 到关联 CronJob 的镜像、拒绝旧 image Job。

| 优先级 | 项目 | 可借鉴结论 | v0.1 采用情况 |
| --- | --- | --- | --- |
| 最高 | kubernetes-job-ui | 从 `CronJob.JobTemplate` 构建 Job、以 OwnerReference 归属任务、参数定义与校验边界 | 采用资源操作思路；不采用其旧 API、轮询集合和前端 |
| 高 | Kronic | 跨 Namespace 入口、CronJob → Job → Pod 下钻、受限 Namespace | 采用信息架构和下钻；不采用编辑/YAML/删除能力与技术栈 |
| 高 | CronJob Guardian | 将调度健康、SLA、长期记录和 UI 解耦的状态模型 | 仅采用未来能力分层；不采用其 CRD、数据库或告警；Workload Sync 单独采用 controller-runtime |
| 中 | Onax | 纯派生状态、指标命名、调度计算隔离 | 采用状态/cron 计算的纯函数边界；不实现 missed schedule 与长期成功率 |
| 中 | kubecron | 最小化 Job 创建与冲突重试更新 `spec.suspend` | 采用直接 client-go 调用与冲突重试；补强名称、审计、鉴权和错误模型 |
| 最高 | Kubebuilder CronJob Tutorial | controller-runtime 的 Reconcile、Watch、索引、OwnerReference 和测试结构 | 采用 Controller 工程骨架和 Deployment → CronJob 反向映射思路；不采用其自定义 CronJob 调度实现 |
| 最高 | OpenKruise AdvancedCronJob | CRD、JobTemplate、Status、Conditions、Controller 分层 | 仅借鉴资源模型和 Reconcile 组织；不引入 AdvancedCronJob CRD，不复制其调度器 |
| 高 | KEDA ScaledJob | JobTemplate、Status/Conditions、Leader Election、并发 Reconcile 和事件 | 借鉴生产化 Controller 细节；不引入 ScaledJob 或事件触发调度 |
| 中 | Metacontroller | 以 Webhook 返回期望资源的轻量控制器模式 | 不采用；JobPilot 已使用 Go/controller-runtime，额外引入 webhook 控制面没有收益 |

## 1. kubernetes-job-ui

- Repository: <https://github.com/lhopki01/kubernetes-job-ui>
- 调研 revision: `2b15cd8dff1cfa7c6539dcc44b6015a18654a77b`（`master`）
- License: 仓库元数据未声明 License；因此 **不得复制其源码**。
- 关键文件：`internal/k8s/client.go`、`internal/k8s/model.go`、`internal/site/http.go`、`cmd/commands.go`。

### 值得借鉴

1. 用 Kubernetes API 作为唯一事实来源：CronJob、Job、Pod 的组合视图是派生数据，不需要数据库。
2. 从 `CronJob.Spec.JobTemplate.Spec` 创建新 Job 是手动触发的正确资源级路径；不应调用 shell 或 `kubectl`。
3. Job 与 CronJob 的关联首先检查 OwnerReference；手动 Job 另加可查询标记。
4. 参数化任务应由 CronJob 显式声明、后端校验后才允许修改容器环境变量，不能接受任意 Pod/Job patch。

### 不采用的部分

- 使用已废弃的 `batch/v1beta1` CronJob API；JobPilot 固定使用 `batch/v1`。
- 每五秒把全部资源装入进程内集合的轮询模式；v0.1 使用请求期 List，前端刷新，后续才可加 SharedInformer。
- 无认证、无应用级 Namespace 授权。
- 旧 React 前端与扁平 API；JobPilot 使用 Vue 3、显式资源页和统一错误模型。
- 通用的 CronJob 参数化 UI：产品未提供任务定义协议前，v0.1 不暴露参数覆盖入口。

## 2. Kronic

- Repository: <https://github.com/mshade/kronic>
- 调研 revision: `27f0faf727c99be6d09b02c433057a9b8df508aa`（`main`）
- License: Apache-2.0。
- 关键文件：`kron.py`、`app.py`、`templates/namespace.html`、`templates/cronjob.html`、`chart/kronic/templates/rbac.yaml`。

### 值得借鉴

1. 首页先按 Namespace 聚合，随后进入任务列表，降低跨团队集群的认知负担。
2. 详情页中把 CronJob、其 Job 和每个 Job 的 Pod 组织成自然下钻链路。
3. Namespace allow-list 与 namespaced 安装选项证明了“产品可见范围”必须被后端强制，而非只靠菜单隐藏。
4. 手动 Job 使用任务来源标签，使它和调度 Job 能共同出现在执行历史中。

### 不采用的部分

- Flask、AlpineJS、PicoCSS 与 HTTP Basic Auth；JobPilot 使用 Go、Gin、Vue 与 GitLab OIDC。
- 创建、克隆、删除 CronJob，完整 YAML 编辑，删除 Job；这些会把 JobPilot 扩展为 Kubernetes Dashboard。
- 以 GET/POST toggle `suspend` 的接口；JobPilot 使用语义明确的 `suspend` / `resume` 操作，并带资源版本冲突处理。
- 直接将 Kubernetes API 对象及错误原样返回；JobPilot 返回稳定 DTO 与错误代码。

## 3. CronJob Guardian

- Repository: <https://github.com/iLLeniumStudios/cronjob-guardian>
- 调研 revision: `ed93b9cc6bf5d1b51389034379da45d30ee48d61`（`main`）
- License: Apache-2.0。
- 关键文件：`internal/analyzer/sla.go`、`internal/controller/job_controller.go`、`internal/metrics/metrics.go`、`api/v1alpha1/cronjobmonitor_types.go`。

### 值得借鉴

1. 将执行记录、SLA 分析、告警、指标和 UI 分层，避免把状态判定散落在页面中。
2. 通过 Job OwnerReference 定位父 CronJob，再以 Job 条件、开始/完成时间、Pod 终止信息形成执行结果；这是状态解释而非调度。
3. dead-man、成功率、P95 和历史保留都依赖长期记录或连续观测，模型边界清楚。
4. 监控型 RBAC 只读 `jobs`、`pods`、`pods/log`、`events` 的做法可作为 JobPilot 读权限的基线。

### 不采用的部分

- CRD、controller-runtime Reconciler、Leader Election、GORM/数据库、告警渠道和自定义监控对象；它们均超出 v0.1 的管理 UI 范围。
- 将日志、事件和执行历史持久化；v0.1 仅展示 Kubernetes 当前仍保留的资源。
- SLA、missed schedule 和性能回归 UI；没有可靠的长期数据时展示这些指标会误导用户。

## 4. Onax

- Repository: <https://github.com/varaxlabs/onax>
- 调研 revision: `006681eec3ca885990e2420933137ae052c5c643`（`main`）
- License: Apache-2.0。
- 关键文件：`pkg/models/cronjob_status.go`、`pkg/watcher/schedule_parser.go`、`pkg/watcher/schedule_tracker.go`、`pkg/metrics/collector.go`。

### 值得借鉴

1. `ComputeStatus` 将 CronJob 与已归属 Job 显式传入，计算结果是纯派生模型，便于 fake client 单测。
2. cron 解析独立封装并使用 `robfig/cron/v3`；next schedule 不应由前端自行解释。
3. Prometheus 指标使用低基数标签 `namespace`、`cronjob`、`status`，并删除已删除资源的 label series。
4. 指标计数在重复 reconcile 下需保持幂等，这是未来引入 informer 时的重要约束。

### 不采用的部分

- 每分钟运行的 schedule tracker、missed schedule counter、success rate 与长期 metrics；这些要求连续运行/历史窗口，不能由当前保留的 Job 可靠推出。
- controller-runtime 作为 CRUD 服务框架；JobPilot 只需 client-go typed client 与 Gin。
- 为管理 UI 重复导出 `kube-state-metrics` 已有的 Job 资源指标；v0.1 只导出 JobPilot 自身 HTTP、Kubernetes API 和操作计数。

## 5. kubecron

- Repository: <https://github.com/danielec7/kubecron>
- 调研 revision: `ce230ac21cb21c24aa294948e59d451c8c1c27d1`（`master`）
- License: MIT。
- 关键文件：`cmd/run.go`、`cmd/suspend.go`。

### 值得借鉴

1. 手动运行只需读取 CronJob 并用 `JobTemplate.Spec` 创建 Job。
2. 修改 `spec.suspend` 应使用 Kubernetes 冲突重试，不能用进程内锁假设资源不会被 GitOps 或其他操作者修改。

### 不采用的部分

- 秒级名称后缀可能碰撞；JobPilot 用 `GenerateName`，由 API Server 完成唯一命名。
- `panic` 处理 Kubernetes 错误；JobPilot 将其映射为稳定的 HTTP 错误并记录 request ID。
- 不带触发者、来源或重试关联标签的裸 Job 创建；JobPilot 补充受限的 `jobpilot.io/*` metadata。

## 6. Controller / CRD 参考项目

### Kubebuilder CronJob Tutorial

- Repository: <https://github.com/kubernetes-sigs/kubebuilder>
- License: Apache-2.0。
- 重点借鉴：controller-runtime Manager、Reconcile、`EnqueueRequestsFromMapFunc`、IndexField、OwnerReference、fake client 测试和 CronJob/Job 生命周期测试。
- 不采用：教程中由自定义资源直接实现 Cron 调度的部分。JobPilot 仍让 Kubernetes CronJob Controller 调度。

### controller-runtime

- Repository: <https://github.com/kubernetes-sigs/controller-runtime>
- License: Apache-2.0。
- 重点借鉴：Manager 生命周期、cache 与 direct API Reader 的边界、Watch/Index 映射、leader election、admission Webhook 和 envtest。
- 不采用：任何默认的 CRD、样例业务逻辑或全量资源 ownership。JobPilot 只实现 FollowWorkload 的局部字段收敛和 Job CREATE 校验。

### Stakater Reloader

- Repository: <https://github.com/stakater/Reloader>
- License: Apache-2.0。
- 重点借鉴：source 资源变更如何映射到由 Annotation 声明依赖关系的目标 Workload，及重复事件下的幂等处理。
- 不采用：ConfigMap/Secret 变更后滚动重启 Deployment 的行为；JobPilot 不主动重启或替换业务工作负载。

### Mondoo Operator

- Repository: <https://github.com/mondoohq/mondoo-operator>
- License: 当前源码为 BUSL-1.1。
- 重点借鉴：CronJob 局部字段收敛、CreateOrUpdate 边界、Event/Condition 与幂等更新的设计。
- 不采用：源码、扫描业务模型、CRD 与外部平台依赖。BUSL-1.1 不进入 JobPilot 源码复用范围。

### kubernetes-replicator

- Repository: <https://github.com/mittwald/kubernetes-replicator>
- License: Apache-2.0。
- 重点借鉴：跨 Namespace 的资源选择、Annotation 约定与复制循环规避。
- 不采用：其 ConfigMap/Secret 复制模型。CronJob Catalog 分发由 Kyverno 首次生成，image/suspend/sync 状态必须由 JobPilot 单独拥有。

### OpenKruise AdvancedCronJob

- Repository: <https://github.com/openkruise/kruise>
- License: GitHub License 元数据未识别；如需直接复用源码，必须在锁定版本时人工核验。
- 重点借鉴：`AdvancedCronJob` 的 JobTemplate、状态条件、子资源关系和 Controller 分层；重点阅读 `pkg/controller/advancedcronjob/`。
- 不采用：AdvancedCronJob CRD、它自己的调度循环、BroadcastJob/ImagePullJob 等扩展资源。JobPilot 只同步原生 `batch/v1 CronJob` 的 image。

### KEDA ScaledJob

- Repository: <https://github.com/kedacore/keda>
- License: Apache-2.0。
- 重点借鉴：JobTemplate/Status/Conditions 数据模型、事件记录、Leader Election、并发 Reconcile 和 namespace watch。
- 不采用：Scaler、事件触发扩缩容和 ScaledJob CRD。镜像跟随由 Deployment 变更触发，而不是由 KEDA scaler 触发。

### Metacontroller

- Repository: <https://github.com/metacontroller/metacontroller>
- License: Apache-2.0。
- 重点借鉴：观察资源并计算期望状态的极简思想。
- 不采用：Webhook 控制面。JobPilot 已经是 Go 服务并需要 controller-runtime/client-go，增加 Metacontroller 会引入额外部署和故障链路。

## 7. 最终架构选择

JobPilot 从 kubernetes-job-ui 与 kubecron 借鉴直接资源操作，从 Kronic 借鉴任务管理 UI 流程，从 Onax 借鉴派生状态和 cron 计算边界，并将 CronJob Guardian 的长期监控能力明确留在后续版本。Workload Sync Controller 与 Image Admission Guard 借鉴 Kubebuilder/controller-runtime 的工程骨架、OpenKruise 的资源/状态组织、Reloader 的 Annotation 关联和 Mondoo Operator 的局部 CronJob 收敛细节，但不复制源码、不创建 CRD、不实现 Scheduler、不生成自定义 Job 资源。

任务支持三种镜像策略：

```text
Fixed
  wms-jobs 等独立任务 Chart 自己声明并随 Helm 发布

FollowWorkload
  wms-app 与 Catalog 任务使用同一个业务镜像，由 jobpilot-controller 同步 Deployment image 并阻止旧 image Job

Manual
  普通原生 CronJob，JobPilot 不接管 image
```

其中 FollowWorkload 是本次 XXL-Job 迁移中同镜像、Catalog 分发的默认策略；Fixed 用于已经独立成业务域任务项目的任务；Manual 保留 Kubernetes 原生资源的兼容性。
