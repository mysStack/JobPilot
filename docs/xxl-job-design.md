# XXL-Job 向 Kubernetes 原生任务体系技术设计

> 本文是技术设计（Design），实现 [xxl-job-plan.md](xxl-job-plan.md) 中的默认方案：
> Kubernetes CronJob/Job 负责任务运行。P0 由独立部署的 `jobpilot-controller` 提供 Workload Sync Controller 和 Image Admission Guard，保证受管 CronJob 使用 Deployment 当前镜像。

## 1. 设计目标

系统必须同时满足：

- 保留 KubeSphere 对业务 Deployment 的现有发布方式；
- 开发只更新 Deployment 镜像，不需要手工修改每个 CronJob 镜像；
- CronJob 创建的 Job Pod 直接执行业务任务代码；
- Job 成功与失败由进程退出码确定，不依赖 HTTP 返回语义；
- 不实现新的 Scheduler、Executor、注册中心、队列或任务管理平台；
- Kubernetes 继续负责调度、Pod 生命周期、重试和资源隔离。
- 受管 CronJob 的新 Job 不得使用与来源 Deployment 不一致的 image。

## 2. 组件与职责

~~~text
KubeSphere 应用商店                 KubeSphere
  │ 安装 Catalog / Controller         │ 更新业务 Deployment image
  ▼                                  ▼
Kyverno 首次分发 CronJob       Deployment ── Watch ── Workload Sync Controller
  │                                  │                     │
  └──────────────────────────────> CronJob <──────────────┘
                                        │
Kubernetes CronJob Controller ──────────┴──> Job Create ──> Image Admission Guard
                                                                  │ 放行
                                                                  ▼
                                                               Job → Pod → Business Job Runner
~~~

| 组件 | 职责 | 明确不负责 |
| --- | --- | --- |
| KubeSphere / 现有发布系统 | 更新 Deployment 期望镜像 | 管理 Cron 调度 |
| Deployment | 承载常驻 API、Consumer 等服务 | 执行一次性 Job 生命周期 |
| JobPilot Jobs Catalog | 定义业务任务模板并通过 Kyverno 首次分发 | 同步目标 CronJob 的运行期字段 |
| Kyverno | 根据 Namespace label 首次生成 Catalog 中的 CronJob | 跟随 Deployment image、调度任务 |
| JobPilot Workload Sync Controller | 从 Deployment 同步 image 到关联 CronJob | 计算 cron、创建 Job、重试、读取业务 Secret |
| JobPilot Image Admission Guard | 拒绝 image 与当前来源 Deployment 不一致的受管 Job | 调度、执行、修改 Job 或 Deployment |
| CronJob Controller | 按 schedule 创建 Job | 选择业务 Handler |
| Job / Pod | 启动任务镜像并保存执行状态 | 发现或注册 Executor |
| Business Job Runner | 按固定任务名执行业务 use case，并以退出码报告结果 | XXL 心跳、回调、路由 |

`jobpilot-controller` 的产品定位是 **Workload Runtime Sync Controller**，不是 Job Scheduler。P0 不依赖未来的 JobPilot Web/API：Catalog 与 Controller 分别作为 KubeSphere 应用商店中的 Helm 应用发布。Controller 只认识通用 Annotation，不包含 WMS、WES 或任何业务任务名。

### 2.1 Helm 发布单元

```text
wms-app
  └── Deployment / Service / Ingress；Deployment image 是唯一版本事实来源

jobpilot-jobs-catalog
  ├── CronJob Catalog（bootstrap image + suspend: true）
  └── Kyverno ClusterPolicy（synchronize: false）

jobpilot-controller
  ├── Workload Sync Controller
  ├── Image Admission Guard
  ├── Service / ValidatingWebhookConfiguration / TLS 证书配置
  └── ServiceAccount、最小 RBAC、leader election
```

目标 Namespace 仅通过 label 接收 Kyverno 生成的 CronJob，不安装 `wms-jobs`、`wes-jobs` 等目标 Helm Release。Catalog 变更采用版本化迁移或删除后重新生成；P0 不使用 Kyverno 持续覆盖目标资源。

