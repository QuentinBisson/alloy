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
	kubetypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestIsJobPod(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "pod owned by Job",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job-pod",
					Namespace: "default",
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
			},
			expected: true,
		},
		{
			name: "pod owned by CronJob",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cronjob-pod",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "batch/v1",
							Kind:       "CronJob",
							Name:       "test-cronjob",
							UID:        "cronjob-uid-123",
							Controller: &[]bool{true}[0],
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod owned by Deployment",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment-pod",
					Namespace: "default",
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
			},
			expected: false,
		},
		{
			name: "pod with no owner references",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "standalone-pod",
					Namespace: "default",
				},
			},
			expected: false,
		},
		{
			name: "pod owned by Job but not controller",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job-pod",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "batch/v1",
							Kind:       "Job",
							Name:       "test-job",
							UID:        "job-uid-123",
							Controller: &[]bool{false}[0],
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isJobPod(tc.pod)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestIsTargetJobPod(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "job pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job-pod",
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
			},
			expected: true,
		},
		{
			name: "regular pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "regular-pod",
					Namespace: "default",
					UID:       "regular-pod-uid",
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create a fake Kubernetes client with the test pod
			fakeClient := fake.NewSimpleClientset(tc.pod)

			// Create a target for testing
			target := &Target{
				namespacedName: kubetypes.NamespacedName{
					Namespace: tc.pod.Namespace,
					Name:      tc.pod.Name,
				},
				uid: string(tc.pod.UID),
			}

			// Create a tailer with the fake client
			tailer := &tailer{
				log:    log.NewNopLogger(),
				target: target,
				opts: &Options{
					Client: fakeClient,
				},
			}

			ctx := context.Background()
			result, err := tailer.isTargetJobPod(ctx)

			require.NoError(t, err)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestJobContainerFinishedLogging(t *testing.T) {
	// Note: This function should only be called for job pods.
	// Regular pods should use containerTerminated() instead.
	tests := []struct {
		name           string
		pod            *corev1.Pod
		containerName  string
		expectedResult bool
		expectedError  bool
		description    string
	}{
		{
			name: "job pod with running container",
			pod: &corev1.Pod{
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
					Containers: []corev1.Container{
						{Name: "job-container"},
					},
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
			},
			containerName:  "job-container",
			expectedResult: false,
			expectedError:  false,
			description:    "Job pod with running container should not be finished",
		},
		{
			name: "job pod recently terminated - within minimum wait time",
			pod: &corev1.Pod{
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
					Containers: []corev1.Container{
						{Name: "job-container"},
					},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "job-container",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode:   0,
									FinishedAt: metav1.Time{Time: time.Now().Add(-5 * time.Second)}, // 5 seconds ago
								},
							},
						},
					},
				},
			},
			containerName:  "job-container",
			expectedResult: false,
			expectedError:  false,
			description:    "Job pod terminated recently should continue logging within minimum wait time",
		},
		{
			name: "job pod terminated beyond maximum wait time",
			pod: &corev1.Pod{
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
					Containers: []corev1.Container{
						{Name: "job-container"},
					},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "job-container",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode:   0,
									FinishedAt: metav1.Time{Time: time.Now().Add(-70 * time.Second)}, // 70 seconds ago
								},
							},
						},
					},
				},
			},
			containerName:  "job-container",
			expectedResult: true,
			expectedError:  false,
			description:    "Job pod terminated beyond maximum wait time should be finished",
		},
		{
			name: "job pod being deleted",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "job-pod",
					Namespace:         "default",
					UID:               "job-pod-uid",
					DeletionTimestamp: &metav1.Time{Time: time.Now()}, // Pod is being deleted
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
					Containers: []corev1.Container{
						{Name: "job-container"},
					},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "job-container",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode:   0,
									FinishedAt: metav1.Time{Time: time.Now().Add(-5 * time.Second)}, // 5 seconds ago
								},
							},
						},
					},
				},
			},
			containerName:  "job-container",
			expectedResult: true,
			expectedError:  false,
			description:    "Job pod being deleted should be finished regardless of grace period",
		},
		{
			name: "job pod not found (deleted)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deleted-job-pod",
					Namespace: "default",
					UID:       "deleted-pod-uid",
				},
			},
			containerName:  "job-container",
			expectedResult: true,
			expectedError:  false,
			description:    "Job pod that was deleted should be finished",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var fakeClient *fake.Clientset

			// Special handling for the "pod not found" test case
			if tc.name == "job pod not found (deleted)" {
				// Create a fake client without the pod to simulate deletion
				fakeClient = fake.NewSimpleClientset()
			} else {
				// Create a fake Kubernetes client with the test pod
				fakeClient = fake.NewSimpleClientset(tc.pod)
			}

			// Create a target for testing
			target := &Target{
				namespacedName: kubetypes.NamespacedName{
					Namespace: tc.pod.Namespace,
					Name:      tc.pod.Name,
				},
				containerName: tc.containerName,
				uid:           string(tc.pod.UID),
			}

			// Create a tailer with the fake client
			tailer := &tailer{
				log:    log.NewNopLogger(),
				target: target,
				opts: &Options{
					Client: fakeClient,
				},
			}

			ctx := context.Background()
			result, err := tailer.jobContainerFinishedLogging(ctx)

			if tc.expectedError {
				assert.Error(t, err, tc.description)
			} else {
				assert.NoError(t, err, tc.description)
				assert.Equal(t, tc.expectedResult, result, tc.description)
			}
		})
	}
}

