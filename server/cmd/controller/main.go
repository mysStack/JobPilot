package main

import (
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	admission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	guardadmission "gitlab.yfsj.cc/operations/jobpilot/server/internal/controller/admission"
	"gitlab.yfsj.cc/operations/jobpilot/server/internal/controller/workloadsync"
)

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))

	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(appsv1.AddToScheme(scheme))
	must(batchv1.AddToScheme(scheme))

	config := ctrl.GetConfigOrDie()
	manager, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:                        scheme,
		LeaderElection:                true,
		LeaderElectionID:              "jobpilot-workload-sync.jobpilot.io",
		HealthProbeBindAddress:        ":8081",
		Metrics:                       metricsserver.Options{BindAddress: ":8080"},
		WebhookServer:                 webhook.NewServer(webhook.Options{Port: 9443, CertDir: "/tmp/k8s-webhook-server/serving-certs"}),
		LeaderElectionReleaseOnCancel: true,
	})
	must(err)

	directReader, err := client.New(config, client.Options{Scheme: scheme})
	must(err)
	guardMetrics, err := guardadmission.NewMetrics(crmetrics.Registry)
	must(err)
	manager.GetWebhookServer().Register("/validate-jobpilot-io-v1-job", &admission.Webhook{Handler: &guardadmission.Guard{
		Reader:  directReader,
		Decoder: admission.NewDecoder(scheme),
		Metrics: guardMetrics,
	}})

	metrics, err := workloadsync.NewMetrics(crmetrics.Registry)
	must(err)
	controller := &workloadsync.Controller{
		Reconciler:        &workloadsync.Reconciler{Client: manager.GetClient()},
		ManagedNamespaces: parseNamespaces(os.Getenv("JOBPILOT_MANAGED_NAMESPACES")),
		Metrics:           metrics,
	}
	must(controller.SetupWithManager(manager))
	must(manager.AddHealthzCheck("healthz", healthz.Ping))
	must(manager.AddReadyzCheck("readyz", healthz.Ping))

	must(manager.Start(ctrl.SetupSignalHandler()))
}

func parseNamespaces(value string) map[string]struct{} {
	namespaces := map[string]struct{}{}
	for _, namespace := range strings.Split(value, ",") {
		namespace = strings.TrimSpace(namespace)
		if namespace != "" {
			namespaces[namespace] = struct{}{}
		}
	}
	return namespaces
}

func must(err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