### 2.2 开源工程参考与复用边界

| 项目 | 可借鉴的内容 | 不采用的内容 |
| --- | --- | --- |
| `kubernetes-sigs/controller-runtime`、`kubernetes-sigs/kubebuilder` | Manager、cache、Watch/index、leader election、Webhook、envtest 骨架 | 不存在本方案的关联/同步业务逻辑 |
| `stakater/Reloader` | Annotation 表达依赖、source event 映射至关联 target | ConfigMap/Secret 变更后的滚动重启行为 |
| `mondoohq/mondoo-operator` | CronJob 局部字段收敛、幂等更新、Event/Condition | 其扫描业务模型与源码；当前为 BUSL-1.1，不复用源码 |
| `mittwald/kubernetes-replicator` | 跨 Namespace 选择器、分发与循环规避 | 其 ConfigMap/Secret 复制模型，不作为 CronJob 同步器 |
| `openkruise/kruise` | 生产级 Controller 的状态、事件与兼容性组织 | AdvancedCronJob 或其他 Workload 体系 |
| `kyverno/kyverno` | Namespace label 驱动的首次资源生成 | Deployment image 跟随、受管 Job 准入 |

`controller-runtime`、`kubebuilder`、Reloader 与 kubernetes-replicator 当前为 Apache-2.0；任何源码复用仍须在锁定版本时复核 License。OpenKruise 的仓库 License 元数据未被 GitHub API 识别，直接复用前必须人工核验。没有发现可以直接安装、同时完成 Deployment → CronJob image 同步、跨 Namespace 分发和旧 image Job 拦截的现成 Controller。

## 3. 资源关联与字段所有权

第一期仅支持同一 Namespace 的 Deployment 与 CronJob 关联。一个 Deployment 可关联多个 CronJob；一个 CronJob 只能关联一个 Deployment。

### 3.1 CronJob Annotation

以下 Annotation 加在 CronJob 上：

~~~yaml
metadata:
  annotations:
    jobpilot.io/follow-workload: "true"
    jobpilot.io/workload-kind: "Deployment"
    jobpilot.io/workload-name: "wes-v2"
    jobpilot.io/source-container-name: "wes-v2"
    jobpilot.io/target-container-name: "job-runner"
~~~

| Annotation | 规则 |
| --- | --- |
| follow-workload | 必须精确为 true，否则 Controller 忽略该 CronJob |
| workload-kind | MVP 必须为 Deployment |
| workload-name | 同 Namespace 中作为镜像来源的 Deployment 名称 |
| source-container-name | 从 Deployment PodTemplate 读取 image 的容器名称 |
| target-container-name | 在 CronJob JobTemplate 中写入 image 的容器名称 |

源容器和目标容器必须使用两个字段。业务 Deployment 常见容器名是 wes-v2，而 Job Pod 中的任务容器通常命名为 job-runner；两者不能靠“同名”假设关联。

### 3.2 字段所有权

| 字段 | 所有者 |
| --- | --- |
| CronJob JobTemplate 的 target 容器 image | JobPilot Workload Sync Controller |
| CronJob 的 bootstrap `suspend` 解除与同步状态 Annotation | JobPilot Workload Sync Controller |
| schedule、timeZone、concurrencyPolicy、startingDeadlineSeconds | 任务定义 |
| command、args、resources、backoffLimit、activeDeadlineSeconds | 任务定义 |
| env、envFrom、volumes、volumeMounts、serviceAccountName、imagePullSecrets | 任务定义 |
| probe、ports、replicas、Deployment strategy | Deployment；Controller 永不复制 |

MVP 不从 Deployment 继承环境变量、Secret、挂载、资源或 ServiceAccount。CronJob 必须独立声明运行 Job 所需的环境，避免把 Web 服务的运行配置误复制为批任务配置。Kyverno 使用 `synchronize: false`，只首次生成目标 CronJob；后续不覆盖 JobPilot 管理的 image、bootstrap suspend 与同步状态字段。

