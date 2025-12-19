package monitor

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"

	"spot-fargate-orchestrator/internal/kubernetes/client"
	"spot-fargate-orchestrator/internal/logger"
)

// RolloutReplicas represents replica counts during rollout
type RolloutReplicas struct {
	Desired   int32 `json:"desired"`
	Updated   int32 `json:"updated"`
	Ready     int32 `json:"ready"`
	Available int32 `json:"available"`
}

// RolloutStatus represents the status of a deployment rollout
type RolloutStatus struct {
	Complete bool            `json:"complete"`
	Failed   bool            `json:"failed"`
	Message  string          `json:"message"`
	Replicas RolloutReplicas `json:"replicas"`
}

// RolloutMonitor monitors deployment rollout status
type RolloutMonitor struct {
	client *client.K8sClient
	logger *logger.Logger
}

// NewRolloutMonitor creates a new rollout monitor
func NewRolloutMonitor(client *client.K8sClient) *RolloutMonitor {
	return &RolloutMonitor{
		client: client,
		logger: logger.NewDefault("rollout-monitor"),
	}
}

// WaitForRollout waits for a deployment rollout to complete
func (r *RolloutMonitor) WaitForRollout(ctx context.Context, deploymentName string, timeout time.Duration) error {
	r.logger.Info("Starting rollout monitoring",
		"deployment", deploymentName,
		"timeout", timeout.String(),
		"namespace", r.client.GetNamespace())

	// Create a timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Get the initial deployment to establish baseline
	deployment, err := r.client.GetClientset().AppsV1().Deployments(r.client.GetNamespace()).Get(
		timeoutCtx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}

	r.logger.Debug("Initial deployment state",
		"deployment", deploymentName,
		"generation", deployment.Generation,
		"observed_generation", deployment.Status.ObservedGeneration,
		"replicas", deployment.Status.Replicas,
		"ready_replicas", deployment.Status.ReadyReplicas)

	// If the deployment is already up to date, return immediately
	if r.isRolloutComplete(deployment) {
		r.logger.Info("Rollout already complete",
			"deployment", deploymentName)
		return nil
	}

	// Watch for deployment changes
	watchOptions := metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", deploymentName),
	}

	watcher, err := r.client.GetClientset().AppsV1().Deployments(r.client.GetNamespace()).Watch(
		timeoutCtx, watchOptions)
	if err != nil {
		return fmt.Errorf("failed to create deployment watcher: %w", err)
	}
	defer watcher.Stop()

	r.logger.Debug("Watching deployment for rollout completion",
		"deployment", deploymentName)

	// Monitor the rollout
	for {
		select {
		case <-timeoutCtx.Done():
			if timeoutCtx.Err() == context.DeadlineExceeded {
				r.logger.Error("Rollout timeout exceeded",
					"deployment", deploymentName,
					"timeout", timeout.String())
				return fmt.Errorf("rollout timeout exceeded for deployment %s after %v", deploymentName, timeout)
			}
			return timeoutCtx.Err()

		case event, ok := <-watcher.ResultChan():
			if !ok {
				r.logger.Warn("Watch channel closed, restarting watch",
					"deployment", deploymentName)
				// Restart the watcher
				watcher.Stop()
				watcher, err = r.client.GetClientset().AppsV1().Deployments(r.client.GetNamespace()).Watch(
					timeoutCtx, watchOptions)
				if err != nil {
					return fmt.Errorf("failed to restart deployment watcher: %w", err)
				}
				continue
			}

			if event.Type == watch.Error {
				r.logger.Error("Watch error occurred",
					"deployment", deploymentName,
					"error", event.Object)
				continue
			}

			if event.Type == watch.Modified || event.Type == watch.Added {
				deployment, ok := event.Object.(*appsv1.Deployment)
				if !ok {
					r.logger.Warn("Unexpected object type in watch event",
						"deployment", deploymentName,
						"type", fmt.Sprintf("%T", event.Object))
					continue
				}

				r.logger.Debug("Deployment updated",
					"deployment", deploymentName,
					"generation", deployment.Generation,
					"observed_generation", deployment.Status.ObservedGeneration,
					"replicas", deployment.Status.Replicas,
					"updated_replicas", deployment.Status.UpdatedReplicas,
					"ready_replicas", deployment.Status.ReadyReplicas,
					"available_replicas", deployment.Status.AvailableReplicas)

				// Check if rollout is complete
				if r.isRolloutComplete(deployment) {
					r.logger.Info("Rollout completed successfully",
						"deployment", deploymentName,
						"replicas", deployment.Status.Replicas,
						"ready_replicas", deployment.Status.ReadyReplicas)
					return nil
				}

				// Check if rollout has failed
				if r.isRolloutFailed(deployment) {
					r.logger.Error("Rollout failed",
						"deployment", deploymentName,
						"conditions", deployment.Status.Conditions)
					return fmt.Errorf("rollout failed for deployment %s", deploymentName)
				}
			}
		}
	}
}

