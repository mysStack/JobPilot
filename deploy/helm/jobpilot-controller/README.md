# jobpilot-controller

This chart deploys the P0 Workload Sync Controller and Image Admission Guard.
The chart creates a self-signed TLS Secret with Helm on first install and keeps
it on subsequent upgrades through `lookup`; the CA bundle is embedded in the
`ValidatingWebhookConfiguration`. Set `webhook.tlsSecretName` when an existing
cluster-managed serving certificate should be used instead (the Secret must
contain `tls.crt`, `tls.key`, and `ca.crt`).

The webhook uses `failurePolicy: Fail` by default. Install and wait for the
controller readiness probe before enabling Catalog target Namespace labels.
