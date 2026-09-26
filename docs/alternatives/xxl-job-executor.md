# XXL-Job Kubernetes Native Executor 备选方案

> 本文是备选架构，不是当前主方案。
>
> 当前主方案见 [XXL-Job 迁移计划](../xxl-job-plan.md) 与 [XXL-Job 技术设计](../xxl-job-design.md)。
> 本方案保留 XXL-Job Admin 作为控制面，将业务 Executor 的 JVM JobThread 执行模型替换为 Kubernetes Job 执行模型。

## 1. 方案定位

当前主方案：

~~~text
Kubernetes CronJob → Kubernetes Job → JobPilot / 业务镜像
~~~

本备选方案：

~~~text
XXL-Job Admin → xxl-k8s-executor → Kubernetes Job → 业务镜像中的 @XxlJob Handler
~~~

核心职责：

| 组件 | 职责 |
| --- | --- |
| XXL-Job Admin | 任务控制面：Cron、参数、重试、历史、UI、权限、任务启停 |
| xxl-k8s-executor | XXL Executor 协议适配和 Kubernetes Job 执行桥梁 |
| Kubernetes Job / Pod | 一次任务执行、调度、超时、资源隔离和生命周期 |
| 业务 Handler | 最终业务逻辑和业务级幂等 |

本方案不使用 Kubernetes CronJob，因此没有双调度器。它适合希望保留 XXL-Job 既有操作体验，同时把重任务从在线 Deployment 中隔离出来的迁移路径。

## 2. XXL-Job 代码基线

本方案以现有 XXL-Job 2.x Executor 模型为兼容基线。需要区分 XXL-Job 的三个层次：

| 代码层 | 现有行为 | 备选方案的替换点 |
| --- | --- | --- |
| Admin 调度层 | 生成 `xxl_job_log`，按 `appname` 选择 Executor 地址，发送 `TriggerParam` | 保留 |
| Executor 协议层 | `beat`、`idleBeat`、`run`、`kill`、`log` | 由 `xxl-k8s-executor` 实现 |
| 业务执行层 | `XxlJobSpringExecutor` 扫描 `@XxlJob`，注册 `MethodJobHandler`，由 `JobThread` 串行执行 | 改为 Job Pod 内一次性 Runner |

现有业务 Executor 的典型启动链路是：

~~~text
XxlJobSpringExecutor 初始化
  → 扫描 @XxlJob 方法
  → 注册 handlerName → MethodJobHandler
  → 向 Admin 注册 appname / address
  → 暴露 ExecutorBiz HTTP 接口
~~~

Admin 触发链路是：

~~~text
XxlJobTrigger
  → 创建 xxl_job_log，得到 logId
  → 根据 xxl_job_group.appname 查询 xxl_job_registry
  → 选择 Executor address
  → POST ExecutorBiz.run(TriggerParam)
  → Executor 返回“已接受/失败”
  → 执行完成后 POST Admin callback(logId, handleCode, handleMsg)
~~~

现有 Executor 内部的 `ExecutorBizImpl.run` 并不直接执行方法并等待结果。它通常会：

1. 根据 `jobId` 找到已经扫描的 Handler；
2. 创建或取得对应的 `JobThread`；
3. 将 `TriggerParam` 放入该 `JobThread` 的触发队列；
4. 立即返回 `ReturnT`；
5. 由 `JobThread` 调用 `MethodJobHandler.execute()`，完成后再回调 Admin。

因此，Gateway 的 `run` 也必须是“创建一次执行并返回接受结果”，不能把 Kubernetes Pod 的最终成功/失败伪装成同步 HTTP 返回值。

### 2.1 `TriggerParam` 是执行输入，不是完整任务配置

标准 `TriggerParam` 主要携带：

~~~text
jobId
logId
logDateTime
executorHandler
executorParams
executorBlockStrategy
executorTimeout
glueType / glueSource
broadcastIndex / broadcastTotal
failRetryCount
~~~

其中：

- `executorHandler` 对应 `@XxlJob("...")` 的 Handler 名称；
- `executorParams` 对应 XXL-Job 页面填写的任务参数；
- `logId` 是本次执行的 Admin 日志 ID；
- `appname` 不在标准 `TriggerParam` 中，而是在 Admin 选择 Executor 地址时使用；
- `executorTimeout`、`executorBlockStrategy` 是触发语义输入，不能直接当作 Kubernetes 字段无条件透传。

这解释了为什么“所有 appname 共用一个 Gateway 地址”存在上下文丢失问题：Gateway 收到标准 `run` 请求时，不能仅从请求体知道它属于哪个 Executor Group。

### 2.2 现有 Handler 返回值不能作为统一成功标准

