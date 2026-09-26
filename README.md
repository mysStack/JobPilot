# JobPilot

Kubernetes 原生 Job / CronJob 管理中心。

项目总需求、范围边界、参考项目调研门禁和实施路线图见 [plan.md](plan.md)。

`plan.md` 不是详细设计文档。文档按“总计划 → 设计 → 实施参考”的层级组织：

**JobPilot 平台设计**

- [总体架构](docs/architecture.md)
- [详细设计](docs/design.md)
- [API Contract](docs/api.md)
- [授权与 Kubernetes RBAC](docs/rbac.md)
- [开源参考调研](docs/reference-analysis.md)

**XXL-Job 迁移**

- [迁移计划](docs/xxl-job-plan.md)
- [迁移技术设计](docs/xxl-job-design.md)

**备选方案（不作为主方案）**

- [Kubernetes Executor 兼容方案](docs/alternatives/xxl-job-executor.md)

## 当前实现

第一阶段交付不依赖 Web/API 的 P0 控制面能力：

- 只处理显式声明 `jobpilot.io/follow-workload="true"` 的 CronJob；
- 读取同 Namespace 的 Deployment 镜像，仅写回指定的 CronJob target container；
- 首次同步完成后解除 bootstrap `suspend`，不覆盖后续人工暂停；
- Image Admission Guard 直接读取 Kubernetes API，在 Job 创建时拒绝旧镜像或无效关联；
- 通过 Helm Chart `deploy/helm/jobpilot-controller` 部署 Controller、Webhook、TLS、Leader Election、健康检查、指标和最小 RBAC；
- 通过 `deploy/helm/jobpilot-jobs-catalog` 生成 Kyverno `synchronize: false` Catalog Policy；目标 CronJob 初始保持 `suspend: true`。

本地验证：

```bash
cd server
go test ./...
go vet ./...

cd ..
helm lint deploy/helm/jobpilot-controller
helm template jobpilot deploy/helm/jobpilot-controller --namespace jobpilot

helm lint deploy/helm/jobpilot-jobs-catalog
helm template catalog deploy/helm/jobpilot-jobs-catalog --namespace jobpilot
```
