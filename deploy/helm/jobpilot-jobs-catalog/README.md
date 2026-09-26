# jobpilot-jobs-catalog

This chart owns the task catalog and renders a Kyverno `ClusterPolicy`. The
policy generates CronJobs only in Namespaces labelled with the configured
selector. `synchronize: false` is intentional: Kyverno performs the initial
distribution only and does not overwrite image, suspend, or sync-state fields
after JobPilot starts managing the generated CronJob.

Install this chart after Kyverno is ready and before enabling the target
Namespace labels. Every generated CronJob starts suspended and must be released
by the Workload Sync Controller after its image matches the source Deployment.
