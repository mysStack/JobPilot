# XXL-Job 向 Kubernetes 原生任务体系迁移计划

> 本文是迁移计划（Plan），说明范围、盘点、阶段、交付物、验收和回滚。
> 具体技术方案见 [xxl-job-design.md](xxl-job-design.md)。Kubernetes 负责调度和执行；JobPilot 只提供运行期管理与受限的镜像同步控制器。

## 1. 目标与边界

目标是在保留现有 KubeSphere 发布流程的前提下，逐步下线 XXL-Job Admin、Executor 注册和心跳链路，改由 Kubernetes CronJob、Job 和 Pod 执行周期性任务。

默认执行模式是方案 A：CronJob 使用业务镜像创建一次性 Job Pod，任务入口在 Pod 中直接执行业务代码，并以进程退出码报告结果。镜像同步与旧版本拦截由独立的 `jobpilot-controller`（Workload Sync Controller + Image Admission Guard）完成；它不参与调度和执行。

不在本计划范围内：

- 重建 XXL-Job Admin、Executor、注册中心、任务队列或调度器；
- 引入 GitOps、Argo CD 或新的发布流程；
- 让 JobPilot 参与业务任务调度或执行；JobPilot 只提供运行期管理、Workload Sync Controller 和 Job image Admission Guard；
- 迁移 MQ Consumer、永久轮询 Worker、复杂工作流或人工审批；
- 把只操作 JVM 本地内存的任务改为外部 CronJob。

## 2. 迁移原则

1. Kubernetes 负责 Cron 调度、Job 生命周期、Pod 调度、Pod 重启和历史清理。
2. JobPilot 的 Runtime Sync Controller 只同步关联 Deployment 的任务镜像；Image Admission Guard 只校验受管 Job，不拥有任何调度语义。
3. KubeSphere 中一次 Deployment 镜像更新，必须使后续 CronJob 创建的 Job 使用相同镜像。
4. 已创建的 Job 和已启动的 Pod 永远不原地修改镜像；新版本只影响后续 Job。
5. 每个任务只能有一个启用的调度源；切换期间严禁 XXL-Job 与 CronJob 双跑写任务。
6. @XxlJob 只作为迁移期适配层。业务逻辑必须从 Handler 中抽出，供 Kubernetes Job Runner 调用。
7. 业务幂等、业务锁、checkpoint 和补偿逻辑仍属于业务系统；CronJob 的 Forbid 并发策略不能替代它们。

## 3. XXL-Job 语义转换门禁

方案 A 不兼容“把 `xxl_job_info` 原样改成 CronJob”。XXL-Job 的 `JobScheduleHelper`、`XxlJobTrigger` 和 `JobThread` 共同提供的 Cron、路由、阻塞、重试、参数与 Handler 上下文语义，必须逐任务分类；无法等价转换的任务不进入首批 CronJob。

| XXL-Job 能力 | Kubernetes 原生对应 | 首期处理规则 |
| --- | --- | --- |
| Quartz Cron | CronJob `schedule` + `timeZone` | 只接受可无歧义转换的五字段表达式；秒字段、`L`、`W`、`#` 等 Quartz 扩展必须改写并逐项验收 |
| Misfire | `startingDeadlineSeconds` | 非等价转换；必须明确“错过即跳过”或“窗口内补一次”，不能默认继承 |
| `FIRST`、`LAST`、`ROUND`、`RANDOM` 等路由 | 无 | Job 直接运行指定镜像，不迁移路由策略；保留其业务目的说明 |
| `SHARDING_BROADCAST` | Indexed Job / 多个显式 Job | v0.1 不支持；单独设计后才迁移 |
| `DISCARD_LATER` | `concurrencyPolicy: Forbid` | 仅近似覆盖 CronJob 自身的重叠；业务锁仍必须存在 |
| `COVER_EARLY` | `concurrencyPolicy: Replace` | 仅候选映射；删除旧 Job 异步，不能保证零重叠，需业务支持中断 |
| `SERIAL_EXECUTION` | 无原生持久队列 | v0.1 不支持；不得把 `JobThread` 内存队列改成隐式队列 |
| `executor_timeout` | Runner 超时 + `activeDeadlineSeconds` | 双层处理；Pod 级 deadline 不能单独等同于 XXL Handler 超时 |
| `fail_retry_count` | `backoffLimit` | 非一一映射；首期副作用任务默认 `backoffLimit: 0`，重试由任务级幂等策略显式决定 |
| `executor_param` | 固定 args / ConfigMap / Secret 引用 | 首期仅支持已声明、版本化参数；不支持 XXL UI 任意在线传参 |
| GLUE 脚本 | 无 | v0.1 不迁移；只支持已审计的 Java Bean Handler |
| `XxlJobHelper` | Job Runner 显式输入、结构化日志、异常 | 必须移除对其运行时上下文的依赖，不能在 CronJob 中启动 XXL Executor |

每条任务除“可迁移 / 不迁移”外，还必须产出一份语义转换记录：原 `jobId`、Cron、时区、misfire、路由、阻塞策略、超时、重试、分片、GLUE 类型、参数模式，以及目标 CronJob 的对应配置或“不支持原因”。