## 4. CronJob 与 Job Runner 规范

### 4.1 CronJob 模板

任务定义负责调度和运行约束；Controller 只会替换 target 容器的 image 值。

~~~yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: wes-v2-station-sync
  namespace: wes
  annotations:
    jobpilot.io/follow-workload: "true"
    jobpilot.io/workload-kind: "Deployment"
    jobpilot.io/workload-name: "wes-v2"
    jobpilot.io/source-container-name: "wes-v2"
    jobpilot.io/target-container-name: "job-runner"
spec:
  schedule: "*/5 * * * *"
  timeZone: Asia/Shanghai
  concurrencyPolicy: Forbid
  startingDeadlineSeconds: 120
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 5
  jobTemplate:
    spec:
      # 迁移期默认不由 Kubernetes 自动重试，避免一次业务执行产生多个副作用尝试
      backoffLimit: 0
      activeDeadlineSeconds: 1800
      template:
        spec:
          restartPolicy: Never
          serviceAccountName: wes-v2-job
          containers:
            - name: job-runner
              # 首次安装的 bootstrapImage；创建后由 JobPilot 收敛为 Deployment 当前 image
              image: eco-harbor.yfsj.cc/wms/wms:v3.6.1
              args: ["--mode=job", "--job=station-sync"]
              envFrom:
                - configMapRef:
                    name: wes-v2-config
                - secretRef:
                    name: wes-v2-secret
~~~

Forbid 只避免同一个 CronJob 的重叠 Job。手工 Job、旧 XXL 调度和其他入口仍可能与它重叠，因此业务锁和幂等性不能删除。

### 4.1.1 XXL Cron 到 Kubernetes CronJob 的转换

XXL-Job 通常使用 Quartz Cron 和 Admin 的 misfire 处理；Kubernetes CronJob 使用自己的 Cron 解析和错过调度窗口语义。迁移工具或人工配置不得直接复制字符串，必须先完成转换记录：

| XXL 字段 | Kubernetes 字段 | 约束 |
| --- | --- | --- |
| cron | `spec.schedule` | 只允许已验证的表达式；Quartz 秒字段、`L`、`W`、`#` 等扩展不得静默丢失 |
| 时区 | `spec.timeZone` | 显式填写，不依赖节点时区 |
| misfire | `startingDeadlineSeconds` | 只能做近似映射，必须记录错过时是否补偿 |
| 阻塞策略 | `concurrencyPolicy` + 业务锁 | `Forbid` 只覆盖同一 CronJob，不覆盖手工 Job 或旧 XXL 执行 |
| 失败重试 | `backoffLimit` | 默认 0；非 0 必须有业务幂等证明 |
| Handler 参数 | `args` / ConfigMap / Secret | 只使用版本化、受限参数，不支持任意在线传参 |

以下 XXL 能力不进入 v0.1：Executor 路由策略、分片广播、GLUE、`SERIAL_EXECUTION` 持久队列和 `COVER_EARLY` 的零重叠保证。任务记录必须保留“不支持原因”和替代处理，不能生成看似成功但语义不同的 CronJob。

### 4.2 Java 任务入口

同一个业务镜像有两个启动模式：

~~~text
Deployment：默认 mode，启动 Web/API/Consumer 等常驻能力
CronJob：--mode=job --job=<stable-job-name>，只运行一次并退出
~~~

Job mode 必须：

1. 禁止启动 Web Server、XXL Executor、XXL 心跳、常驻 MQ Consumer 和无关 @Scheduled 线程；
2. 加载任务实际依赖的数据库、缓存、下游客户端、ConfigMap 和 Secret；
3. 通过稳定任务名查找受限任务注册表，拒绝未知任务名；
4. 执行与迁移期 @XxlJob 适配器共用的业务 use case；
5. 等待该任务创建的 Future、线程池和事务全部结束；
6. 关闭 Spring ApplicationContext 并退出进程。

