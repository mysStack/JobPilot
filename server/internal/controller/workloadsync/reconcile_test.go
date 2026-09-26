package workloadsync

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSyncCronJobCopiesDeploymentImageAndReleasesBootstrapSuspend(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "wms", Namespace: "wms"},
		Spec:       appsv1.DeploymentSpec{Template: corePodTemplate("wms", "registry/wms:v2")},
	}
	cronJob := followWorkloadCronJob("inventory-sync", "wms", "registry/wms:v1", true)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, cronJob).Build()
	reconciler := &Reconciler{Client: c}

	if _, err := reconciler.SyncCronJob(ctx, cronJob); err != nil {
		t.Fatalf("SyncCronJob() error = %v", err)
	}

	updated := &batchv1.CronJob{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(cronJob), updated); err != nil {
		t.Fatal(err)
	}
	if got := updated.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image; got != "registry/wms:v2" {
		t.Fatalf("image = %q, want %q", got, "registry/wms:v2")
	}
	if updated.Spec.Suspend == nil || *updated.Spec.Suspend {
		t.Fatalf("suspend = %v, want false", updated.Spec.Suspend)
	}
	if got := updated.Annotations[ImageSyncStateAnnotation]; got != ImageSyncStateSynced {
		t.Fatalf("sync state = %q, want %q", got, ImageSyncStateSynced)
	}
}

func TestSyncCronJobDoesNotModifyUnmanagedCronJob(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "manual", Namespace: "wms"},
		Spec:       batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corePodTemplate("job", "registry/wms:v1")}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cronJob).Build()
	reconciler := &Reconciler{Client: c}

	if _, err := reconciler.SyncCronJob(ctx, cronJob); err != nil {
		t.Fatalf("SyncCronJob() error = %v", err)
	}

	updated := &batchv1.CronJob{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(cronJob), updated); err != nil {
		t.Fatal(err)
	}
	if got := updated.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image; got != "registry/wms:v1" {
		t.Fatalf("image = %q, want unmanaged image unchanged", got)
	}
}

func TestSyncCronJobReturnsErrorForMissingSourceContainer(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "wms", Namespace: "wms"},
		Spec:       appsv1.DeploymentSpec{Template: corePodTemplate("other", "registry/wms:v2")},
	}
	cronJob := followWorkloadCronJob("inventory-sync", "wms", "registry/wms:v1", true)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, cronJob).Build()
	reconciler := &Reconciler{Client: c}

	if _, err := reconciler.SyncCronJob(ctx, cronJob); err == nil {
		t.Fatal("SyncCronJob() error = nil, want missing source container error")
	} else {
		var syncError *SyncError
		if !errors.As(err, &syncError) || syncError.Reason != ReasonSourceContainerNotFound {
			t.Fatalf("error = %v, want %s", err, ReasonSourceContainerNotFound)
		}
	}
}

func TestSyncCronJobUpdatesMetadataWhenDeploymentImageChanges(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "wms", Namespace: "wms", Generation: 2},
		Spec:       appsv1.DeploymentSpec{Template: corePodTemplate("wms", "registry/wms:v2")},
	}
	cronJob := followWorkloadCronJob("inventory-sync", "wms", "registry/wms:v1", false)
	cronJob.Annotations[ImageSyncStateAnnotation] = ImageSyncStateSynced
	cronJob.Annotations[ImageSyncSourceImageAnnotation] = "registry/wms:v1"
	cronJob.Annotations[ImageSyncSourceGenerationAnnotation] = "1"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, cronJob).Build()
	reconciler := &Reconciler{Client: c}

	if _, err := reconciler.SyncCronJob(ctx, cronJob); err != nil {
		t.Fatalf("SyncCronJob() error = %v", err)
	}
	updated := &batchv1.CronJob{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(cronJob), updated); err != nil {
		t.Fatal(err)
	}
	if got := updated.Annotations[ImageSyncSourceImageAnnotation]; got != "registry/wms:v2" {
		t.Fatalf("source image annotation = %q, want v2", got)
	}
	if got := updated.Annotations[ImageSyncSourceGenerationAnnotation]; got != "2" {
		t.Fatalf("source generation annotation = %q, want 2", got)
	}
}

func TestSyncCronJobPreservesManualSuspendAfterInitialSync(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "wms", Namespace: "wms", Generation: 3},
		Spec:       appsv1.DeploymentSpec{Template: corePodTemplate("wms", "registry/wms:v3")},
	}
	cronJob := followWorkloadCronJob("inventory-sync", "wms", "registry/wms:v2", true)
	cronJob.Annotations[ImageSyncStateAnnotation] = ImageSyncStateSynced
	cronJob.Annotations[ImageSyncSourceImageAnnotation] = "registry/wms:v2"
	cronJob.Annotations[ImageSyncSourceGenerationAnnotation] = "2"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, cronJob).Build()
	reconciler := &Reconciler{Client: c}

	if _, err := reconciler.SyncCronJob(ctx, cronJob); err != nil {
		t.Fatalf("SyncCronJob() error = %v", err)
	}
	updated := &batchv1.CronJob{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(cronJob), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Suspend == nil || !*updated.Spec.Suspend {
		t.Fatalf("suspend = %v, want true after manual pause", updated.Spec.Suspend)
	}
}

func TestRequestsForDeploymentReturnsOnlyAssociatedCronJobs(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)

	matching := followWorkloadCronJob("inventory-sync", "wms", "registry/wms:v1", false)
	nonMatching := followWorkloadCronJob("other-workload", "wms", "registry/wms:v1", false)
	nonMatching.Annotations[WorkloadNameAnnotation] = "wes"
	unmanaged := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "manual", Namespace: "wms"}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&batchv1.CronJob{}, workloadNameIndex, func(object client.Object) []string {
			association, managed, err := parseAssociation(object.(*batchv1.CronJob))
			if err != nil || !managed {
				return nil
			}
			return []string{association.workloadName}
		}).
		WithObjects(matching, nonMatching, unmanaged).
		Build()
	controller := &Controller{Reconciler: &Reconciler{Client: c}}

	requests := controller.requestsForDeployment(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "wms", Namespace: "wms"}})
	if len(requests) != 1 || requests[0].Name != "inventory-sync" || requests[0].Namespace != "wms" {
		t.Fatalf("requests = %#v, want only wms/inventory-sync", requests)
	}
}

func TestControllerHonorsManagedNamespaces(t *testing.T) {
	controller := &Controller{ManagedNamespaces: map[string]struct{}{"wms": {}}}
	if !controller.managesNamespace("wms") {
		t.Fatal("wms should be managed")
	}
	if controller.managesNamespace("wes") {
		t.Fatal("wes should not be managed")
	}
}

func followWorkloadCronJob(name, namespace, image string, suspended bool) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				FollowWorkloadAnnotation:  "true",
				WorkloadKindAnnotation:    "Deployment",
				WorkloadNameAnnotation:    "wms",
				SourceContainerAnnotation: "wms",
				TargetContainerAnnotation: "job-runner",
			},
		},
		Spec: batchv1.CronJobSpec{
			Suspend:     &suspended,
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corePodTemplate("job-runner", image)}},
		},
	}
}

func corePodTemplate(containerName, image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: containerName, Image: image}},
		},
	}
}
