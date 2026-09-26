# JobPilot API Contract v1

## 1. Conventions

- Base path: `/api/v1`; authenticated API accepts and returns `application/json; charset=utf-8` unless otherwise noted.
- Browser session uses same-origin secure cookies. All state-changing requests require `X-CSRF-Token` obtained from `GET /api/v1/me`.
- Each response contains `X-Request-ID`; JSON responses also carry `requestId`.
- Timestamps are RFC3339 UTC strings. Durations are integer seconds or `null`.
- List parameters: `page` default `1`, minimum `1`; `pageSize` default `25`, range `1..100`. Invalid values return `400 VALIDATION_FAILED`.
- `sort` names only documented fields; default and allowed values are endpoint-specific. `order` is `asc` or `desc`, default `desc` for time and `asc` for names.
- Collection results are query-time snapshots. The cursor is numeric page/size, not a Kubernetes `continue` token; resources may change between requests.

## 2. Response envelopes

Success:

```json
{
  "data": {},
  "requestId": "01J..."
}
```

Paged success:

```json
{
  "data": [],
  "page": { "number": 1, "size": 25, "total": 68, "totalPages": 3 },
  "requestId": "01J..."
}
```

Error:

```json
{
  "error": {
    "code": "RESOURCE_VERSION_CONFLICT",
    "message": "CronJob changed; refresh and try again",
    "details": [{ "field": "resourceVersion", "reason": "stale" }]
  },
  "requestId": "01J..."
}
```

`details` is optional and only used for client-safe validation fields. Kubernetes API status bodies, authentication material and internal error strings never appear in the envelope.

## 3. Public DTOs

```ts
type Role = "viewer" | "operator" | "admin";
type CronJobState = "Running" | "Success" | "Failed" | "Suspended" | "NeverRun" | "Unknown";
type JobState = "Pending" | "Running" | "Success" | "Failed" | "Unknown";
type TriggerType = "Schedule" | "Manual" | "Retry" | "Unknown";
type ImagePolicy = "Fixed" | "FollowWorkload" | "Manual";

interface Actor { subject: string; username: string; groups: string[]; }
interface NamespaceGrant { namespace: string; role: Role; }
interface Me { actor: Actor; grants: NamespaceGrant[]; csrfToken: string; }

interface ExecutionSummary {
  name: string;
  namespace: string;
  state: JobState;
  sourceCronJob?: { name: string; uid: string };
  triggerType: TriggerType;
  triggeredBy?: string;
  startedAt?: string;
  completedAt?: string;
  durationSeconds?: number;
  completions: number;
  failedPods: number;
  retryOf?: string;
}

interface CronJobSummary {
  name: string;
  namespace: string;
  uid: string;
  schedule: string;
  timeZone?: string;
  scheduleTimeZone: string;
  suspended: boolean;
  concurrencyPolicy: "Allow" | "Forbid" | "Replace";
  startingDeadlineSeconds?: number;
  state: CronJobState;
  activeJobs: number;
  lastExecution?: ExecutionSummary;
  nextRun?: string;
  imagePolicy: ImagePolicy;
  workloadRef?: {
    kind: "Deployment";
    name: string;
    namespace: string;
    sourceContainer: string;
    targetContainer: string;
    sourceImage?: string;
    resolvedImage?: string;
    syncState: "Synced" | "Pending" | "Error" | "NotManaged";
  };
  resourceVersion: string;
}

interface PodSummary {
  name: string;
  uid: string;
  phase: "Pending" | "Running" | "Succeeded" | "Failed" | "Unknown";
  startedAt?: string;
  completedAt?: string;
  containers: Array<{ name: string; type: "container" | "initContainer"; state: string; exitCode?: number }>;
}
```

Kubernetes map values that have no public equivalent are deliberately omitted. `scheduleTimeZone` is `spec.timeZone` when provided, otherwise the configured controller-default timezone used for preview calculation.

## 4. Health and identity

### `POST /auth/break-glass/login`

仅在 `auth.breakGlass.enabled=true` 时存在；未启用时返回 `404 RESOURCE_NOT_FOUND`。它不属于日常登录流程，也不由 OIDC 失败自动调用。请求体包含长度受限的 `username`、`password`、`otp` 和 `reason`。成功时创建短时加密的 HttpOnly Session Cookie，并 `303` 重定向到同源首页；失败统一返回 `401 BREAK_GLASS_AUTH_FAILED`，不泄露是账号、密码还是 OTP 错误。

该端点独立按源 IP 和 username 限流。成功、失败、登出都产生审计事件；成功还触发安全告警及 `jobpilot_break_glass_logins_total`。身份随后只按 `breakGlassBindings` 执行 RBAC/Namespace 授权。

### `GET /healthz`