建议将任务入口抽象为：

~~~java
public interface BusinessJob {
    String name();
    void execute() throws Exception;
}
~~~

@XxlJob 方法只调用 BusinessJob.execute()；Job Runner 也调用同一方法。Kubernetes 入口不能依赖 XxlJobHelper，也不能通过请求参数反射执行任意 Spring Bean。

### 4.2.1 参数、分片与 GLUE 的边界

现有 `TriggerParam` 会将 `executorParams`、分片索引/总数、超时和阻塞策略交给 `JobThread`。方案 A 没有 `TriggerParam`，因此必须明确替代边界：

| XXL 运行时输入 | 方案 A 首期处理 |
| --- | --- |
| `executorParams` | 将参数定义为稳定 schema；由 Helm values、ConfigMap 或 Secret 引用提供，不支持 XXL UI 临时输入 |
| `broadcastIndex` / `broadcastTotal` | 不支持；不得把单个 CronJob 伪装为分片广播 |
| `executorTimeout` | 见 4.3.1；同时设置 Runner timeout 与 Pod deadline |
| `executorBlockStrategy` | 转换为任务定义中的 `concurrencyPolicy`，非等价策略单独审批 |
| GLUE Java/Groovy/Shell | 不支持；只迁移已经编译进业务镜像、并列入注册表的 Java 任务 |

手工补跑只允许复用相同 CronJob 模板和已声明参数。若确需按业务批次、日期范围等临时参数补跑，必须创建带参数审计记录的显式 Job Manifest 或后续独立的人工执行能力；v0.1 不把任意用户输入透传给 Spring 或 shell。

### 4.3 退出码

| 结果 | 行为 | 容器退出码 |
| --- | --- | ---: |
| 完整成功或安全 no-op | 记录结构化结果后退出 | 0 |
| 业务异常、依赖异常、任务超时、异步阶段失败 | 记录错误并抛出 | 非 0 |
| 缺少必需配置、未知任务名 | 启动校验失败 | 非 0 |
| 锁冲突或已在运行 | 按任务语义明确记录；通常非 0 | 非 0 |

Kubernetes 的 `backoffLimit` 按 Job 模板生效，不会根据退出码区分“可重试”和“不可重试”。迁移期默认使用 `backoffLimit: 0`：一次 Cron 调度只产生一次业务尝试；确需重试时，必须在任务记录中说明幂等依据、最大尝试次数和失败告警行为。

### 4.3.1 超时、重试与阻塞策略

`activeDeadlineSeconds` 从 Pod 开始运行后计时，不能单独等同于 XXL `executorTimeout`：它不覆盖 Cron 调度等待、Pod Pending，且终止前仍有 Kubernetes grace period。任务超时使用双层控制：

~~~text
任务级 timeout
  → Job Runner 内部取消 / 终止业务调用
  → activeDeadlineSeconds = timeout + terminationGracePeriodSeconds + 安全余量
~~~

转换规则：

| XXL 语义 | 首期规则 |
| --- | --- |
| `DISCARD_LATER` | 使用 `concurrencyPolicy: Forbid`，并保留业务锁；错过的调度是否补一次由 `startingDeadlineSeconds` 明确控制 |
| `COVER_EARLY` | 默认不迁移；若采用 `Replace`，必须接受旧 Pod 异步终止期间可能重叠 |
| `SERIAL_EXECUTION` | 不迁移；Kubernetes CronJob 不提供 `JobThread` 式持久触发队列 |
| XXL 失败重试 | 不复制 Admin 的新执行记录语义；默认 `backoffLimit: 0`，需要重试的任务自行声明安全策略 |
| Kubernetes Pod 重试 | 仅用于幂等、可重入任务；同一个 Job 可能产生多次 Pod 尝试，日志与副作用必须可区分 |

### 4.4 XXL Handler 到 Job Runner 的迁移契约

旧 XXL 任务的 `jobId`、Handler、参数和调度配置只用于迁移审计，不直接作为 Job Runner 的动态执行入口。每个任务必须生成一个静态、受限的任务注册项：

