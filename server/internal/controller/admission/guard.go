package admission

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	cradmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"gitlab.yfsj.cc/operations/jobpilot/server/internal/controller/workloadsync"
)

// Guard validates only Job CREATE requests that are owned by a managed
// CronJob. It never mutates a request and uses a direct API Reader supplied by
// the process rather than the manager cache.
type Guard struct {
	Reader  client.Reader
	Decoder cradmission.Decoder
	Metrics *Metrics
}

type Metrics struct {
	Requests *prometheus.CounterVec
}

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	metrics := &Metrics{Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "jobpilot",
		Subsystem: "image_admission",
		Name:      "requests_total",
		Help:      "Admission decisions for Job image validation.",
	}, []string{"result", "reason"})}
	if err := registerer.Register(metrics.Requests); err != nil {
		return nil, err
	}
	return metrics, nil
}

func (g *Guard) Handle(ctx context.Context, request cradmission.Request) cradmission.Response {
	if request.Operation != "CREATE" {
		return g.allow("operation is not Job CREATE", "NotCreate")
	}

	job := &batchv1.Job{}
	if err := g.Decoder.Decode(request, job); err != nil {
		return g.errorResponse(fmt.Errorf("decode Job: %w", err))
	}
	owner := cronJobOwner(job)
	if owner == nil {
		return g.allow("Job is not controlled by a CronJob", "Unmanaged")
	}

	cronJob := &batchv1.CronJob{}
	if err := g.Reader.Get(ctx, client.ObjectKey{Namespace: job.Namespace, Name: owner.Name}, cronJob); err != nil {
		if apierrors.IsNotFound(err) {
			return g.allow("owner CronJob no longer exists", "OwnerGone")
		}
		return g.deny("WorkloadNotFound", "owner CronJob is unavailable")
	}
	if owner.UID == "" || cronJob.UID == "" || owner.UID != cronJob.UID {
		return g.deny("OwnerReferenceMismatch", "Job owner UID does not match the CronJob")
	}
	association, managed, err := workloadsync.AssociationFor(cronJob)
	if !managed {
		return g.allow("owner CronJob is not JobPilot-managed", "Unmanaged")
	}
	if err != nil {
		return g.deny("InvalidAssociation", "CronJob FollowWorkload annotations are invalid")
	}

	deployment := &appsv1.Deployment{}
	if err := g.Reader.Get(ctx, client.ObjectKey{Namespace: job.Namespace, Name: association.WorkloadName}, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return g.deny("WorkloadNotFound", "source Deployment does not exist")
		}
		return g.deny("WorkloadNotFound", "source Deployment is unavailable")
	}
	sourceImage, found := sourceContainerImage(deployment, association.SourceContainer)
	if !found || sourceImage == "" {
		return g.deny("SourceContainerNotFound", "source container image is unavailable")
	}
	actualImage, found := jobContainerImage(job, association.TargetContainer)
	if !found {
		return g.deny("TargetContainerNotFound", "target container is unavailable")
	}
	if actualImage != sourceImage {
		return g.deny("ImageOutOfSync", "Job image does not match the source Deployment")
	}
	return g.allow("Job image matches the source Deployment", "ImageMatched")
}

func (g *Guard) allow(message, reason string) cradmission.Response {
	if g.Metrics != nil {
		g.Metrics.Requests.WithLabelValues("allowed", reason).Inc()
	}
	return cradmission.Allowed(message)
}

func (g *Guard) deny(reason, message string) cradmission.Response {
	if g.Metrics != nil {
		g.Metrics.Requests.WithLabelValues("denied", reason).Inc()
	}
	return cradmission.Denied(reason + ": " + message)
}

func (g *Guard) errorResponse(err error) cradmission.Response {
	if g.Metrics != nil {
		g.Metrics.Requests.WithLabelValues("error", "DecodeError").Inc()
	}
	return cradmission.Errored(400, err)
}

func sourceContainerImage(deployment *appsv1.Deployment, name string) (string, bool) {
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == name {
			return container.Image, true
		}
	}
	return "", false
}

func jobContainerImage(job *batchv1.Job, name string) (string, bool) {
	for _, container := range job.Spec.Template.Spec.Containers {
		if container.Name == name {
			return container.Image, true
		}
	}
	return "", false
}

func cronJobOwner(job *batchv1.Job) *metav1.OwnerReference {
	for i := range job.OwnerReferences {
		owner := &job.OwnerReferences[i]
		if owner.Controller != nil && *owner.Controller && owner.Kind == "CronJob" {
			return owner
		}
	}
	return nil
}