`MethodJobHandler.execute()` 反射调用目标方法，业务方法常见返回类型是 `void`。现有 XXL 结果主要来自：

- 方法抛出异常；
- `XxlJobHelper.handleSuccess()` / `handleFail()` 设置的 `XxlJobContext` 状态；
- Executor 在 `JobThread` 完成后生成 `HandleCallbackParam`。

因此，Job Runner 必须保留 `XxlJobContext` 和 `XxlJobHelper` 语义，并同时把最终状态转换为进程退出码：

| Handler 结果 | `XxlJobContext` | Pod 进程 |
| --- | --- | ---: |
| 正常返回，未标记失败 | success | 0 |
| `handleSuccess()` | success | 0 |
| `handleFail()` | fail | 非 0 |
| 抛出未处理异常 | fail | 非 0 |

仅仅调用一个 `void` 方法并观察容器是否启动成功是不够的。现有“捕获异常后只打印日志”的 Handler 还可能被判定为成功，必须在迁移清单中逐个审计。

## 3. 问题与目标

当前 Executor 位于业务 Pod：

~~~text
业务 Deployment Pod
  ├── Spring Boot Web
  ├── MQ Consumer
  ├── XXL Executor
  └── JobThread / @XxlJob Handler
~~~

这会使定时任务与在线服务共享 CPU、Memory、JVM Heap 与 GC；重任务、OOM 或 Full GC 会影响在线业务。

目标执行模型：

~~~text
XXL trigger
  → xxl-k8s-executor
  → Kubernetes Job
  → 独立任务 Pod
  → 同一业务镜像的 Job mode
  → @XxlJob Handler
~~~

任务 Pod 可以使用独立 resources、activeDeadlineSeconds、TTL、节点约束和生命周期，不影响业务 Deployment Pod。

## 4. 总体架构

~~~text
                         XXL-Job Admin
                ┌────────────────────────┐
                │ UI / Cron / 参数 / 重试 │
                │ 历史 / 权限 / 手工触发  │
                └───────────┬────────────┘
                            │ Executor Protocol
                            ▼
                ┌────────────────────────┐
                │   xxl-k8s-executor     │
                │ protocol adapter        │
                │ app/workload resolver   │
                │ job builder             │
                │ job watcher             │
                │ callback controller     │
                │ log proxy               │
                └───────────┬────────────┘
                            │ Kubernetes API
                            ▼
                       Kubernetes Job
                            ▼
                           Pod
                            ▼
                    Business Job Runner
                            ▼
                     existing @XxlJob
~~~

xxl-k8s-executor 建议采用 Go、client-go 与 controller-runtime，实现为无状态 Deployment。它不保存业务数据，不维护内存队列，也不需要独立数据库。

## 5. XXL Executor 协议与统一 Gateway

标准 ExecutorBiz 的实际方法和 HTTP 路径通常如下；“触发”是业务概念，协议方法名是 `run`：

| 协议方法 | 典型路径 | 现有 Executor 语义 | Gateway 实现 |
| --- | --- | --- | --- |
| `beat()` | `/beat` | Executor 存活 | 返回 Gateway 就绪状态；不得以业务 Pod 存活替代 |
| `idleBeat(jobId)` | `/idleBeat` | 对应 `JobThread` 是否空闲 | 查询 `(app, jobId)` 是否存在 Active Kubernetes Job |
| `run(TriggerParam)` | `/run` | 入队后立即返回接受结果 | 校验、幂等创建 Kubernetes Job 后立即返回 |
| `kill(jobId)` | `/kill` | 停止该 JobThread | 见第 11.3 节的协议限制 |
| `log(logDateTim, logId, fromLineNum)` | `/log` | 读取 Executor 本地日志文件 | 根据 `logId` 查询 Pod stdout/stderr 或日志平台 |
| `logDateTimely(...)` | `/logDateTimely` | 判断日志日期是否可用 | 以 Pod / 外部日志保留策略实现 |

XXL Admin 向 Executor 的调用是 HTTP JSON 请求，由 `ExecutorBizClient` 发起；Admin 并不等待 Handler 完成。Gateway 必须维持 `ReturnT` 的兼容语义：

- `run` 成功只表示该 `logId` 已被安全接管（已存在的同一 Job 也算接管成功）；
- Job 创建失败、映射非法、Handler 不存在等不可恢复问题，`run` 返回失败；
- 业务执行成功、失败、超时一律通过后续 Admin callback 反映；
- 不得用“Pod 尚未启动”作为 `run` 的失败条件，否则会错误触发 Admin 重投。

统一部署：

~~~text
xxl-k8s-executor
  replicas: 2
  Service: xxl-k8s-executor
