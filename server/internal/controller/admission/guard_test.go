package admission

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	cradmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestGuardAllowsJobUsingCurrentDeploymentImage(t *testing.T) {
	guard, request := newGuardFixture(t, "registry/wms:v2", "registry/wms:v2", true)

	response := guard.Handle(context.Background(), request)
	if !response.Allowed {
		t.Fatalf("job was denied: %s", response.Result.Message)
	}
}

func TestGuardRejectsJobUsingStaleImage(t *testing.T) {
	guard, request := newGuardFixture(t, "registry/wms:v2", "registry/wms:v1", true)

	response := guard.Handle(context.Background(), request)
	if response.Allowed {
		t.Fatal("stale job image was allowed")
	}
	if !strings.Contains(response.Result.Message, "ImageOutOfSync") {
		t.Fatalf("reason = %q, want ImageOutOfSync", response.Result.Message)
	}
}

func TestGuardAllowsUnmanagedJob(t *testing.T) {
	guard, request := newGuardFixture(t, "registry/wms:v2", "registry/wms:v1", false)

	response := guard.Handle(context.Background(), request)
	if !response.Allowed {
		t.Fatalf("unmanaged job was denied: %s", response.Result.Message)
	}
}

func TestGuardRejectsWhenSourceDeploymentIsMissing(t *testing.T) {
	guard, request := newGuardFixture(t, "registry/wms:v2", "registry/wms:v2", true)
	guard.Reader = fake.NewClientBuilder().WithScheme(requestScheme()).WithObjects(&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{
		Name: "inventory-sync", Namespace: "wms", UID: types.UID("cronjob-uid"), Annotations: map[string]string{
			"jobpilot.io/follow-workload":       "true",
			"jobpilot.io/workload-kind":         "Deployment",
			"jobpilot.io/workload-name":         "wms",
			"jobpilot.io/source-container-name": "wms",
			"jobpilot.io/target-container-name": "job-runner",
		},
	}}).Build()

	response := guard.Handle(context.Background(), request)
	if response.Allowed {
		t.Fatal("job was allowed without source deployment")
	}
	if !strings.Contains(response.Result.Message, "WorkloadNotFound") {
		t.Fatalf("reason = %q, want WorkloadNotFound", response.Result.Message)
	}
}

func TestGuardAllowsWhenOwnerCronJobNoLongerExists(t *testing.T) {
	guard, request := newGuardFixture(t, "registry/wms:v2", "registry/wms:v1", true)
	guard.Reader = fake.NewClientBuilder().WithScheme(requestScheme()).WithObjects().Build()

	response := guard.Handle(context.Background(), request)
	if !response.Allowed {
		t.Fatalf("orphaned Job was denied: %s", response.Result.Message)
	}
}

func TestGuardRejectsJobWhoseOwnerUIDDoesNotMatchCronJob(t *testing.T) {
	guard, request := newGuardFixture(t, "registry/wms:v2", "registry/wms:v2", true)
	job := &batchv1.Job{}
	if err := guard.Decoder.Decode(request, job); err != nil {
		t.Fatal(err)
	}
	job.OwnerReferences[0].UID = types.UID("stale-owner-uid")
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	request.Object = runtime.RawExtension{Raw: raw}

	response := guard.Handle(context.Background(), request)
	if response.Allowed {
		t.Fatal("Job with stale owner UID was allowed")
	}
	if !strings.Contains(response.Result.Message, "OwnerReferenceMismatch") {
		t.Fatalf("reason = %q, want OwnerReferenceMismatch", response.Result.Message)
	}
}

func TestGuardRejectsManagedJobWithInvalidAssociation(t *testing.T) {
	guard, request := newGuardFixture(t, "registry/wms:v2", "registry/wms:v2", true)
	invalidCronJob := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "inventory-sync", Namespace: "wms", UID: types.UID("cronjob-uid"), Annotations: map[string]string{
		"jobpilot.io/follow-workload":       "true",
		"jobpilot.io/workload-kind":         "Deployment",
		"jobpilot.io/workload-name":         "wms",
		"jobpilot.io/source-container-name": "wms",
	}}}
	guard.Reader = fake.NewClientBuilder().WithScheme(requestScheme()).WithObjects(invalidCronJob).Build()

	response := guard.Handle(context.Background(), request)
	if response.Allowed {
		t.Fatal("Job with an invalid managed association was allowed")
	}
	if !strings.Contains(response.Result.Message, "InvalidAssociation") {
		t.Fatalf("reason = %q, want InvalidAssociation", response.Result.Message)
	}
}

func TestGuardRecordsImageOutOfSyncDenial(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	guard, request := newGuardFixture(t, "registry/wms:v2", "registry/wms:v1", true)
	guard.Metrics = metrics

	response := guard.Handle(context.Background(), request)
	if response.Allowed {
		t.Fatal("stale image was allowed")
	}
	if got := testutil.ToFloat64(metrics.Requests.WithLabelValues("denied", "ImageOutOfSync")); got != 1 {
		t.Fatalf("denied ImageOutOfSync metric = %v, want 1", got)
	}
}

func newGuardFixture(t *testing.T, sourceImage, jobImage string, managed bool) (*Guard, cradmission.Request) {
	t.Helper()
	scheme := requestScheme()
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "wms", Namespace: "wms"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "wms", Image: sourceImage}}}}},
	}
	cronJob := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "inventory-sync", Namespace: "wms", UID: types.UID("cronjob-uid"), Annotations: map[string]string{
		"jobpilot.io/follow-workload":       "true",
		"jobpilot.io/workload-kind":         "Deployment",
		"jobpilot.io/workload-name":         "wms",
		"jobpilot.io/source-container-name": "wms",
		"jobpilot.io/target-container-name": "job-runner",
	}}}
	owner := metav1.OwnerReference{APIVersion: "batch/v1", Kind: "CronJob", Name: cronJob.Name, UID: cronJob.UID}
	controller := true
	owner.Controller = &controller
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "inventory-sync-123", Namespace: "wms"}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "job-runner", Image: jobImage}}}}}}
	if managed {
		job.OwnerReferences = []metav1.OwnerReference{owner}
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{deployment, cronJob}
	if managed {
		objects = append(objects, job)
	}
	return &Guard{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), Decoder: cradmission.NewDecoder(scheme)}, cradmission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UID:       types.UID("request-uid"),
		Operation: admissionv1.Create,
		Namespace: "wms",
		Object:    runtime.RawExtension{Raw: raw},
	}}
}

func requestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	return scheme
}