~~~text
stableJobName
  → BusinessJob 实现
  → 允许的参数 Schema
  → ConfigMap / Secret 引用
  → 超时、资源、并发和重试策略
~~~

`args: ["--mode=job", "--job=<stableJobName>"]` 只能命中注册表中的任务。不能把 CronJob 参数拼接成 Spring Bean 名称、Java 方法名或 shell 命令。

任务结果契约固定为：

| 结果 | Runner 行为 | Kubernetes 结果 |
| --- | --- | --- |
| 成功或安全 no-op | 结构化记录 checkpoint 后正常退出 | exit code 0 / Complete |
| 配置、依赖或业务异常 | 输出错误上下文并退出非 0 | Failed |
| 超时 | 先尝试取消任务，最终由 Runner 或 deadline 终止 | Failed，原因记录为 Timeout |
| 并发锁冲突 | 不执行副作用，记录冲突原因并按任务策略退出 | 通常 Failed |

不能通过“捕获异常后只打印日志并返回”报告成功；这是现有 `void @XxlJob` 方法迁移时的强制代码审查项。

## 5. Catalog 分发与同一业务镜像

这里拆分的是 Kubernetes 发布模型，不是代码和镜像。在线服务通过默认启动模式运行；任务通过 `--mode=job --job=<stable-name>` 运行同一镜像中的任务入口：

~~~text
wms-app.tgz
  └── Deployment/wms
       └── image: eco-harbor.yfsj.cc/wms/wms:v3.6.1

jobpilot-jobs-catalog.tgz
  └── CronJob Catalog + Kyverno Generate Policy
       └── 首次生成 wms、wes 等 Namespace 的 CronJob

jobpilot-controller.tgz
  └── Workload Sync Controller + Image Admission Guard
~~~

`wms-app` Chart 是 Deployment image 的唯一事实来源。`jobpilot-jobs-catalog` 的事实来源是任务集合、schedule、args、资源、重试和 workload 引用；它不能把 bootstrap image 当作长期版本来源。目标 Namespace 只安装生成后的 CronJob，不安装独立 `wms-jobs` Helm Release。

Catalog 的 values 形态：

~~~yaml
jobs:
  inventorySync:
    enabled: true
    schedule: "*/5 * * * *"
    workloadRef:
      kind: Deployment
      name: wms
      sourceContainer: wms
      targetContainer: inventory-sync
    args: ["--mode=job", "--job=inventory-sync"]
    concurrencyPolicy: Forbid
    backoffLimit: 0
    activeDeadlineSeconds: 1800
~~~

生成的 CronJob 使用 `jobpilot.io/*` Annotation 声明镜像跟随关系。初次生成必须提供一个合法的 `bootstrapImage`，以满足 Kubernetes PodSpec 的必填要求；同时必须设置 `spec.suspend: true`。Controller 收敛到 Deployment 当前镜像后才解除这一次 bootstrap suspend。

Catalog、Kyverno 与 Controller 的字段所有权必须明确：

| 字段 | Catalog / Kyverno | JobPilot Controller |
| --- | --- | --- |
| CronJob 名称、schedule、args、资源、重试 | 首次生成时负责 | 不修改 |
| workloadRef Annotation | 首次生成时负责 | 读取 |
| JobTemplate 目标容器 image | 仅提供 bootstrap 值 | 唯一运行期事实来源 |
| bootstrap suspend、同步状态 Annotation | 仅提供初值 | 唯一运行期事实来源 |

Kyverno 在 P0 使用 `synchronize: false`，因此不会在 Catalog Helm upgrade 时持续修改已生成的目标 CronJob，也不会把旧 bootstrap image 写回。Catalog 变更采用版本化迁移或删除目标 CronJob 后重新生成；重新生成的 CronJob 必须保持 suspend，直到 Controller 再次收敛。

