package workloadsync

import "github.com/prometheus/client_golang/prometheus"

// Metrics are registered by main against the controller-runtime registry.
// They deliberately measure controller outcomes, never workload payload data.
type Metrics struct {
	Syncs        prometheus.Counter
	SyncFailures prometheus.Counter
}

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	metrics := &Metrics{
		Syncs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "jobpilot",
			Subsystem: "workload_sync",
			Name:      "operations_total",
			Help:      "Total successful CronJob image synchronization operations.",
		}),
		SyncFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "jobpilot",
			Subsystem: "workload_sync",
			Name:      "failures_total",
			Help:      "Total failed CronJob image synchronization operations.",
		}),
	}
	for _, metric := range []prometheus.Collector{metrics.Syncs, metrics.SyncFailures} {
		if err := registerer.Register(metric); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}