func TestContainerTerminated_JobHandling(t *testing.T) {
	tests := []struct {
		name           string
		pod            *corev1.Pod
		containerName  string
		expectedResult bool
		description    string
	}{
		{
			name: "job pod with terminated container - containerTerminated should use standard logic",
			pod: &corev1.Pod{
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
					Containers: []corev1.Container{
						{Name: "job-container"},
					},
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
			},
			containerName:  "job-container",
			expectedResult: true, // containerTerminated should use standard logic - terminated container with exit code 0 and OnFailure policy = terminated
			description:    "Job pod with successfully terminated container - containerTerminated should use standard Kubernetes logic",
		},
		{
			name: "regular pod with terminated container should stop tailing",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "regular-pod",
					Namespace: "default",
					UID:       "regular-pod-uid",
					// No owner references - this is a regular pod
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{Name: "app-container"},
					},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "app-container",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode:   0,
									FinishedAt: metav1.Time{Time: time.Now()},
								},
							},
						},
					},
				},
			},
			containerName:  "app-container",
			expectedResult: true, // Should stop tailing (terminated from tailer perspective)
			description:    "Regular pod with successfully terminated container should stop tailing",
		},
		{
			name: "job pod with failed container and OnFailure restart policy should restart",
			pod: &corev1.Pod{
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
					Containers: []corev1.Container{
						{Name: "job-container"},
					},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "job-container",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode:   1, // Failed
									FinishedAt: metav1.Time{Time: time.Now()},
								},
							},
						},
					},
				},
			},
			containerName:  "job-container",
			expectedResult: false, // Should continue tailing (container will restart)
			description:    "Job pod with failed container and OnFailure restart policy should continue tailing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create a fake Kubernetes client with the test pod
			fakeClient := fake.NewSimpleClientset(tc.pod)

			// Create a target for testing
			target := &Target{
				namespacedName: kubetypes.NamespacedName{
					Namespace: tc.pod.Namespace,
					Name:      tc.pod.Name,
				},
				containerName: tc.containerName,
				uid:           string(tc.pod.UID),
			}

			// Create a tailer with the fake client
			tailer := &tailer{
				log:    log.NewNopLogger(),
				target: target,
				opts: &Options{
					Client: fakeClient,
				},
			}

			ctx := context.Background()
			result, err := tailer.containerTerminated(ctx)

			require.NoError(t, err)
			assert.Equal(t, tc.expectedResult, result, tc.description)
		})
	}
}

// TestJobLogCollection_NoDuplication ensures that job pods don't result in
// duplicate log collection by verifying that targets are properly deduplicated
func TestJobLogCollection_NoDuplication(t *testing.T) {

	// Create two identical targets (simulating potential duplication)
	origLabels := labels.New(
		labels.Label{Name: "__pod_namespace__", Value: "default"},
		labels.Label{Name: "__pod_name__", Value: "test-job-pod"},
		labels.Label{Name: "__pod_container_name__", Value: "job-container"},
		labels.Label{Name: "__pod_uid__", Value: "job-pod-uid-123"},
		labels.Label{Name: "job", Value: "test-job"},
	)

	target1 := NewTarget(origLabels, origLabels)
	target2 := NewTarget(origLabels, origLabels)

	// Verify that identical targets have the same hash
	assert.Equal(t, target1.Hash(), target2.Hash(), "Identical targets should have the same hash")

	// Verify that the targets are considered equal
	task1 := &tailerTask{Target: target1}
	task2 := &tailerTask{Target: target2}
	assert.True(t, task1.Equals(task2), "Identical tailer tasks should be equal")

	// Verify that position entries are the same (preventing duplicate position tracking)
	entry1 := entryForTarget(target1)
	entry2 := entryForTarget(target2)
	assert.Equal(t, entry1, entry2, "Identical targets should have the same position entry")
}