~~~

多个 XXL Executor Group 可以复用同一 Gateway Deployment，但不能假设标准 trigger 请求一定携带 appname。MVP 必须先解决 Group 到业务 Workload 的映射，否则 Gateway 无法安全区分 WMS、OMS 等目标。

允许的实现路径三选一：

1. 每个 Executor Group 使用独立 Service DNS，但所有 Service 都指向同一 Gateway Deployment；
2. Gateway 根据 jobId 查询 XXL Admin 任务配置，解析 Executor Group；
3. 在确认 XXL 协议兼容性后，扩展内部请求并显式传入 appname。

未解决前，不得将多个 Group 无条件配置为同一个无上下文入口。

### 5.1 注册模型必须随之调整

当前业务服务通常由 `XxlJobSpringExecutor` 的注册线程将：

~~~text
appname + executorAddress
  → xxl_job_registry
~~~

注册到 Admin。迁移到本方案后，业务 Deployment 不应继续注册自身地址；否则 Admin 可能把同一 `appname` 的任务随机发往旧业务 Pod 或 Gateway，导致同一任务存在两种执行路径。

Gateway 需要替代原注册职责。对于“同一 Gateway Deployment 服务多个 appname”的安全实现，推荐：

~~~text
xxl-k8s-executor
  ├── Service xxl-wms-executor  → Gateway Pod
  ├── Service xxl-wes-executor  → Gateway Pod
  └── Service xxl-oms-executor  → Gateway Pod

xxl_job_registry
  wms-executor → http://xxl-wms-executor:9999/
  wes-executor → http://xxl-wes-executor:9999/
  oms-executor → http://xxl-oms-executor:9999/
~~~

Gateway 按请求 Host / Service 入口取得 appname，再使用受控映射解析 Workload。入口层必须确保 Host 被保留；若 Ingress、L4 代理或 Admin 地址配置不能可靠保留该上下文，就改用“Gateway 以 `jobId` 查询 Admin 配置”的方式，而不是猜测。

注册方式只能二选一并固定：

| 模式 | 责任 | 要求 |
| --- | --- | --- |
| 自动注册 | Gateway 按 appname 周期注册、摘除 | 需要为每个 appname 维护明确 address；Gateway 优雅退出时注销 |
| 手工地址 | `xxl_job_group.address_list` 配置固定 Service DNS | 禁用对应业务 Executor 的自动注册，变更由部署流程审核 |

不得同时让旧业务 Executor 和 Gateway 自动注册到同一个 `appname`。

## 6. AppName 到 Workload 映射

第一期使用 ConfigMap：

~~~yaml
apps:
  wms-executor:
    namespace: prod-workspace
    workload:
      kind: Deployment
      name: wms
      container: wms
  oms-executor:
    namespace: prod-workspace
    workload:
      kind: Deployment
      name: oms
      container: oms
~~~

解析链路：

~~~text
Executor Group / appname
  → Namespace
  → Deployment
  → source container
~~~

后续可改为 Deployment Annotation 自动发现。请求方不能任意指定 Namespace、Deployment、image、ServiceAccount、hostPath 或 privileged 配置；所有目标均必须来自受控映射。

## 7. Deployment 作为运行时模板

每次 Trigger 读取 Deployment 当前 PodTemplate：

~~~text
KubeSphere 更新 Deployment/wms
  image: v3.6.4 → v3.6.5

下一次 XXL Trigger
  → 读取 Deployment image = v3.6.5
  → 创建使用 v3.6.5 的 Kubernetes Job
~~~

建议白名单继承：

~~~text
image
imagePullPolicy
env / envFrom
volumes / volumeMounts
imagePullSecrets
nodeSelector / tolerations
必要的 securityContext
~~~

禁止直接复制：

~~~text
replicas
ports
readinessProbe / livenessProbe / startupProbe
Deployment strategy
~~~

Job 专有内容通过 App/Handler Profile 显式配置：

~~~text
command / args
resources
restartPolicy
backoffLimit
activeDeadlineSeconds
ttlSecondsAfterFinished
~~~

serviceAccountName、Pod Security Context、节点策略和 Secret 引用不能无条件继承，必须使用显式白名单和最小权限评审。Gateway 自身不读取 Secret/ConfigMap，只复制受控引用。

MVP 必须明确镜像策略：

| 策略 | 行为 |
| --- | --- |
| DesiredImage | 读取 Deployment spec.template，发布后立即用于新 Job |
| ReadyImage | 仅在 Deployment 对应 generation 可用后才用于新 Job |

两种策略不能隐式混用。

## 8. 业务镜像与 Spring Job Runner