Controller 仅修改 image、bootstrap suspend 与自己的同步状态 Annotation，使用独立 field manager；不得全量 apply 覆盖 CronJob Spec。运行期人工暂停不被 Controller 自动恢复。

### 5.1 Helm 与 Controller 的并发更新边界

Kyverno 的首次生成与 Controller 的 image 收敛不是原子操作；Deployment 更新与 CronJob 调度之间同样存在窗口。以下时序仍然可能发生：

~~~text
T1  Deployment image 更新为 v3.6.2
T2  CronJob 模板仍为 v3.6.1，Controller 尚未完成同步
T3  Kubernetes CronJob Controller 请求创建 image=v3.6.1 的 Job
T4  Image Admission Guard 读取 Deployment=v3.6.2，拒绝该 Job
T5  Workload Sync Controller 将 CronJob 模板收敛为 v3.6.2
T6  下一次 Job 创建通过 Guard
~~~

仅靠异步 Controller 只能提供**最终收敛**，不能单独证明“每一次 Cron 调度都不会在窗口内使用旧镜像”。本方案的 P0 选择强一致准入：Image Admission Guard 在 Job Create 时比较 Job PodTemplate image 与来源 Deployment 当前 image；不一致时拒绝 Job。因此 Deployment 更新、Controller 收敛之间即使发生 CronJob 调度，也不会启动旧 image 的 Job。

Guard 使用 `failurePolicy: Fail`。这是明确的可用性取舍：Guard 不可用时受管 Job 也被拒绝，以避免绕过镜像一致性保证。已有 Job/Pod 不受影响；`jobpilot-controller` 必须以两个副本、PDB、readiness、Webhook 延迟与拒绝数告警运行。

## 6. JobPilot Workload Sync Controller 设计

### 6.1 Watch 范围

Controller watch：

- Deployment：Deployment PodTemplate image 改变时同步关联 CronJob；
- CronJob：CronJob 创建、Annotation 变更或目标 image 被手工修改时校正 image；
- 不 watch 或创建 Job、Pod、Secret、ConfigMap。

Deployment 事件发生时，Controller 在**同一 Namespace**列出 follow-workload=true 的 CronJob，再按 workload-kind=Deployment 和 workload-name 精确过滤。MVP 不支持跨 Namespace 查询。

### 6.2 Reconcile 算法

每次以 CronJob 为收敛对象，执行如下顺序：

~~~text
1. 读取 CronJob。
2. 若不存在或 follow-workload != true，结束。
3. 校验 workload-kind、workload-name、source-container-name、target-container-name。
4. 读取同 Namespace 的 Deployment/workload-name。
5. 找到 Deployment 中 source-container-name 的 image。
6. 找到 CronJob JobTemplate 中 target-container-name 的 image。
7. image 相同：记录 no-op，结束。
8. image 不同：仅更新目标容器 image，写入 Event、同步状态 Annotation 和结构化日志。
9. 更新冲突：重新读取后重试；不得覆盖 CronJob 的其他字段。
~~~

任一校验失败时，Controller 不修改 CronJob。应在 CronJob 上发出 Warning Event，并以 reason 区分：InvalidAssociation、WorkloadNotFound、SourceContainerNotFound、TargetContainerNotFound、UpdateConflict。

Deployment 被删除时，Controller 不清空 CronJob 镜像、不删除 CronJob，也不创建 Job；只记录 WorkloadNotFound。恢复 Deployment 后下一次 reconcile 自动收敛。

首次生成的 CronJob 必须带 `spec.suspend: true`。Controller 成功收敛 image 后，将其解除并写入 `jobpilot.io/image-sync-state=Synced`、source image、source generation 与同步时间。该“解除 bootstrap suspend”只执行一次；后续人工暂停不被 Controller 自动恢复。

### 6.3 幂等与更新方式

Controller 对相同 Deployment image 的重复事件必须没有资源写入。更新时使用 Kubernetes API 的局部 Patch，或只修改内存对象中目标 containers[].image 后 Update，并声明独立 field manager，例如 cronjob-runtime-sync-controller。不得采用全量 apply 覆盖 CronJob Spec。

