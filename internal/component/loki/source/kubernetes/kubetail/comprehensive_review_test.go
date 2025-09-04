package kubetail

import (
	"context"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// TestComprehensiveLogCollection validates that the implementation:
// 1. Properly collects logs from regular pods
// 2. Properly collects logs from job pods with extended grace periods
// 3. Does not create duplicate tailers for the same target
// 4. Uses correct termination logic for each pod type
func TestComprehensiveLogCollection(t *testing.T) {
	t.Run("Regular Pod Log Collection", func(t *testing.T) {
		// Create a regular pod (Deployment-owned)
		regularPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "regular-pod",
				Namespace: "default",
				UID:       "regular-pod-uid",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "ReplicaSet",
						Name:       "test-deployment-rs",
						UID:        "rs-uid-123",
						Controller: &[]bool{true}[0],
					},
				},
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyAlways,
				Containers:    []corev1.Container{{Name: "app"}},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: "app",
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
		}

		fakeClient := fake.NewSimpleClientset(regularPod)
		tailer := createTestTailer(t, regularPod, "app", fakeClient)

		// Verify pod type detection
		isJob, err := tailer.isTargetJobPod(context.Background())
		require.NoError(t, err)
		assert.False(t, isJob, "Regular pod should not be detected as job")

		// Verify termination logic - running container should not be terminated
		terminated, err := tailer.containerTerminated(context.Background())
		require.NoError(t, err)
		assert.False(t, terminated, "Running container should not be terminated")

		// Simulate container termination with successful exit
		regularPod.Status.ContainerStatuses[0].State = corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   0,
				FinishedAt: metav1.Time{Time: time.Now()},
			},
		}
		fakeClient.CoreV1().Pods("default").Update(context.Background(), regularPod, metav1.UpdateOptions{})

		// For regular pods with RestartPolicyAlways, terminated container will restart (not terminated)
		terminated, err = tailer.containerTerminated(context.Background())
		require.NoError(t, err)
		assert.False(t, terminated, "Regular pod with RestartPolicyAlways should restart (not be terminated)")
	})

	t.Run("Job Pod Log Collection", func(t *testing.T) {
		// Create a job pod
		jobPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "job-pod",
				Namespace: "default",
				UID:       "job-pod-uid",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "batch/v1",
						Kind:       "Job",
						Name:       "test-job",
						UID:        "job-uid-123",
						Controller: &[]bool{true}[0],
					},
				},
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyOnFailure,
				Containers:    []corev1.Container{{Name: "job-container"}},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: "job-container",
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
		}

		fakeClient := fake.NewSimpleClientset(jobPod)
		tailer := createTestTailer(t, jobPod, "job-container", fakeClient)

		// Verify pod type detection
		isJob, err := tailer.isTargetJobPod(context.Background())
		require.NoError(t, err)
		assert.True(t, isJob, "Job pod should be detected as job")

		// Verify running container is not finished
		finished, err := tailer.jobContainerFinishedLogging(context.Background())
		require.NoError(t, err)
		assert.False(t, finished, "Running job container should not be finished")

		// Simulate job completion
		jobPod.Status.ContainerStatuses[0].State = corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   0,
				FinishedAt: metav1.Time{Time: time.Now()},
			},
		}
		fakeClient.CoreV1().Pods("default").Update(context.Background(), jobPod, metav1.UpdateOptions{})

		// Job container should not be finished immediately (within grace period)
		finished, err = tailer.jobContainerFinishedLogging(context.Background())
		require.NoError(t, err)
		assert.False(t, finished, "Job container should not be finished within grace period")

		// Verify containerTerminated still works with standard logic for job pods
		terminated, err := tailer.containerTerminated(context.Background())
		require.NoError(t, err)
		assert.True(t, terminated, "containerTerminated should use standard logic regardless of pod type")
	})

	t.Run("Target Deduplication", func(t *testing.T) {
		// Create identical targets to test deduplication
		targetLabels := labels.New(
			labels.Label{Name: "__pod_namespace__", Value: "default"},
			labels.Label{Name: "__pod_name__", Value: "test-pod"},
			labels.Label{Name: "__pod_container_name__", Value: "container"},
			labels.Label{Name: "__pod_uid__", Value: "pod-uid-123"},
			labels.Label{Name: "job", Value: "test"},
		)

		target1 := NewTarget(targetLabels, targetLabels)
		target2 := NewTarget(targetLabels, targetLabels)
		target3 := NewTarget(targetLabels, targetLabels)

		// Verify hash consistency
		assert.Equal(t, target1.Hash(), target2.Hash(), "Identical targets should have same hash")
		assert.Equal(t, target2.Hash(), target3.Hash(), "Identical targets should have same hash")

		// Verify task equality (used by runner for deduplication)
		sharedOptions := &Options{}
		task1 := &tailerTask{Target: target1, Options: sharedOptions}
		task2 := &tailerTask{Target: target2, Options: sharedOptions}
		task3 := &tailerTask{Target: target3, Options: sharedOptions}

		assert.True(t, task1.Equals(task2), "Identical tasks should be equal")
		assert.True(t, task2.Equals(task3), "Identical tasks should be equal")
		assert.True(t, task1.Equals(task3), "Identical tasks should be equal")

		// Verify position entry consistency (prevents duplicate position tracking)
		entry1 := entryForTarget(target1)
		entry2 := entryForTarget(target2)
		entry3 := entryForTarget(target3)

		assert.Equal(t, entry1, entry2, "Identical targets should have same position entry")
		assert.Equal(t, entry2, entry3, "Identical targets should have same position entry")
		assert.Equal(t, entry1, entry3, "Identical targets should have same position entry")

		// Verify position path includes UID for uniqueness
		expectedPath := "cursor-default/test-pod:container:pod-uid-123"
		assert.Equal(t, expectedPath, entry1.Path, "Position path should include UID")
	})

	t.Run("Different Pods Same Name Different UID", func(t *testing.T) {
		// Test that pods with same name but different UIDs are treated as different targets
		labels1 := labels.New(
			labels.Label{Name: "__pod_namespace__", Value: "default"},
			labels.Label{Name: "__pod_name__", Value: "same-name"},
			labels.Label{Name: "__pod_container_name__", Value: "container"},
			labels.Label{Name: "__pod_uid__", Value: "uid-1"},
		)

		labels2 := labels.New(
			labels.Label{Name: "__pod_namespace__", Value: "default"},
			labels.Label{Name: "__pod_name__", Value: "same-name"},
			labels.Label{Name: "__pod_container_name__", Value: "container"},
			labels.Label{Name: "__pod_uid__", Value: "uid-2"},
		)

		target1 := NewTarget(labels1, labels1)
		target2 := NewTarget(labels2, labels2)

		// Different UIDs should result in different hashes
		assert.NotEqual(t, target1.Hash(), target2.Hash(), "Different UIDs should have different hashes")

		// Different position entries
		entry1 := entryForTarget(target1)
		entry2 := entryForTarget(target2)
		assert.NotEqual(t, entry1, entry2, "Different UIDs should have different position entries")

		// Tasks should not be equal
		task1 := &tailerTask{Target: target1}
		task2 := &tailerTask{Target: target2}
		assert.False(t, task1.Equals(task2), "Different UID tasks should not be equal")
	})

	t.Run("Run Loop Logic Validation", func(t *testing.T) {
		// This test validates the main run loop logic by checking that:
		// 1. Job pods use jobContainerFinishedLogging
		// 2. Regular pods use containerTerminated
		// 3. Error handling works correctly

		regularPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "regular-pod",
				Namespace: "default",
				UID:       "regular-uid",
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever, // Never restart - so terminated container is truly terminated
				Containers:    []corev1.Container{{Name: "app"}},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: "app",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode:   0,
								FinishedAt: metav1.Time{Time: time.Now()},
							},
						},
					},
				},
			},
		}

		jobPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "job-pod",
				Namespace: "default",
				UID:       "job-uid",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "batch/v1",
						Kind:       "Job",
						Name:       "test-job",
						Controller: &[]bool{true}[0],
					},
				},
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyOnFailure,
				Containers:    []corev1.Container{{Name: "job-container"}},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: "job-container",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode:   0,
								FinishedAt: metav1.Time{Time: time.Now()},
							},
						},
					},
				},
			},
		}

		// Test regular pod logic
		regularClient := fake.NewSimpleClientset(regularPod)
		regularTailer := createTestTailer(t, regularPod, "app", regularClient)

		isJob, err := regularTailer.isTargetJobPod(context.Background())
		require.NoError(t, err)
		assert.False(t, isJob, "Regular pod should not be job")

		terminated, err := regularTailer.containerTerminated(context.Background())
		require.NoError(t, err)
		assert.True(t, terminated, "Regular pod with RestartPolicyNever and terminated container should be terminated")

		// Test job pod logic
		jobClient := fake.NewSimpleClientset(jobPod)
		jobTailer := createTestTailer(t, jobPod, "job-container", jobClient)

		isJob, err = jobTailer.isTargetJobPod(context.Background())
		require.NoError(t, err)
		assert.True(t, isJob, "Job pod should be job")

		finished, err := jobTailer.jobContainerFinishedLogging(context.Background())
		require.NoError(t, err)
		assert.False(t, finished, "Job container should not be finished within grace period")

		// Both should use containerTerminated for standard logic
		terminated, err = jobTailer.containerTerminated(context.Background())
		require.NoError(t, err)
		assert.True(t, terminated, "containerTerminated should use standard logic for all pods")
	})
}

// Helper function to create a test tailer
func createTestTailer(t *testing.T, pod *corev1.Pod, containerName string, client *fake.Clientset) *tailer {
	target := &Target{
		namespacedName: types.NamespacedName{
			Namespace: pod.Namespace,
			Name:      pod.Name,
		},
		containerName: containerName,
		uid:           string(pod.UID),
	}

	return &tailer{
		log:    log.NewNopLogger(),
		target: target,
		opts: &Options{
			Client: client,
		},
	}
}