同一个业务镜像支持两种模式：

~~~text
server mode
  启动 Web、MQ、常驻业务服务

job mode
  --xxl-job-mode=true
  --xxl-job-handler=timeoutOrder
  执行一个 Handler 后退出
~~~

Job mode 的行为：

~~~text
启动 Spring Context
  → 禁止 Web Server、MQ Consumer、XXL 注册/心跳和无关定时线程
  → 创建 XxlJobContext
  → 查找指定 @XxlJob Handler
  → 执行业务代码
  → 等待 Future、线程池与事务结束
  → 输出 stdout/stderr
  → 退出 JVM
~~~

建议公共依赖为 `xxl-k8s-runner-spring-boot-starter`。它只复用业务侧的 Handler 扫描与上下文语义，不启动完整 `XxlJobSpringExecutor`。原因是后者会启动 Executor HTTP Server、Admin 注册/心跳、Callback 线程与 `JobThread`，与 Gateway 职责重复。

Runner 的最小内部流程应明确为：

~~~text
读取环境变量 / 文件化 TriggerParam
  → 创建 XxlJobContext(logId, logDateTime, params, shard index/total)
  → Spring 扫描或注册 handlerName → MethodJobHandler
  → 调用指定 Handler 一次
  → 读取 XxlJobContext 的 handleCode / handleMsg
  → 写 execution-result.json（给 Gateway 解析）
  → 以对应退出码结束
~~~

建议由 Gateway 将非敏感执行输入作为环境变量或只读文件注入：

~~~yaml
env:
  - name: XXL_JOB_ID
    value: "123"
  - name: XXL_LOG_ID
    value: "998822"
  - name: XXL_LOG_DATE_TIME
    value: "1720000000000"
  - name: XXL_HANDLER
    value: "timeoutOrder"
  - name: XXL_EXECUTOR_PARAMS
    value: "..."
  - name: XXL_BROADCAST_INDEX
    value: "0"
  - name: XXL_BROADCAST_TOTAL
    value: "1"
~~~

`executorParams` 可能包含业务敏感信息，不能放入 Job Label、Annotation、命令行参数或 Gateway 日志。长度超过环境变量可接受范围或涉及敏感信息时，应生成按 `logId` 命名、最小权限挂载、并由 TTL 清理的 Secret；该 Secret 不能成为长期执行记录。

Runner 必须兼容现有 `XxlJobHelper` 的 `getJobParam()`、分片索引/总数、`handleSuccess`、`handleFail` 与日志调用。对 `XxlJobHelper.log()` 的处理有两种选择：

1. 输出结构化 stdout，由 Gateway / 日志平台按 `logId` 检索；
2. 实现同一进程内的临时日志追加器，再由 Gateway 转换为 XXL LogData。

第一期建议选择 stdout。不要复制原 Executor 的本地 `${logpath}/${logDate}/${logId}.log` 文件模型，因为 Kubernetes Pod 本地磁盘随 Pod 回收而消失。

Runner 不应让业务镜像持有 Kubernetes API 权限。

失败规范：

| 情况 | 进程退出码 |
| --- | ---: |
| 完整成功或安全 no-op | 0 |
| 未知 Handler、配置错误、业务异常 | 非 0 |
| handleFail 或异步阶段失败 | 非 0 |

### 8.1 业务代码改造边界

现有 `@XxlJob` 方法在常驻服务的 `JobThread` 中执行；Job mode 只会执行一次并退出。因此每个 Handler 要通过以下检查后才能迁移：

| 检查项 | Job mode 要求 |
| --- | --- |
| 数据库、Redis、MQ、配置 | 使用与业务 Deployment 等价的受控 ConfigMap / Secret 引用 |
| Web / MQ 依赖 | 不启动 Web Server 和 Consumer；Handler 所需 Bean 仍可初始化 |
| 异步线程 | Runner 必须等待 Future / 线程池完成，否则 JVM 退出会截断任务 |
| 本地文件 | 不依赖 Deployment Pod 的临时磁盘；需要时显式挂载 PVC / 对象存储 |
| `@Scheduled`、SSE、心跳 | 不迁移到一次性 Job；保留在 Deployment 或重构为独立服务 |
| 业务防重 | 使用 `logId` 或业务 execution ID 作为唯一键 / 锁 |

例如已识别的 `wes-v2.stationSseHeartbeat` 依赖常驻 JVM 的本地 SSE emitter，不适合作为一次性 Kubernetes Job；`stationSessionCleanupJob`、`stationSyncJob` 才是可优先 PoC 的候选。最终名单仍需以 `xxl_job_info`、`xxl_job_group`、`xxl_job_registry` 和实际 Handler 依赖审计为准。