Controller 自己更新 CronJob 会再次触发 watch；image 已相同时必须立即 no-op，防止 reconcile 循环。

### 6.4 Image Admission Guard

Webhook 仅注册 `batch/v1 Job` 的 `CREATE` 请求。校验顺序固定如下：

```text
1. Job 没有 controller OwnerReference(CronJob) → 放行。
2. 读取同 Namespace 的 owner CronJob；不存在或未标记 follow-workload=true → 放行。
3. 解析 CronJob 的关联 Annotation，读取同 Namespace 来源 Deployment。
4. 从 Deployment source container 取得 expected image。
5. 从 AdmissionRequest Job PodTemplate 的 target container 取得 actual image。
6. image 相同 → 放行；不相同、关联非法或来源不可读取 → 拒绝。
```

Webhook 使用直接 Kubernetes API Reader，不能只依赖可能滞后的 controller cache。拒绝响应必须给出固定、可操作的 reason，例如 `ImageOutOfSync`、`WorkloadNotFound` 或 `TargetContainerNotFound`，但不得暴露 Secret 或任意 Kubernetes 错误内容。Webhook 永不修改 Job，也不重试创建。

`failurePolicy: Fail` 是 P0 的固定选择。Webhook 超时、服务不可达或 TLS 配置错误时，API Server 拒绝新的受管 Job；因此安装顺序必须先就绪 `jobpilot-controller`，再解除任何 Catalog CronJob 的 bootstrap suspend。

### 6.5 版本行为

~~~text
09:50  Job A 从 CronJob 模板创建，image = v1.8.6，正在运行
10:00  KubeSphere 更新 Deployment image: v1.8.6 → v1.8.7
10:00  Controller 更新 CronJob JobTemplate image: v1.8.6 → v1.8.7
10:10  Job B 从新模板创建，image = v1.8.7
~~~

Job A 保持 v1.8.6，Job B 使用 v1.8.7。Controller 不读取或修改已经创建的 Job/Pod，版本可从 Job 的 PodSpec 和日志追溯。

MVP 的同步时机是 Deployment **期望状态**变更后立即同步，而不是等待滚动发布完成。若新 Deployment 最终未就绪，需要由现有发布回滚机制把 Deployment image 回滚；Controller 会随之把 CronJob 模板回滚。Controller 日志和 Event 必须记录源 Deployment 的 generation、旧 image 和新 image。

Controller 必须在成功同步时记录至少以下关联信息：

~~~text
source deployment UID
source generation
source container
source image
target CronJob UID
target container
sync timestamp
~~~

这些信息用于判断某次 Job 使用的是哪个模板版本，但不改变已经创建的 Job。

## 7. 安全、可用性与可观测性

### 7.1 RBAC

Controller ServiceAccount 的最小权限：

| API Group | Resource | Verbs | 用途 |
| --- | --- | --- | --- |
| apps | deployments | get, list, watch | Controller 读取关联工作负载镜像；Guard 读取当前期望镜像 |
| batch | cronjobs | get, list, watch, patch, update | Controller 查找和同步；Guard 读取 Job owner CronJob |
| core | events | create, patch | 写入同步成功或失败事件 |
| coordination | leases | get, list, watch, create, update, patch | leader election |

Controller 不授予 jobs、pods、secrets 的读取/写入权限，也没有 create CronJob 的权限。Guard 不写 Kubernetes 资源；`ValidatingWebhookConfiguration` 由 Helm 安装身份管理，不授予运行时 ServiceAccount 更新权限。

### 7.2 高可用

`jobpilot-controller` 作为 Deployment 运行两个副本。Workload Sync Controller 启用 controller-runtime Leader Election，同一时刻只有 leader reconcile；Image Admission Guard 在两个副本上同时提供服务。leader 故障后其他副本接管。Controller 无数据库、无队列、无业务状态；设置 PDB，避免维护操作同时驱逐所有 Webhook endpoint。