Unauthenticated. Returns `200 {"status":"ok"}` when the process can serve HTTP. It does not call Kubernetes or GitLab.

### `GET /readyz`

Unauthenticated. Calls a bounded Kubernetes discovery/`ServerVersion` check. Returns `200` only when configuration and Kubernetes client are ready; otherwise `503 KUBERNETES_UNAVAILABLE`. Do not make OIDC provider reachability a readiness condition because a temporary GitLab outage must not restart healthy Pods.

### `GET /api/v1/me`

Authenticated. Returns `Me`. `groups` is filtered to groups used by policy or a separate safe display allow-list; do not expose every raw OIDC claim by default. `csrfToken` is a short-lived synchronizer token and is required for later writes.

### `GET /api/v1/namespaces`

Viewer. Returns sorted visible Namespace grants:

```json
{ "data": [{ "name": "wms-test", "role": "operator" }] }
```

The response has no `*` entry. For wildcard grants, server lists discoverable namespaces and emits concrete names. A Namespace removed from the cluster is not returned.

## 5. CronJob endpoints

### `GET /api/v1/cronjobs`

Viewer. Query:

| Parameter | Meaning |
| --- | --- |
| `namespace` | optional single visible Namespace |
| `q` | case-insensitive substring of name; maximum 128 chars |
| `state` | comma-separated CronJobState values |
| `suspended` | `true` or `false` |
| `sort` | `name`, `namespace`, `nextRun`, `lastRun`, `state`; default `name` |
| `order`, `page`, `pageSize` | conventions above |

The service lists only authorized namespaces, derives Job state from current retained Jobs, filters/sorts, then pages. It returns `CronJobSummary[]`. An inaccessible `namespace` filter yields `404 RESOURCE_NOT_FOUND`; an absent allowed namespace yields an empty list.

### `GET /api/v1/namespaces/{namespace}/cronjobs/{name}`

Viewer. Returns `CronJobSummary` plus:

```json
{
  "successfulJobsHistoryLimit": 3,
  "failedJobsHistoryLimit": 1,
  "ttlSecondsAfterFinished": 3600,
  "jobTemplate": {
    "containers": [{ "name": "worker", "image": "registry.example/worker:v2" }],
    "restartPolicy": "Never",
    "serviceAccountName": "worker"
  }
}
```

The template is a read-only summary, not raw YAML. Environment values, command arguments and Secret references are omitted; secret value is never fetched. Missing/inaccessible resource returns identical `404 RESOURCE_NOT_FOUND`.

For a CronJob with `jobpilot.io/follow-workload="true"`, the response also includes `imagePolicy: "FollowWorkload"` and `workloadRef`. `sourceImage` is read from the referenced Deployment container; `resolvedImage` is the CronJob target container image. `syncState` is `Synced` when they are equal, `Pending` when the association is valid but the image has not yet converged, and `Error` when the association, Deployment or source/target container is unavailable or invalid. The API never accepts an image update request: Helm owns the task definition and JobPilot Controller owns only the resolved target image.

### `GET /api/v1/namespaces/{namespace}/cronjobs/{name}/executions`

Viewer. Query: `state`, `triggerType`, `from`, `to` (RFC3339 UTC), `sort` (`startedAt`, `duration`, `state`; default `startedAt`), pagination. `from/to` filter only resources currently retained; response adds:

```json
{ "historyScope": "kubernetes-retained-jobs" }
```

The endpoint only includes Jobs associated by the UID rules in the detailed design. `triggeredBy` is an annotation value and may be null for old Jobs.

### `POST /api/v1/namespaces/{namespace}/cronjobs/{name}/trigger`

Operator. Body is empty object `{}`; unknown fields are rejected. The operation is non-idempotent and client libraries must not automatically retry.

Success `201` returns the created `ExecutionSummary` and `Location: /api/v1/namespaces/{namespace}/jobs/{createdName}`. `202` is not used: successful Kubernetes Create has already accepted the Job, while execution state may initially be `Pending`.

The endpoint accepts suspended CronJobs; response includes `warning: "cronjob_is_suspended"` because the new manual Job remains valid. It never allows argument/image/env override.

### `POST /api/v1/namespaces/{namespace}/cronjobs/{name}/suspend`

Admin. Body:

```json
{ "resourceVersion": "12345" }
```

Success returns `{ "changed": true, "cronjob": CronJobSummary }`; already-suspended returns `{ "changed": false, ... }`. `resourceVersion` is required; stale version returns `409 RESOURCE_VERSION_CONFLICT`.

### `POST /api/v1/namespaces/{namespace}/cronjobs/{name}/resume`

Admin. Same body and response as suspend. It only sets `spec.suspend=false`; it does not create missed Jobs or immediately schedule a Job.