## 9. Trigger 与 Execution Identity

触发流程：

~~~text
XXL Admin POST /run (TriggerParam)
  → 从接入 Service Host 或 jobId 解析 appname / Group
  → 校验 jobId、logId、handler、参数、timeout、blockStrategy
  → 查询受控 App Mapping
  → 读取 Deployment PodTemplate
  → 构建 Kubernetes Job
  → Create Job
  → 返回 ReturnT.SUCCESS（已接管）或 ReturnT.FAIL（未接管）
~~~

Gateway 在返回成功前必须完成“可恢复地接管”这件事，即 Job 已创建，或确认同名 Job 已由本次 `logId` 创建。不能先回成功再异步投递到内存队列。

强制使用 XXL logId 作为 Execution Identity：

~~~text
1 logId
  → 1 execution
  → 1 Kubernetes Job
~~~

Job 名采用确定性规则：

~~~text
xxl-<jobId>-<logId>
~~~

不使用 GenerateName。若数字 ID 组合超过 Kubernetes 名称限制，使用稳定哈希压缩，并将完整 jobId/logId 写入 Annotation。

Job metadata 至少包含：

~~~yaml
metadata:
  labels:
    xxl-job.io/job-id: "123"
    xxl-job.io/log-id: "998822"
    xxl-job.io/appname: wms-executor
    xxl-job.io/handler: timeoutOrder
    xxl-job.io/managed-by: xxl-k8s-executor
  annotations:
    xxl-job.io/callback-status: pending
    xxl-job.io/log-date-time: "1720000000000"
    xxl-job.io/params-sha256: "<digest>"
    xxl-job.io/template-image: "registry.example/wms@sha256:..."
~~~

`params-sha256` 只用于重复 Trigger 一致性校验，原始参数不得写入 Label、Annotation、Event 或 Gateway 日志。建议另外记录 Runner / Gateway 版本与 Deployment `metadata.uid`、`generation`，用于排障“该执行用了哪个发布版本”。

### 9.1 Job 构建后的不可变性

Kubernetes Job 的 PodTemplate 大部分字段不可原地更新。Deployment 更新只影响**下一次** `run` 创建的 Job，不会改变已经创建或运行中的 Job。

为避免同一执行被错误覆盖：

- Job Name、`jobId`、`logId`、Handler、参数摘要、来源 Workload UID 必须在第一次创建后视为不可变；
- 同名 Job 已存在且校验不一致，Gateway 必须返回失败并告警，而不是 Patch Job Template；
- `backoffLimit`、`activeDeadlineSeconds`、TTL、资源 Profile 在创建时固定；
- Job Pod 失败后不要由 Gateway 复制一个随机新 Job；重试只能由 Admin 产生新的 `logId`。

## 10. 三层防重与重试边界

### 第一层：XXL 调度层

XXL Admin 负责自身调度锁，Gateway 不重复实现 Scheduler Lock。

Admin 在创建 `xxl_job_log` 后可能因网络超时、Executor 返回丢失或故障转移而重复调用 `/run`。这不是新的计划执行，Gateway 必须用相同 `logId` 的确定性 Job 名处理为第二层幂等。

### 第二层：Trigger 幂等

相同 logId 再次触发时：

~~~text
Create deterministic Job
  → AlreadyExists
  → GET existing Job
  → 校验 jobId、logId、appname、handler、参数摘要
  → 一致：幂等成功
  → 不一致：冲突并告警
~~~

绝对禁止 AlreadyExists 后使用随机名称再次创建。

### 第三层：业务幂等

Kubernetes Runtime 为 At-Least-Once，不承诺 exactly-once。关键业务必须以 logId 为 execution_id 实现唯一约束、业务锁、checkpoint 或可重入 upsert。

~~~sql
CREATE TABLE job_execution (
  execution_id bigint PRIMARY KEY,
  job_id bigint NOT NULL,
  handler varchar(128) NOT NULL,
  status varchar(32) NOT NULL
);
~~~

该执行表属于业务系统，不是 Gateway 的平台数据库。

### XXL Retry 与 Kubernetes Retry

MVP 使用：

~~~yaml
backoffLimit: 0
restartPolicy: Never
~~~

失败由 Callback 回传 XXL，XXL Retry 产生新的 logId 和新的 Job。相同 logId 的重复 Trigger 与新 logId 的 Retry 必须严格区分。

`backoffLimit: 0` 使“一个 XXL `logId` 对应一个 Pod 尝试”成立。若设置 Kubernetes `backoffLimit > 0`，一个 logId 会产生多个 Pod 尝试，而 XXL 只看到一次最终 Callback；除非设计额外的尝试级审计和业务幂等规则，否则不应启用。

