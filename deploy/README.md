# P0 部署顺序

P0 的安装顺序必须先建立 Controller/Webhook，再允许 Catalog 生成目标
CronJob：

```text
1. 安装 jobpilot-controller
2. 等待 Deployment readiness 与 Webhook TLS Secret 就绪
3. 安装 jobpilot-jobs-catalog
4. 给目标 Namespace 增加 jobpilot.io/cronjob-catalog=enabled
5. 确认生成的 CronJob 已完成 image 同步并解除 suspend
```

示例：

```bash
helm upgrade --install jobpilot-controller \
  deploy/helm/jobpilot-controller \
  --namespace jobpilot --create-namespace

kubectl -n jobpilot rollout status deployment/jobpilot-controller-jobpilot-controller

helm upgrade --install jobpilot-catalog \
  deploy/helm/jobpilot-jobs-catalog \
  --namespace jobpilot

kubectl label namespace wms jobpilot.io/cronjob-catalog=enabled --overwrite
```

`jobpilot-controller` 的 Admission Guard 固定使用 `failurePolicy: Fail`。
Controller/Webhook 不可用时，新的受管 Job 会被拒绝；已有 Job/Pod 不受影响。
Kyverno Catalog 使用 `synchronize: false`，不会在后续同步中覆盖 Controller 管理
的 image、suspend 或同步状态字段。