// GetRolloutStatus gets the current rollout status
func (r *RolloutMonitor) GetRolloutStatus(ctx context.Context, deploymentName string) (*RolloutStatus, error) {
	deployment, err := r.client.GetClientset().AppsV1().Deployments(r.client.GetNamespace()).Get(
		ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}

	status := &RolloutStatus{
		Complete: r.isRolloutComplete(deployment),
		Failed:   r.isRolloutFailed(deployment),
		Replicas: RolloutReplicas{
			Desired:   *deployment.Spec.Replicas,
			Updated:   deployment.Status.UpdatedReplicas,
			Ready:     deployment.Status.ReadyReplicas,
			Available: deployment.Status.AvailableReplicas,
		},
	}

	// Set message based on status
	if status.Complete {
		status.Message = "Rollout completed successfully"
	} else if status.Failed {
		status.Message = r.getRolloutFailureMessage(deployment)
	} else {
		status.Message = fmt.Sprintf("Rollout in progress: %d/%d replicas updated, %d/%d ready",
			deployment.Status.UpdatedReplicas, *deployment.Spec.Replicas,
			deployment.Status.ReadyReplicas, *deployment.Spec.Replicas)
	}

	r.logger.Debug("Retrieved rollout status",
		"deployment", deploymentName,
		"complete", status.Complete,
		"failed", status.Failed,
		"message", status.Message,
		"replicas", status.Replicas)

	return status, nil
}

// isRolloutComplete checks if the deployment rollout is complete
func (r *RolloutMonitor) isRolloutComplete(deployment *appsv1.Deployment) bool {
	// Check if the deployment has been observed by the controller
	if deployment.Generation != deployment.Status.ObservedGeneration {
		return false
	}

	// Check if all replicas are updated, ready, and available
	desiredReplicas := *deployment.Spec.Replicas
	if deployment.Status.UpdatedReplicas != desiredReplicas ||
		deployment.Status.ReadyReplicas != desiredReplicas ||
		deployment.Status.AvailableReplicas != desiredReplicas {
		return false
	}

	// Check deployment conditions for "NewReplicaSetAvailable"
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == "True" &&
			condition.Reason == "NewReplicaSetAvailable" {
			return true
		}
	}

	return false
}

// isRolloutFailed checks if the deployment rollout has failed
func (r *RolloutMonitor) isRolloutFailed(deployment *appsv1.Deployment) bool {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == "False" &&
			condition.Reason == "ProgressDeadlineExceeded" {
			return true
		}
		if condition.Type == appsv1.DeploymentReplicaFailure &&
			condition.Status == "True" {
			return true
		}
	}
	return false
}

// getRolloutFailureMessage extracts failure message from deployment conditions
func (r *RolloutMonitor) getRolloutFailureMessage(deployment *appsv1.Deployment) string {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == "False" {
			return condition.Message
		}
		if condition.Type == appsv1.DeploymentReplicaFailure &&
			condition.Status == "True" {
			return condition.Message
		}
	}
	return "Rollout failed for unknown reason"
}