## 11. Callback、日志与 Kill

### 11.1 Callback

~~~text
Job Complete / Failed
  → Job Watcher
  → Result Parser（condition + exit code + result file）
  → HandleCallbackParam(logId, logDateTime, handleCode, handleMsg)
  → AdminBizClient.callback(...)
~~~

Callback 是 At-Least-Once，不是 Exactly-Once。Job Annotation 中记录：

~~~text
pending → sending → success
~~~

状态领取必须使用 resourceVersion 乐观并发或 Lease/CAS。多个 Gateway 可以同时发现终态 Job，但只有领取成功的副本发送 Callback。sending 超时和 Gateway 崩溃后必须可重新领取。

若 Callback 已送达但写 success Annotation 前进程崩溃，后续重复 Callback 是允许的；XXL Admin 必须根据同一 logId 安全处理重复终态回调。

Gateway 重启/重新选主后，List/Watch 已完成且 callback-status 不为 success 的受管 Job，恢复 Callback。不能仅依赖 watch 事件：watch 断开期间完成的 Job 必须可由周期性 List 补偿发现。

结果判定优先级必须固定：

| 观察到的状态 | 回调结果 |
| --- | --- |
| `activeDeadlineSeconds` 到期 / Job `FailureTarget` 为 DeadlineExceeded | timeout / fail |
| Pod 容器退出码非 0 | fail |
| Runner 写入 `handleFail` 结果 | fail |
| Job Complete 且 Runner 成功退出 | success |
| Job 被 Kill / 删除 | killed / fail，按 Admin 能识别的失败消息回调 |

XXL Admin 的 callback 接口以 `logId` 关联 `xxl_job_log`，因此 Gateway 不需要自建执行数据库；但 Job 的 `callback-status`、终态 condition 与结果文件必须保留到 callback 成功或达到人工告警阈值。

### 11.2 日志

~~~text
XXL log(logId)
  → Gateway 找到确定性 Job
  → 查询 Job Pod
  → 读取 Pod stdout/stderr
  → 转换为 XXL LogData
~~~

Pod 被 TTL 清理后不能再可靠读取 Pod Logs。若 XXL UI 需要长期日志，必须接入 VictoriaLogs 等日志系统，并定义 logId 到日志标签的查询；否则文档必须声明日志仅在 Pod 保留期可查看。

### 11.3 Kill

这里必须保留一个标准协议限制：XXL ExecutorBiz 的 `kill` 入参只有 `jobId`，**不携带 `logId`**。而标准本地 Executor 是按 `jobId` 持有一个 `JobThread`，所以其接口天然只能停止该任务当前线程/队列，不能精确选择历史中的某一次执行。

因此，下述“按 `logId` 精确 Kill”不能在不改 Admin 协议的前提下宣称实现：

~~~text
标准 XXL kill(jobId)
  → 查找 (appname, jobId) 的 Active Kubernetes Job
  → 仅在最多一个 Active Job 时删除该 Job
~~~

MVP 的兼容限制：

- 每个 `(appname, jobId)` 同时最多允许一个 Active Kubernetes Job；
- 不支持分片广播并发、同任务并行分片和多个 Active Retry；
- `kill(jobId)` 找不到 Active Job 时返回幂等成功 `AlreadyStopped`；
- 找到多个 Active Job 时返回失败并告警，绝不猜测删除其中一个。

如果业务要求精确终止某一个 `logId`，必须同时修改 Admin 与 Gateway，增加内部扩展接口 `kill(jobId, logId)`；这已不再是“纯标准 XXL Executor 协议兼容”，应作为后续定制能力单独评审。

## 12. Timeout 与 Block Strategy

现有 `JobThread` 会根据 `executorTimeout` 限制一次 Handler 执行。Kubernetes 的 `activeDeadlineSeconds` 从 Pod 开始运行后计时，且终止前还有 grace period；它不能单独等价于 XXL timeout。

MVP 采用双层超时：

~~~text
executorTimeout
  → Runner 内部 Future / Cancellation 超时（业务语义 timeout）
  → activeDeadlineSeconds = timeout + 终止宽限余量（Pod 兜底回收）
~~~

Runner 发生超时时必须写出 timeout 结果并以非 0 退出；若 Runner 卡死，则由 `activeDeadlineSeconds` 终止 Pod，Result Parser 根据 Job condition 标记 Timeout，再 Callback XXL。`executorTimeout = 0` 代表不设置 Runner 业务超时，但仍应允许每个 App Profile 配置平台级最大 `activeDeadlineSeconds`，防止无限运行。

