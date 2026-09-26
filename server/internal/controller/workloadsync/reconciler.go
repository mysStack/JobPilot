package workloadsync

import (
	"context"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler owns only the fields explicitly declared by the FollowWorkload
// contract. It never copies a Deployment PodTemplate into a CronJob.
type Reconciler struct {
	client.Client
	Now func() time.Time
}

type SyncResult struct {
	Updated       bool
	PreviousImage string
	SourceImage   string
}

const (
	ReasonInvalidAssociation       = "InvalidAssociation"
	ReasonWorkloadNotFound         = "WorkloadNotFound"
	ReasonSourceContainerNotFound  = "SourceContainerNotFound"
	ReasonTargetContainerNotFound  = "TargetContainerNotFound"
	ReasonUpdateConflict           = "UpdateConflict"
	ReasonImageSynchronizationFail = "ImageSyncFailed"
)

// SyncError is safe to surface in a Kubernetes Warning Event.
type SyncError struct {
	Reason string
	Err    error
}

func (e *SyncError) Error() string { return e.Err.Error() }
func (e *SyncError) Unwrap() error { return e.Err }

func syncError(reason, format string, args ...any) error {
	return &SyncError{Reason: reason, Err: fmt.Errorf(format, args...)}
}

type association struct {
	workloadName string
	sourceName   string
	targetName   string
}

// Association is the validated public form of a CronJob's FollowWorkload
// annotations. It is shared by the synchronizer and the admission guard.
type Association struct {
	WorkloadName    string
	SourceContainer string
	TargetContainer string
}

// AssociationFor validates and returns a CronJob's FollowWorkload association.
func AssociationFor(cronJob *batchv1.CronJob) (Association, bool, error) {
	association, managed, err := parseAssociation(cronJob)
	if err != nil || !managed {
		return Association{}, managed, err
	}
	return Association{
		WorkloadName:    association.workloadName,
		SourceContainer: association.sourceName,
		TargetContainer: association.targetName,
	}, true, nil
}

// SyncCronJob converges one managed CronJob to its source Deployment. An
// unmanaged CronJob is a no-op. Missing resources and invalid associations
// return errors so the controller can emit a reason-specific Warning Event.
func (r *Reconciler) SyncCronJob(ctx context.Context, cronJob *batchv1.CronJob) (SyncResult, error) {
	assoc, managed, err := parseAssociation(cronJob)
	if err != nil {
		return SyncResult{}, syncError(ReasonInvalidAssociation, "%w", err)
	}
	if !managed {
		return SyncResult{}, nil
	}

	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Name: assoc.workloadName, Namespace: cronJob.Namespace}
	if err := r.Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return SyncResult{}, syncError(ReasonWorkloadNotFound, "source deployment %s/%s not found", key.Namespace, key.Name)
		}
		return SyncResult{}, syncError(ReasonWorkloadNotFound, "get source deployment %s/%s: %w", key.Namespace, key.Name, err)
	}

	sourceImage, found := containerImage(deployment.Spec.Template.Spec.Containers, assoc.sourceName)
	if !found || sourceImage == "" {
		return SyncResult{}, syncError(ReasonSourceContainerNotFound, "source container %q not found in deployment %s/%s", assoc.sourceName, key.Namespace, key.Name)
	}
	if _, found := containerImage(cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers, assoc.targetName); !found {
		return SyncResult{}, syncError(ReasonTargetContainerNotFound, "target container %q not found in cronjob %s/%s", assoc.targetName, cronJob.Namespace, cronJob.Name)
	}

	before := cronJob.DeepCopy()
	updated := false
	previousImage := ""
	if target := findContainer(&cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers, assoc.targetName); target != nil && target.Image != sourceImage {
		previousImage = target.Image
		target.Image = sourceImage
		updated = true
	}

	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if cronJob.Annotations == nil {
		cronJob.Annotations = map[string]string{}
	}
	firstSync := cronJob.Annotations[ImageSyncStateAnnotation] != ImageSyncStateSynced
	metadataStale := cronJob.Annotations[ImageSyncSourceImageAnnotation] != sourceImage ||
		cronJob.Annotations[ImageSyncSourceGenerationAnnotation] != strconv.FormatInt(deployment.Generation, 10)
	if firstSync || metadataStale {
		cronJob.Annotations[ImageSyncStateAnnotation] = ImageSyncStateSynced
		cronJob.Annotations[ImageSyncSourceImageAnnotation] = sourceImage
		cronJob.Annotations[ImageSyncSourceGenerationAnnotation] = strconv.FormatInt(deployment.Generation, 10)
		cronJob.Annotations[ImageSyncTimestampAnnotation] = now().UTC().Format(time.RFC3339)
		updated = true
		if firstSync && cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend {
			unsuspended := false
			cronJob.Spec.Suspend = &unsuspended
		}
	}
	if !updated {
		return SyncResult{SourceImage: sourceImage}, nil
	}

	if err := r.Patch(ctx, cronJob, client.MergeFrom(before), client.FieldOwner(controllerName)); err != nil {
		reason := ReasonImageSynchronizationFail
		if apierrors.IsConflict(err) {
			reason = ReasonUpdateConflict
		}
		return SyncResult{}, syncError(reason, "patch cronjob %s/%s: %w", cronJob.Namespace, cronJob.Name, err)
	}
	return SyncResult{Updated: true, PreviousImage: previousImage, SourceImage: sourceImage}, nil
}

func parseAssociation(cronJob *batchv1.CronJob) (association, bool, error) {
	a := cronJob.GetAnnotations()
	if a[FollowWorkloadAnnotation] != "true" {
		return association{}, false, nil
	}
	if a[WorkloadKindAnnotation] != "Deployment" {
		return association{}, true, fmt.Errorf("annotation %s must be Deployment", WorkloadKindAnnotation)
	}
	assoc := association{
		workloadName: a[WorkloadNameAnnotation],
		sourceName:   a[SourceContainerAnnotation],
		targetName:   a[TargetContainerAnnotation],
	}
	if assoc.workloadName == "" || assoc.sourceName == "" || assoc.targetName == "" {
		return association{}, true, fmt.Errorf("follow-workload annotations must include workload, source and target container names")
	}
	return assoc, true, nil
}

func containerImage(containers []corev1.Container, name string) (string, bool) {
	for _, container := range containers {
		if container.Name == name {
			return container.Image, true
		}
	}
	return "", false
}

func findContainer(containers *[]corev1.Container, name string) *corev1.Container {
	for i := range *containers {
		if (*containers)[i].Name == name {
			return &(*containers)[i]
		}
	}
	return nil
}