### 7.3 事件、日志和指标

每次真实 image 更新记录 Normal Event ImageSynchronized，含 source Deployment、source container、target CronJob、target container、旧/新 image 和 generation。关联错误记录 Warning Event。

至少提供：

- cronjob_runtime_sync_reconcile_total{result}；
- cronjob_runtime_sync_updates_total；
- cronjob_runtime_sync_errors_total{reason}；
- cronjob_runtime_sync_reconcile_duration_seconds。
- jobpilot_image_admission_requests_total{result,reason}；
- jobpilot_image_admission_duration_seconds；
- jobpilot_image_admission_errors_total{reason}；P0 不定义或暴露任何 fail-open 路径。

业务任务日志必须写 stdout/stderr，包含 job-name、Job UID、Pod 名、任务名、镜像版本和业务批次/checkpoint。监控、日志和告警继续使用现有 Kubernetes 体系。

## 8. 不支持的行为与后续演进

MVP 明确不支持：

- 自动继承 env、Secret、volume、ServiceAccount 或资源限制；
- 跨 Namespace 或跨集群同步；
- 从 Deployment 自动生成 CronJob；
- 同步 command、args、schedule 或任务参数；
- 任务 UI、人工编排、在线任意参数执行；
- 通过 HTTP 触发业务服务来代替 Job Runner。

如后续确需同步运行环境，必须逐字段引入 opt-in Annotation，并定义冲突和安全边界；不能以“复制整个 PodTemplate”实现。任何新增字段同步都需要独立设计评审。

## 9. wes-v2 应用规则

stationSessionCleanupJob 与 stationSyncJob 可作为方案 A 候选：先抽取业务 use case，再以 job mode 和固定任务名直接运行。它们当前的异常捕获与仅日志记录逻辑必须调整为可返回失败的 Job Runner 结果。

stationSseHeartbeat 不迁移到 CronJob。它维护的是当前 Pod 的内存 emitter；CronJob 的单次 Pod 无法对所有 wes-v2 副本的 SSE 连接发送心跳。该任务保留在 Deployment 进程内调度。

## 10. 设计验收

- Catalog 经 Kyverno 生成的 CronJob 初始为 `suspend: true`，且不会被 Kyverno 后续覆盖 Controller 管理字段；
- 一个 Deployment 可稳定同步多个同 Namespace CronJob；
- Annotation 不完整、Deployment 不存在或容器名不匹配时，CronJob 不被修改且存在 Warning Event；
- Deployment 镜像变更会使 CronJob 模板收敛到同一 image；
- Controller 只会改动 target image、一次 bootstrap suspend 与自己的同步状态 Annotation，不会创建/修改 Job 或 Pod；
- Guard 放行未受管 Job 与 image 已同步的受管 Job；拒绝旧 image、关联非法或来源不可读取的受管 Job；
- Webhook 不可用时，新的受管 Job 被 API Server 拒绝；两个 Controller 副本中任一可用时仍可提供 Guard；
- 运行中 Job 在镜像同步后保持原 image；
- Job Runner 在成功时退出 0，在异常或配置错误时非 0；
- Controller 在重复事件、多副本和资源版本冲突下保持幂等；
- 最小 RBAC 不包含业务 Secret、Job 或 Pod 权限。

## 11. 测试范围

- Controller 单元测试：Deployment image 变更、多个 CronJob 映射、Annotation/容器缺失、局部 Patch、同步状态、bootstrap suspend、重复事件与资源版本冲突；
- Guard 单元测试：非受管 Job 放行、同步 image 放行、旧 image 拒绝、来源 Deployment/CronJob/容器缺失拒绝；
- `envtest` 或临时集群测试：两个 Controller 副本下 leader 接管、Webhook TLS、`failurePolicy: Fail`、Kyverno 首次分发后同步和解除 bootstrap suspend；
- 端到端测试：Deployment image 更新后，旧 image Job 被 Guard 拒绝；Controller 收敛后，下一次 CronJob 创建的新 image Job 成功通过。