XXL 的 Block Strategy 是 Admin 随 `TriggerParam` 下发的执行语义，不是 Kubernetes Job 的 `concurrencyPolicy`。Gateway 不得静默忽略不支持的策略。

### DISCARD_LATER

这是 MVP 唯一支持的策略。若同一 `(appname, jobId)` 有活动 Execution，Gateway 返回失败/忙；Admin 将按自身语义记录本次调度失败。查询和创建间需要 Lease/CAS 或等价原子保护，不能只 List Active 后 Create。

锁的粒度为 `appname + jobId`，而非全局锁。Lease 名可使用稳定哈希；Lease 只用来保护“检查 Active + 创建 Job”的临界区，不作为执行状态来源。

### COVER_EARLY

后续才支持。找到该 jobId 的活动 Job，等待其被标记终止，再创建新 logId 的 Job。仅适用于业务支持中断的任务。

需要明确其风险：Kubernetes 删除 Job 是异步的，旧 Pod 可能在 termination grace period 内继续执行。因此必须等旧 Job 不再 Active 或业务 execution lock 已释放，不能“Delete 后立刻 Create”并声称不会重叠。

### SERIAL_EXECUTION

MVP 不支持。标准 `JobThread` 会把多个 Trigger 放入内存队列，而 Gateway 不得复刻内存队列；Gateway 重启会丢失等待中的执行。

后续若要支持，必须有持久化队列语义。可将尚未开始的执行表示为 `spec.suspend: true` 的 Kubernetes Job，并以资源版本/CAS 或 Lease 确保同一 jobId 任意时刻最多一个 unsuspended Job；但这需要完整设计取消、超时、Admin callback 和顺序保证，不能作为简单开关加入 MVP。

## 13. 高可用、资源与安全

Gateway 以至少两个副本运行。任一副本 Crash 不影响已创建 Job；确定性 Job Name 防止另一副本重复创建相同 logId 的 Job。Gateway 不得使用本地 Map、内存 Queue 或本地 Callback State 作为事实来源。

Deployment 副本数不影响 Job 数量：

~~~text
Deployment replicas = 10
  ≠ 10 个任务

一次 XXL Trigger
  → 一个 logId
  → 一个 Kubernetes Job
~~~

资源 Profile 可按 appname 配置，后续再按 handler 覆盖。任务 Job 的 OOM 或 CPU 压力不应影响在线 Deployment。

最小 RBAC：

| Resource | Verbs |
| --- | --- |
| deployments | get, list, watch |
| jobs | get, list, watch, create, patch, update, delete |
| pods | get, list, watch |
| pods/log | get |
| leases | get, list, watch, create, update, patch |

XXL 到 Gateway 使用 XXL AccessToken。Gateway 必须验证访问令牌，并以受控 App Mapping 限制可操作资源，避免成为通用 Kubernetes Job RCE 接口。executorParam 只能进入白名单环境变量或 Runner 参数，严禁拼接到 shell。

## 14. 分阶段与 PoC

### MVP

- beat、idleBeat、trigger、kill、log；
- Group/AppName 到 Deployment 映射；
- Deployment Template 到 Job 构建；
- One-shot Spring Runner；
- 确定性 Job Name 与 Trigger 幂等；
- Job Watch、Success/Failed/Timeout Callback；
- Callback Retry/Recovery；
- backoffLimit=0；
- Gateway HA、Metrics；
- DISCARD_LATER。

### 第二阶段

- SERIAL_EXECUTION、COVER_EARLY；
- Resource Profile；
- Deployment Annotation Discovery；
- VictoriaLogs 长期日志；
- 更完善的 Callback 状态恢复。

### 第三阶段

- Sharding Broadcast 与 Indexed Job；
- 多集群、跨区域、Quota、Priority 和复杂资源策略。

PoC 必须验证：

1. 现有 @XxlJob Handler 可由 Job mode 调用且正确退出；
2. XXL UI 可通过 Gateway 查看仍保留 Pod 的日志；
3. 相同 logId Trigger 两次、两个 Gateway 并发 Trigger、创建后 HTTP 返回丢失时，只存在一个 Job；
4. Gateway 在 Create Job 后或 Callback 中崩溃时，Job 不丢失且最终 Callback；
5. XXL Admin 暂不可用与重复 Callback 不会产生重复业务执行。

## 15. 验收与非目标

验收标准：

- 同一个 logId 只能对应一个 Kubernetes Job；
- Gateway 多副本和重启不会重复创建 Job；
- XXL Retry 使用新 logId 创建新 Job；
- 任务 OOM 不影响在线 Deployment；
- Deployment 更新后下一次任务使用选定策略对应的镜像；
- XXL UI 正确显示 Success、Failed、Timeout，并能在声明的保留期内查看日志；
- Gateway 无业务数据库，不创建 Kubernetes CronJob；
- 不存在 XXL 与 Kubernetes CronJob 的双调度。