### 3.1 必须从 XXL 代码中消除的运行时依赖

方案 A 的 Job mode 不启动 `XxlJobSpringExecutor`、`ExecutorBizImpl`、`JobThread`、注册心跳或 Callback。原 Handler 中的以下调用必须逐项改造：

| 原依赖 | 方案 A 的替代方式 |
| --- | --- |
| `XxlJobHelper.getJobParam()` | 受限的任务参数对象；来自 Chart values、ConfigMap 或 Secret 引用 |
| `XxlJobHelper.getShardIndex()/getShardTotal()` | v0.1 不支持；后续 Indexed Job 需单独设计 |
| `XxlJobHelper.handleSuccess()/handleFail()` | 正常返回 / 抛出异常，最终映射为 0 / 非 0 退出码 |
| `XxlJobHelper.log()` | stdout/stderr 的结构化日志，包含任务名、Job UID 和业务 checkpoint |
| `JobThread` 的队列与超时 | CronJob/Job 生命周期 + Runner 内部超时控制；不保留内存队列 |

## 4. 初始源码任务清单

已扫描 WMS/WES 源码中 @XxlJob 声明，共发现 **31 个 Handler**。该清单是评审起点，不代表它们都在当前环境实际注册或启用。

| 仓库 | 数量 | Handler |
| --- | ---: | --- |
| wms/scheduling | 14 | orderPriorityChange、calculatePickPodTask、calculateConsolidationPodTask、assignPodToteTask、cancelConsolidationLine、matchSection、matchProcessPath、frameAutoBindRebinWall、uploadBackgroundFile、deleteBackgroundFile、receivePodSelection、stocktakingPodSelection、deleteNonCriticalData、syncOmsInventory |
| wms/wms | 8 | retryFeedbackJob、saveStockUnitSnapshot、generateNormalReplenishment、generateUrgentReplenishment、generateSalesRank、callPodByHeat、dealExpiredStock、pushStockAgingToBmsJob |
| wes/wes | 3 | arrangeOriginTask、recordTaskProcess、updateOriginTaskAttr |
| wes/wes-v2 | 3 | stationSessionCleanupJob、stationSyncJob、stationSseHeartbeat |
| wes/ams | 3 | scanDispatcherTask、arrangeWesTask、dispatchArrangedTask |

未发现有效 @XxlJob Handler 的已扫描仓库：wms/web-wms、wms/web-wms-pda、wms/tagadapter、wes/web-wes、wes/hip。wms/scheduling-dispatcher 是 XXL-Job 调度中心，不是业务 Executor。

wes/pod-collating 没有 XXL Handler，但有 Spring @Scheduled 的 ManualWarehouseBatchTriggerCleanupJob.cleanupExpiredTriggers()；它需要单独评审，不计入本次 XXL 迁移数。

## 5. 运行态盘点：形成最终范围

源码存在 Handler 不等于任务正在执行。迁移前必须从 XXL-Job Admin 的只读数据库导出以下信息，并与第 3 节逐项比对：

- xxl_job_group：Executor appname、地址类型和静态地址；
- xxl_job_registry：当前或近期自动注册的 Executor 地址；
- xxl_job_info：任务 ID、Handler、参数、cron、阻塞策略、超时、重试和启用状态；
- xxl_job_log：最近成功/失败、执行时长、重试情况和最后一次业务水位。

每条最终任务记录必须包含：

| 字段 | 用途 |
| --- | --- |
| XXL 任务 ID、Executor appname、Handler、参数、GLUE 类型 | 建立旧新映射和下线依据 |
| cron、时区、misfire、路由、阻塞策略、超时、重试、分片 | 转换 Kubernetes 调度语义 |
| 业务服务、镜像、目标 Namespace、配置来源 | 建立 Job Runner 与 CronJob |
| 数据库/缓存/下游依赖、资源需求、最大时长 | 配置 Job 运行环境和限制 |
| 幂等键、分布式锁、checkpoint、补偿方式 | 防止双跑和重复副作用 |
| 负责人、风险等级、迁移目标、验收和回滚步骤 | 允许按批次上线 |

| 任务情况 | 处理结果 |
| --- | --- |
| 代码、注册和启用配置均存在 | 纳入迁移评审 |
| 代码存在但未配置/未启用 | 确认废弃或补齐配置后再决定 |
| 数据库任务存在但源码或 Executor 不存在 | 作为遗留任务核查并下线，不直接迁移 |
| 仅处理当前 JVM 内存，例如 SSE emitter | 保留在 Deployment 内部调度 |
| 常驻监听、消息消费、复杂编排 | 迁到 Deployment、Worker 或工作流，不创建 CronJob |

## 6. 分阶段计划

### 阶段 0：基线和任务盘点

交付物：最终任务清单、XXL Admin 数据库导出、每个任务近一个业务周期的日志和时长基线。

完成条件：每条已启用任务都能定位到所属代码、负责人和处理分类；不存在“来源不明但仍启用”的任务。