## 6. Job endpoints

### `GET /api/v1/jobs`

Viewer. Query: `namespace`, `q`, `state`, `sourceCronJob`, `triggerType`, `sort` (`name`, `namespace`, `startedAt`, `duration`, `state`; default `startedAt`), pagination. This list includes visible standalone Jobs with `sourceCronJob: null`; the UI labels them `Unknown` source and does not offer Retry unless safe retry criteria are met.

### `GET /api/v1/namespaces/{namespace}/jobs/{name}`

Viewer. Returns `ExecutionSummary`, terminal condition summary, Pod count and `retryEligible` boolean/reason. It does not return `spec.template` raw fields or arbitrary Job metadata.

### `POST /api/v1/namespaces/{namespace}/jobs/{name}/retry`

Operator. Body `{}`. Preconditions: Job exists, is terminal `Failed`, and service can construct an allowed retry template. Success `201` returns:

```json
{
  "execution": { "name": "order-timeout-retry-f8q2z", "triggerType": "Retry" },
  "retryTemplateSource": "cronjob"
}
```

Errors are `409 JOB_NOT_RETRYABLE` for nonfailed/terminal-in-transition jobs and `422 RETRY_TEMPLATE_UNAVAILABLE` when neither a matching source CronJob nor safe original spec can be used. The source Job is never changed.

### `GET /api/v1/namespaces/{namespace}/jobs/{name}/pods`

Viewer. Returns `PodSummary[]` only after verifying each Pod is controller-owned by the requested Job. Pod order is creation time ascending. This endpoint is used by Job details; it does not return arbitrary Namespace Pods.

## 7. Logs

### `GET /api/v1/namespaces/{namespace}/pods/{pod}/logs`

Viewer. Required query `job` binds the Pod to a visible Job. Query:

| Parameter | Default | Rule |
| --- | --- | --- |
| `container` | only container if exactly one | required for multiple containers |
| `tailLines` | 1000 | integer 1..10000 |
| `previous` | false | only valid with selected container |

Success returns `text/plain; charset=utf-8`, timestamps enabled. A completed Pod’s current log is returned when Kubernetes still retains it; missing logs return `404 LOG_NOT_FOUND`, not an empty success. `container` invalid returns `422 CONTAINER_NOT_FOUND`.

### `GET /api/v1/namespaces/{namespace}/pods/{pod}/logs/stream`

Viewer. Requires the same `job` and `container` checks; accepts `tailLines` and `previous=false` only. On success its response is `text/event-stream; charset=utf-8`, with `Cache-Control: no-cache` and no JSON envelope.

Events are `ready`, `log`, `end`, and `error`. `error` can be emitted only after headers have been sent and carries `{code,message,requestId}`. SSE connection does not support mutation, authorization refresh, log replay or `Last-Event-ID` resume. Client reconnect uses exponential delay capped at 10 seconds and visibly reports potential duplicate lines.

## 8. Error codes

| HTTP | Code | Meaning | Client action |
| --- | --- | --- | --- |
| 400 | `VALIDATION_FAILED` | query/body invalid | correct input |
| 401 | `AUTHENTICATION_REQUIRED` | missing/expired session | login |
| 403 | `CSRF_VALIDATION_FAILED` | unsafe cookie request lacks valid CSRF token | reload `/me` then retry user action |
| 403 | `AUTHORIZATION_DENIED` | named visible resource but role insufficient for mutation | hide action / request access |
| 404 | `RESOURCE_NOT_FOUND` | absent or not readable resource | refresh list; do not disclose difference |
| 404 | `LOG_NOT_FOUND` | Pod log no longer available | use platform log store if configured outside v0.1 |
| 409 | `RESOURCE_VERSION_CONFLICT` | CronJob changed after page load | refresh and reconfirm |
| 409 | `JOB_NOT_RETRYABLE` | Job is not currently failed | refresh Job state |
| 422 | `RETRY_TEMPLATE_UNAVAILABLE` | no safe template for retry | inspect original Job/CronJob outside JobPilot |
| 422 | `CONTAINER_NOT_FOUND` | selected container is invalid | choose a listed container |
| 429 | `KUBERNETES_RATE_LIMITED` | API server throttled | wait and refresh |
| 502 | `KUBERNETES_FORBIDDEN` | deployment ServiceAccount lacks required RBAC | operator fixes deployment RBAC |
| 503 | `KUBERNETES_UNAVAILABLE` | API unavailable | retry later; no operation queued |
| 504 | `KUBERNETES_TIMEOUT` | outcome may be uncertain for a write | refresh and inspect before retry |
| 500 | `INTERNAL_ERROR` | unexpected server error | use request ID for support |

No endpoint returns raw Kubernetes status or uses `500` to represent a client input error.