明确不做：

- 新任务 UI、新 Cron Scheduler、新权限系统、新任务数据库；
- Kubernetes CronJob、ScheduledJob CRD、CronJob Sync Controller；
- 每业务一个独立 Gateway；
- 基础设施级 exactly-once 保证。

## 16. XXL-Job 源码映射与实现边界

下表是 Gateway / Runner 设计必须对照的 XXL-Job 核心源码职责。类名和包路径以 XXL-Job 2.x 的常见代码结构为准；落地前需要再对已部署 `scheduling-dispatcher` 的实际版本和二次定制进行一次逐文件比对。

| 现有类 / 模块 | 现有职责 | Gateway / Runner 对应实现 |
| --- | --- | --- |
| `xxl-job-admin/.../core/thread/JobScheduleHelper` | 预读 Cron、创建调度触发 | 保留在 Admin，不复制 |
| `xxl-job-admin/.../core/trigger/XxlJobTrigger` | 组装 `TriggerParam`、选择 Executor 地址、发起 `run` | Gateway 接收其请求；不重做调度选择 |
| `xxl-job-admin/.../core/complete/XxlJobCompleter` | 处理回调、更新 `xxl_job_log` | 保留；Gateway 只发送 `HandleCallbackParam` |
| `xxl-job-core/.../biz/ExecutorBiz` | 定义 `beat`、`idleBeat`、`run`、`kill`、`log` 等协议 | Gateway 兼容该契约 |
| `xxl-job-core/.../biz/client/ExecutorBizClient` | Admin 侧 HTTP 请求客户端 | 无需复制；用于验证路径、请求体和 `ReturnT` 行为 |
| `xxl-job-core/.../biz/impl/ExecutorBizImpl` | Executor 协议入口，创建 / 管理 `JobThread` | Gateway 的 HTTP adapter；不创建本地 `JobThread` |
| `xxl-job-core/.../executor/impl/XxlJobSpringExecutor` | 扫描 `@XxlJob`、注册地址、启动 Executor 服务 | server mode 保留；job mode 禁用其网络与注册部分 |
| `xxl-job-core/.../thread/JobThread` | 触发队列、block strategy、超时、方法执行、callback | 用 Kubernetes Job + Runner + Watcher 拆分替代 |
| `xxl-job-core/.../handler/impl/MethodJobHandler` | 反射调用 `@XxlJob` 方法 | Runner 内复用 / 等价实现 |
| `xxl-job-core/.../context/XxlJobContext`、`XxlJobHelper` | 参数、分片、日志、成功/失败状态 | Runner 必须兼容的业务 API |

### 16.1 已知的定制代码影响

此前对 `wms/scheduling-dispatcher` 的代码扫描表明，它是 XXL-Job Admin/调度中心，而不是业务 Executor；业务 Handler 位于 WMS、WES 等服务。其已有的基于 Handler 的 Redis 互斥只能处理部分并发触发，不能取代本方案的：

~~~text
logId → deterministic Kubernetes Job → business execution_id
~~~

三层关联。原因是 Redis Handler 锁不表达一次执行身份，也无法在 Gateway 创建成功但 HTTP 响应丢失时判断“是否已经接管同一个 logId”。因此该互斥逻辑可以在过渡期保留为 Admin 行为，但不能复制为 Kubernetes Job 的防重机制。

### 16.2 迁移前必须完成的代码核验

在开发 Gateway 前，针对实际部署版本逐项核验：

1. `ExecutorBiz` 的路径、请求 JSON 字段、AccessToken 头、`ReturnT` 成功/失败码；
2. `run`、`kill`、`log` 是否存在本地二次修改，特别是 `kill` 是否仍只有 `jobId`；
3. `xxl_job_group` 的自动/手工地址模式，以及是否仍有旧业务 Pod 注册到同一 appname；
4. `HandleCallbackParam` 的字段与 Admin 对重复 callback 的处理；
5. 每个 `@XxlJob` 的同步/异步行为、异常处理、`XxlJobHelper` 用法、外部依赖和业务幂等键；
6. `xxl_job_info` 中实际启用任务的路由、阻塞策略、超时、失败重试、分片和 GLUE 类型。

只要发现使用 GLUE 脚本、分片广播、`SERIAL_EXECUTION` 队列语义或依赖本地 Executor 文件日志，就不应直接进入 MVP；应先归类为“保留旧 Executor”或“扩展后迁移”。