### 阶段 1：Runtime Sync Controller MVP

交付物：独立 `jobpilot-controller` Helm、`jobpilot-jobs-catalog` Helm、Kyverno 首次分发 Policy、无状态 Workload Sync Controller、Image Admission Guard、Annotation 规范、最小 RBAC、Event/日志、至少一个非生产环境 PoC。

MVP 只同步关联 Deployment 指定容器的 image 到 CronJob 指定任务容器的 image，并在 Job 创建时拒绝旧 image。不同步环境变量、挂载、资源、命令、参数、调度表达式或 Secret。

完成条件：Catalog 生成的 CronJob 初始保持 suspend；Controller 同步 Deployment image 后解除 bootstrap suspend；开发通过 KubeSphere 更新 Deployment 镜像后，旧 image Job 被 Guard 拒绝、同步后的后续 Job 使用新镜像；正在运行的 Job 保持旧镜像；Controller 不会修改无关联 Annotation 的 CronJob。

### 阶段 2：业务 Job Runner 标准化

交付物：每个候选服务的 job 启动模式、任务注册表、退出码约定、结构化日志字段、配置校验和测试用例。

完成条件：相同业务 use case 可同时被迁移期 XXL Handler 和 Job Runner 调用；任务异常、配置错误和异步未完成均不能被误报为成功。

### 阶段 3：非生产迁移试点

优先选择低风险、可幂等、执行时间可控的任务。初始 CronJob 必须处于 suspend 状态，先手工创建一次 Job 验证任务入口、配置、权限、数据库副作用、退出码、日志和告警。

完成条件：至少一个完整调度周期的业务结果与 XXL 基线一致；失败能够形成失败 Job；回滚步骤已演练。

### 阶段 4：按服务分批切换

每个任务的切换顺序固定为：停止 XXL-Job → 确认无运行中的旧执行 → 记录业务水位 → 解除 CronJob suspend → 观察一个完整周期 → 验收 → 删除 XXL 配置和适配代码。

wes-v2 的 stationSseHeartbeat 不应进入 CronJob 切换批次：它操作的是本 Pod 内的 SSE emitter，应保留为 Deployment 内的 @Scheduled。

### 阶段 5：下线与复盘

当所有已启用任务都完成验收并且历史保留期结束后，下线对应 Executor 配置、XXL-Job Admin 和仅为 XXL 存在的依赖。保留任务清单、切换记录、日志链接和业务水位，用于审计与复盘。

## 7. 单任务迁移检查表

- [ ] 在 Admin 数据库确认任务处于启用状态，并记录任务 ID、cron、参数、超时、重试和最近执行结果。
- [ ] 核验 Quartz Cron、misfire、路由、阻塞策略、分片和 GLUE 类型；存在非等价语义时先记录处理方案，不得直接生成 CronJob。
- [ ] 确认任务属于方案 A；若不属于，记录其替代目标而不是强行创建 CronJob。
- [ ] 将 Handler 的业务逻辑提取为可复用 use case，不再让 Job Runner 依赖 XXL SDK。
- [ ] 在业务镜像实现 job mode 和稳定任务名，异常必须使进程非零退出。
- [ ] 将 XXL 参数改为受控、版本化的任务输入；确认不依赖任意在线传参、`XxlJobHelper` 分片或 GLUE 脚本。
- [ ] 配置 CronJob 的时区、并发、延迟、超时、重试、历史保留和资源限制。
- [ ] 配置 CronJob 所需的 ConfigMap、Secret、ServiceAccount、镜像拉取凭据和网络访问；这些资源由业务任务定义，不由 Controller 推断。
- [ ] 配置关联 Deployment 的 Annotation，并验证 Controller 只同步镜像字段。
- [ ] 在非生产环境手工运行 Job，核对数据、日志、退出码、锁和幂等性。
- [ ] 停止 XXL 后再启用 CronJob，并观察至少一个完整周期。
- [ ] 记录验收证据、最后业务水位和明确回滚命令。

## 8. 验收标准

- 所有生产启用的 XXL 任务都有最终处置记录；
- 迁移任务由 Kubernetes CronJob 调度，不存在 Executor 注册或心跳依赖；
- Deployment 镜像升级后，关联 CronJob 的后续 Job 使用相同镜像；
- 已运行 Job 不会因同步而变更镜像；
- 业务成功、失败、超时和重试均能从 Kubernetes Job 状态和日志明确识别；
- 不发生 XXL 和 CronJob 的写任务双跑；
- 不存在未审批的 Quartz Cron、GLUE、分片、串行队列或动态参数语义丢失；
- 任务具备业务层幂等、锁或 checkpoint 保护；
- Controller 不具备创建 Job、读取业务 Secret 或调度任务的权限；
- 回滚可以恢复到单一调度源。

## 9. 回滚原则

回滚不是再触发一次任务。固定顺序：暂停新 CronJob → 处理运行中的 Kubernetes Job → 核对业务水位与锁 → 恢复原 XXL 任务 → 验证仅 XXL 调度。Kubernetes Job、Pod 日志和切换记录应保留，不能因回滚而删除审计证据。
