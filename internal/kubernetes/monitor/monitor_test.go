package monitor

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"spot-fargate-orchestrator/internal/kubernetes/client"
)

func TestNewRolloutMonitor(t *testing.T) {
	// Create a mock client (we'll use nil for this basic test)
	var mockClient *client.K8sClient

	monitor := NewRolloutMonitor(mockClient)

	if monitor == nil {
		t.Fatal("NewRolloutMonitor() returned nil")
	}

	if monitor.client != mockClient {
		t.Error("NewRolloutMonitor() did not set client correctly")
	}

	if monitor.logger == nil {
		t.Error("NewRolloutMonitor() did not initialize logger")
	}
}

func TestIsRolloutComplete(t *testing.T) {
	monitor := &RolloutMonitor{}

	tests := []struct {
		name       string
		deployment *appsv1.Deployment
		expected   bool
	}{
		{
			name: "complete rollout",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 2,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(3),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2,
					UpdatedReplicas:    3,
					ReadyReplicas:      3,
					AvailableReplicas:  3,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Status: "True",
							Reason: "NewReplicaSetAvailable",
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "incomplete rollout - generation mismatch",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 3,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(3),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2, // Different from generation
					UpdatedReplicas:    3,
					ReadyReplicas:      3,
					AvailableReplicas:  3,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Status: "True",
							Reason: "NewReplicaSetAvailable",
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "incomplete rollout - not all replicas ready",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 2,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(3),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2,
					UpdatedReplicas:    3,
					ReadyReplicas:      2, // Not all ready
					AvailableReplicas:  3,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Status: "True",
							Reason: "NewReplicaSetAvailable",
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "incomplete rollout - missing condition",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 2,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(3),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2,
					UpdatedReplicas:    3,
					ReadyReplicas:      3,
					AvailableReplicas:  3,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Status: "True",
							Reason: "ReplicaSetUpdated", // Different reason
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := monitor.isRolloutComplete(tt.deployment)
			if result != tt.expected {
				t.Errorf("isRolloutComplete() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestIsRolloutFailed(t *testing.T) {
	monitor := &RolloutMonitor{}

	tests := []struct {
		name       string
		deployment *appsv1.Deployment
		expected   bool
	}{
		{
			name: "failed rollout - progress deadline exceeded",
			deployment: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Status: "False",
							Reason: "ProgressDeadlineExceeded",
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "failed rollout - replica failure",
			deployment: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentReplicaFailure,
							Status: "True",
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "successful rollout",
			deployment: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Status: "True",
							Reason: "NewReplicaSetAvailable",
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "no conditions",
			deployment: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := monitor.isRolloutFailed(tt.deployment)
			if result != tt.expected {
				t.Errorf("isRolloutFailed() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestGetRolloutFailureMessage(t *testing.T) {
	monitor := &RolloutMonitor{}

	tests := []struct {
		name       string
		deployment *appsv1.Deployment
		expected   string
	}{
		{
			name: "progress deadline exceeded message",
			deployment: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:    appsv1.DeploymentProgressing,
							Status:  "False",
							Message: "ReplicaSet has timed out progressing.",
						},
					},
				},
			},
			expected: "ReplicaSet has timed out progressing.",
		},
		{
			name: "replica failure message",
			deployment: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:    appsv1.DeploymentReplicaFailure,
							Status:  "True",
							Message: "Pod has unbound immediate PersistentVolumeClaims.",
						},
					},
				},
			},
			expected: "Pod has unbound immediate PersistentVolumeClaims.",
		},
		{
			name: "no failure conditions",
			deployment: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Status: "True",
							Reason: "NewReplicaSetAvailable",
						},
					},
				},
			},
			expected: "Rollout failed for unknown reason",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := monitor.getRolloutFailureMessage(tt.deployment)
			if result != tt.expected {
				t.Errorf("getRolloutFailureMessage() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

// Helper function to create int32 pointer
func int32Ptr(i int32) *int32 {
	return &i
}
