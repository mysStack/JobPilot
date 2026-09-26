package workloadsync

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const controllerName = "jobpilot-workload-sync"

// Controller adapts the pure CronJob synchronizer to controller-runtime.
// A controller instance can be limited to a configured set of namespaces.
type Controller struct {
	*Reconciler
	Recorder          record.EventRecorder
	ManagedNamespaces map[string]struct{}
	Metrics           *Metrics
}

func (r *Controller) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx).WithValues("cronJob", request.NamespacedName)
	if !r.managesNamespace(request.Namespace) {
		return ctrl.Result{}, nil
	}

	cronJob := &batchv1.CronJob{}
	if err := r.Get(ctx, request.NamespacedName, cronJob); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to get CronJob")
		return ctrl.Result{}, fmt.Errorf("get cronjob %s: %w", request.NamespacedName, err)
	}

	result, err := r.SyncCronJob(ctx, cronJob)
	if err != nil {
		logger.Error(err, "unable to synchronize CronJob image")
		reason := ReasonImageSynchronizationFail
		var syncError *SyncError
		if errors.As(err, &syncError) {
			reason = syncError.Reason
		}
		r.recordWarning(cronJob, reason, err.Error())
		if r.Metrics != nil {
			r.Metrics.SyncFailures.Inc()
		}
		return ctrl.Result{}, err
	}
	if result.Updated {
		logger.Info("synchronized CronJob image", "previousImage", result.PreviousImage, "image", result.SourceImage)
		r.recordNormal(cronJob, "ImageSynchronized", fmt.Sprintf("synchronized %s/%s image from %s to %s", cronJob.Namespace, cronJob.Name, result.PreviousImage, result.SourceImage))
		if r.Metrics != nil {
			r.Metrics.Syncs.Inc()
		}
	}
	return ctrl.Result{}, nil
}

func (r *Controller) SetupWithManager(manager ctrl.Manager) error {
	if r.Reconciler == nil {
		r.Reconciler = &Reconciler{Client: manager.GetClient()}
	}
	if r.Client == nil {
		r.Client = manager.GetClient()
	}
	if r.Recorder == nil {
		r.Recorder = manager.GetEventRecorderFor(controllerName)
	}
	if err := manager.GetFieldIndexer().IndexField(context.Background(), &batchv1.CronJob{}, workloadNameIndex, func(object client.Object) []string {
		association, managed, err := parseAssociation(object.(*batchv1.CronJob))
		if err != nil || !managed {
			return nil
		}
		return []string{association.workloadName}
	}); err != nil {
		return fmt.Errorf("index cronjobs by workload name: %w", err)
	}

	return ctrl.NewControllerManagedBy(manager).
		For(&batchv1.CronJob{}, builder.WithPredicates(predicate.ResourceVersionChangedPredicate{})).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(r.requestsForDeployment)).
		Complete(r)
}

func (r *Controller) requestsForDeployment(ctx context.Context, object client.Object) []ctrl.Request {
	deployment, ok := object.(*appsv1.Deployment)
	if !ok || !r.managesNamespace(object.GetNamespace()) {
		return nil
	}

	cronJobs := &batchv1.CronJobList{}
	if err := r.List(ctx, cronJobs,
		client.InNamespace(deployment.Namespace),
		client.MatchingFields{workloadNameIndex: deployment.Name},
	); err != nil {
		return nil
	}

	requests := make([]ctrl.Request, 0)
	for i := range cronJobs.Items {
		cronJob := &cronJobs.Items[i]
		assoc, managed, err := parseAssociation(cronJob)
		if err != nil || !managed || assoc.workloadName != deployment.Name {
			continue
		}
		requests = append(requests, ctrl.Request{NamespacedName: types.NamespacedName{
			Namespace: cronJob.Namespace,
			Name:      cronJob.Name,
		}})
	}
	return requests
}

func (r *Controller) managesNamespace(namespace string) bool {
	if len(r.ManagedNamespaces) == 0 {
		return true
	}
	if _, allNamespaces := r.ManagedNamespaces["*"]; allNamespaces {
		return true
	}
	_, managed := r.ManagedNamespaces[namespace]
	return managed
}

func (r *Controller) recordNormal(object client.Object, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(object, "Normal", reason, message)
	}
}

func (r *Controller) recordWarning(object client.Object, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(object, "Warning", reason, message)
	}
}
