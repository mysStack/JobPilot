package workloadsync

const (
	workloadNameIndex = "jobpilot.io/index-workload-name"

	FollowWorkloadAnnotation  = "jobpilot.io/follow-workload"
	WorkloadKindAnnotation    = "jobpilot.io/workload-kind"
	WorkloadNameAnnotation    = "jobpilot.io/workload-name"
	SourceContainerAnnotation = "jobpilot.io/source-container-name"
	TargetContainerAnnotation = "jobpilot.io/target-container-name"

	ImageSyncStateAnnotation            = "jobpilot.io/image-sync-state"
	ImageSyncStateSynced                = "Synced"
	ImageSyncSourceImageAnnotation      = "jobpilot.io/image-sync-source"
	ImageSyncSourceGenerationAnnotation = "jobpilot.io/image-sync-source-generation"
	ImageSyncTimestampAnnotation        = "jobpilot.io/image-sync-timestamp"
)
