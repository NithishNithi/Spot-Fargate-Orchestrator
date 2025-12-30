package orchestrator

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"spot-fargate-orchestrator/internal/kubernetes/annotations"
	"spot-fargate-orchestrator/internal/kubernetes/discovery"
	"spot-fargate-orchestrator/internal/logger"
)

const (
	// Annotations for tracking recovery state
	AnnotationMigratedAt     = "spot-orchestrator/migrated-at" // Fixed: removed .io to match annotation manager
	AnnotationLastRecovered  = "spot-orchestrator.io/last-recovered-at"
	AnnotationRecoveryReason = "spot-orchestrator.io/recovery-reason"
	AnnotationFailedAttempts = "spot-orchestrator.io/failed-recovery-attempts"
)

// RecoveryManager handles Fargate → Spot recovery logic
type RecoveryManager struct {
	orchestrator *Orchestrator
	logger       *logger.Logger
	ticker       *time.Ticker
	stopCh       chan struct{}
}

// NewRecoveryManager creates a new recovery manager
func NewRecoveryManager(orchestrator *Orchestrator) *RecoveryManager {
	return &RecoveryManager{
		orchestrator: orchestrator,
		logger:       logger.NewDefault("recovery-manager"),
		stopCh:       make(chan struct{}),
	}
}

// Start begins the recovery monitoring loop
func (r *RecoveryManager) Start(ctx context.Context) {
	// Check if recovery is enabled
	if !r.orchestrator.config.RecoveryEnabled {
		r.logger.Info("Recovery manager is disabled by configuration")
		return
	}

	r.logger.Info("Starting recovery manager",
		"recovery_interval", r.orchestrator.config.RecoveryInterval.String(),
		"recovery_cooldown", r.orchestrator.config.RecoveryCooldown.String())

	r.ticker = time.NewTicker(r.orchestrator.config.RecoveryInterval)
	defer r.ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("Recovery manager stopping due to context cancellation")
			return
		case <-r.stopCh:
			r.logger.Info("Recovery manager stopping")
			return
		case <-r.ticker.C:
			if err := r.attemptRecovery(ctx); err != nil {
				r.logger.Error("Recovery attempt failed", "error", err)
			}
		}
	}
}

// Stop stops the recovery manager
func (r *RecoveryManager) Stop() {
	close(r.stopCh)
}

// attemptRecovery performs the recovery logic
func (r *RecoveryManager) attemptRecovery(ctx context.Context) error {
	// Get all managed deployments
	deployments, err := r.orchestrator.discoveryService.DiscoverManagedDeployments(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover managed deployments: %w", err)
	}

	if len(deployments) == 0 {
		r.logger.Debug("No managed deployments found, skipping recovery")
		return nil
	}

	// Check if spot nodes exist (only once for all deployments)
	if !r.spotNodesExist(ctx) {
		r.logger.Debug("No spot nodes available, skipping recovery for all deployments")
		return nil
	}

	// Attempt recovery for each deployment independently
	for _, deploymentInfo := range deployments {
		if err := r.attemptDeploymentRecovery(ctx, deploymentInfo); err != nil {
			r.logger.Error("Recovery failed for deployment",
				"deployment", deploymentInfo.Name,
				"namespace", deploymentInfo.Namespace,
				"error", err)
			// Continue with other deployments even if one fails
		}
	}

	return nil
}

// attemptDeploymentRecovery attempts recovery for a single deployment
func (r *RecoveryManager) attemptDeploymentRecovery(ctx context.Context, deploymentInfo discovery.DeploymentInfo) error {
	// Step 1: Get deployment to check current state and annotations
	deployment, err := r.orchestrator.k8sClient.GetClientset().AppsV1().Deployments(deploymentInfo.Namespace).Get(
		ctx, deploymentInfo.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", deploymentInfo.Name, err)
	}

	// Step 2: Check if deployment is currently on Fargate
	currentComputeType := r.getCurrentComputeType(deployment)
	if currentComputeType != r.orchestrator.config.FargateLabel {
		r.logger.Debug("Deployment not on Fargate, skipping recovery",
			"deployment", deploymentInfo.Name,
			"current_compute_type", currentComputeType)
		return nil
	}

	// Step 3: Get deployment-specific configuration
	deploymentConfig := annotations.NewDeploymentConfig(r.orchestrator.config, deployment)

	// Step 4: Check if recovery is enabled for this deployment
	if !deploymentConfig.GetRecoveryEnabled() {
		r.logger.Debug("Recovery disabled for deployment",
			"deployment", deploymentInfo.Name)
		return nil
	}

	// Step 5: Check cooldown period (using deployment-specific cooldown)
	if !r.shouldAttemptRecoveryWithConfig(deployment.Annotations, deploymentConfig) {
		r.logger.Debug("Deployment still in cooldown period",
			"deployment", deploymentInfo.Name,
			"cooldown", deploymentConfig.GetRecoveryCooldown())
		return nil
	}

	r.logger.Info("Attempting recovery from Fargate to Spot",
		"deployment", deploymentInfo.Name,
		"namespace", deploymentInfo.Namespace,
		"cooldown", deploymentConfig.GetRecoveryCooldown())

	// Step 6: Attempt migration back to Spot
	return r.performDeploymentRecovery(ctx, deploymentInfo, deploymentConfig)
}

// getCurrentComputeType determines the current compute type from deployment labels
func (r *RecoveryManager) getCurrentComputeType(deployment *appsv1.Deployment) string {
	if deployment.Spec.Template.Labels != nil {
		if computeType, exists := deployment.Spec.Template.Labels[r.orchestrator.config.ComputeLabelKey]; exists {
			return computeType
		}
	}

	// Check nodeSelector for Fargate
	if deployment.Spec.Template.Spec.NodeSelector != nil {
		if computeType, exists := deployment.Spec.Template.Spec.NodeSelector["eks.amazonaws.com/compute-type"]; exists && computeType == "fargate" {
			return r.orchestrator.config.FargateLabel
		}
	}

	// Default to spot if we can't determine
	return r.orchestrator.config.SpotLabel
}

// shouldAttemptRecovery checks if we should attempt recovery based on cooldown and backoff (legacy)
// TODO: Currently unused - legacy function, use shouldAttemptRecoveryWithConfig instead
// func (r *RecoveryManager) shouldAttemptRecovery(annotations map[string]string) bool {
// 	return r.shouldAttemptRecoveryWithConfig(annotations, nil)
// }

// shouldAttemptRecoveryWithConfig checks if we should attempt recovery using deployment-specific config
func (r *RecoveryManager) shouldAttemptRecoveryWithConfig(annotations map[string]string, deploymentConfig *annotations.DeploymentConfig) bool {
	if annotations == nil {
		return true
	}

	// Get the cooldown duration (deployment-specific or global)
	var cooldownDuration time.Duration
	if deploymentConfig != nil {
		cooldownDuration = deploymentConfig.GetRecoveryCooldown()
	} else {
		cooldownDuration = r.orchestrator.config.RecoveryCooldown
	}

	// Check migration cooldown
	if migratedAtStr, exists := annotations[AnnotationMigratedAt]; exists {
		if migratedAt, err := time.Parse(time.RFC3339, migratedAtStr); err == nil {
			if time.Since(migratedAt) < cooldownDuration {
				r.logger.Debug("Still in cooldown period",
					"migrated_at", migratedAt.Format(time.RFC3339),
					"cooldown_duration", cooldownDuration,
					"cooldown_remaining", cooldownDuration-time.Since(migratedAt))
				return false
			}
		}
	}

	// Check recovery backoff based on failed attempts
	if failedAttemptsStr, exists := annotations[AnnotationFailedAttempts]; exists {
		if lastRecoveredStr, exists := annotations[AnnotationLastRecovered]; exists {
			if lastRecovered, err := time.Parse(time.RFC3339, lastRecoveredStr); err == nil {
				failedAttempts := 0
				if attempts, parseErr := time.ParseDuration(failedAttemptsStr + "m"); parseErr == nil {
					failedAttempts = int(attempts.Minutes())
				}

				// Calculate exponential backoff
				backoff := time.Duration(failedAttempts) * r.orchestrator.config.RecoveryBackoffBase
				if backoff > r.orchestrator.config.RecoveryMaxBackoff {
					backoff = r.orchestrator.config.RecoveryMaxBackoff
				}

				if time.Since(lastRecovered) < backoff {
					r.logger.Debug("Still in recovery backoff period",
						"last_recovery_attempt", lastRecovered.Format(time.RFC3339),
						"failed_attempts", failedAttempts,
						"backoff_remaining", backoff-time.Since(lastRecovered))
					return false
				}
			}
		}
	}

	return true
}

// spotNodesExist checks if there are any spot nodes available in the cluster
func (r *RecoveryManager) spotNodesExist(ctx context.Context) bool {
	nodes, err := r.orchestrator.k8sClient.GetClientset().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		r.logger.Warn("Failed to list nodes for spot check", "error", err)
		return false
	}

	spotNodeCount := 0
	for _, node := range nodes.Items {
		// Check for spot node labels
		if isSpotNode(&node) {
			spotNodeCount++
		}
	}

	r.logger.Debug("Spot node availability check",
		"spot_nodes", spotNodeCount,
		"total_nodes", len(nodes.Items))

	return spotNodeCount > 0
}

// isSpotNode checks if a node is a spot instance (same logic as in main.go)
func isSpotNode(node *corev1.Node) bool {
	// Check for common spot node labels
	if capacityType, exists := node.Labels["eks.amazonaws.com/capacityType"]; exists && capacityType == "SPOT" {
		return true
	}
	if capacityType, exists := node.Labels["capacity-type"]; exists && capacityType == "spot" {
		return true
	}
	if capacityType, exists := node.Labels["karpenter.sh/capacity-type"]; exists && capacityType == "spot" {
		return true
	}
	if capacityType, exists := node.Labels["node.kubernetes.io/capacity-type"]; exists && capacityType == "spot" {
		return true
	}

	// Check for spot instance taints
	for _, taint := range node.Spec.Taints {
		if taint.Key == "capacity-type" && taint.Value == "spot" {
			return true
		}
		if taint.Key == "karpenter.sh/capacity-type" && taint.Value == "spot" {
			return true
		}
		if taint.Key == "eks.amazonaws.com/capacityType" && taint.Value == "SPOT" {
			return true
		}
	}

	return false
}

// performDeploymentRecovery executes the actual recovery migration for a specific deployment
func (r *RecoveryManager) performDeploymentRecovery(ctx context.Context, deploymentInfo discovery.DeploymentInfo, deploymentConfig *annotations.DeploymentConfig) error {
	recoveryStart := time.Now()

	r.logger.Info("Starting recovery with deployment-specific configuration",
		"deployment", deploymentInfo.Name,
		"recovery_cooldown", deploymentConfig.GetRecoveryCooldown(),
		"rollout_timeout", deploymentConfig.GetRolloutTimeout(),
		"verification_delay", deploymentConfig.GetVerificationDelay())

	// Send recovery start alert
	r.orchestrator.alertManager.MigrationStarted(ctx,
		deploymentInfo.Name,
		deploymentInfo.Namespace,
		r.orchestrator.config.FargateLabel,
		r.orchestrator.config.SpotLabel)

	// Step 1: Update annotations to track recovery attempt
	if err := r.updateRecoveryAnnotations(ctx, deploymentInfo.Name, deploymentInfo.Namespace, "attempting", ""); err != nil {
		r.logger.Warn("Failed to update recovery annotations", "error", err)
	}

	// Step 2: Patch deployment to use Spot
	if err := r.orchestrator.patcher.PatchComputeType(ctx, deploymentInfo.Name, r.orchestrator.config.SpotLabel); err != nil {
		r.recordFailedRecovery(ctx, deploymentInfo.Name, deploymentInfo.Namespace, fmt.Errorf("patch failed: %w", err))
		return fmt.Errorf("failed to patch deployment for spot recovery: %w", err)
	}

	r.logger.Info("Deployment patched for spot recovery, waiting for rollout")

	// Step 3: Wait for rollout using deployment-specific timeout
	rolloutTimeout := deploymentConfig.GetRolloutTimeout()
	rolloutCtx, rolloutCancel := context.WithTimeout(ctx, rolloutTimeout)
	defer rolloutCancel()

	if err := r.orchestrator.monitor.WaitForRollout(rolloutCtx, deploymentInfo.Name, rolloutTimeout); err != nil {
		r.logger.Warn("Recovery rollout failed or timed out, rolling back to Fargate",
			"timeout", rolloutTimeout,
			"error", err)

		// Rollback to Fargate
		if rollbackErr := r.rollbackToFargate(ctx, deploymentInfo.Name, deploymentInfo.Namespace); rollbackErr != nil {
			r.logger.Error("Failed to rollback to Fargate after recovery failure", "error", rollbackErr)
		}

		r.recordFailedRecovery(ctx, deploymentInfo.Name, deploymentInfo.Namespace, fmt.Errorf("rollout failed: %w", err))
		return fmt.Errorf("recovery rollout failed: %w", err)
	}

	// Step 4: Verify recovery with deployment-specific verification delay
	if err := r.verifyDeploymentRecovery(ctx, deploymentInfo, deploymentConfig); err != nil {
		r.logger.Warn("Recovery health check failed, rolling back to Fargate", "error", err)

		// Rollback to Fargate
		if rollbackErr := r.rollbackToFargate(ctx, deploymentInfo.Name, deploymentInfo.Namespace); rollbackErr != nil {
			r.logger.Error("Failed to rollback to Fargate after health check failure", "error", rollbackErr)
		}

		r.recordFailedRecovery(ctx, deploymentInfo.Name, deploymentInfo.Namespace, fmt.Errorf("health check failed: %w", err))
		return fmt.Errorf("recovery health check failed: %w", err)
	}

	// Step 5: Recovery successful
	recoveryDuration := time.Since(recoveryStart)
	r.logger.Info("Recovery to Spot completed successfully",
		"deployment", deploymentInfo.Name,
		"duration", recoveryDuration.String())

	// Update state and annotations (only for single deployment mode)
	if r.orchestrator.config.IsSingleMode() && deploymentInfo.Name == r.orchestrator.config.DeploymentName {
		r.orchestrator.state.SetCurrentComputeType(r.orchestrator.config.SpotLabel)
		r.orchestrator.state.SetStatus("healthy")
	}

	if err := r.updateRecoveryAnnotations(ctx, deploymentInfo.Name, deploymentInfo.Namespace, "success", ""); err != nil {
		r.logger.Warn("Failed to update success annotations", "error", err)
	}

	// Send success alert with deployment-specific pod count
	pods, err := r.orchestrator.k8sClient.GetClientset().CoreV1().Pods(deploymentInfo.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", deploymentInfo.Name), // Simplified selector
	})
	podsCount := 0
	if err == nil {
		podsCount = len(pods.Items)
	}

	r.orchestrator.alertManager.MigrationCompleted(ctx,
		deploymentInfo.Name,
		deploymentInfo.Namespace,
		recoveryDuration,
		podsCount)

	return nil
}

// rollbackToFargate rolls back to Fargate after a failed recovery attempt
func (r *RecoveryManager) rollbackToFargate(ctx context.Context, deploymentName, namespace string) error {
	r.logger.Info("Rolling back to Fargate after failed recovery",
		"deployment", deploymentName,
		"namespace", namespace)

	if err := r.orchestrator.patcher.PatchComputeType(ctx, deploymentName, r.orchestrator.config.FargateLabel); err != nil {
		return fmt.Errorf("failed to rollback to Fargate: %w", err)
	}

	// Wait for rollback rollout
	if err := r.orchestrator.monitor.WaitForRollout(ctx, deploymentName, r.orchestrator.config.RolloutTimeout); err != nil {
		return fmt.Errorf("rollback rollout failed: %w", err)
	}

	// Only update state if this is the single deployment being managed
	if r.orchestrator.config.IsSingleMode() && deploymentName == r.orchestrator.config.DeploymentName {
		r.orchestrator.state.SetCurrentComputeType(r.orchestrator.config.FargateLabel)
	}

	r.logger.Info("Rollback to Fargate completed successfully",
		"deployment", deploymentName)

	return nil
}

// recordFailedRecovery records a failed recovery attempt and updates backoff
func (r *RecoveryManager) recordFailedRecovery(ctx context.Context, deploymentName, namespace string, err error) {
	r.logger.Warn("Recording failed recovery attempt",
		"deployment", deploymentName,
		"error", err)

	if updateErr := r.updateRecoveryAnnotations(ctx, deploymentName, namespace, "failed", err.Error()); updateErr != nil {
		r.logger.Error("Failed to update failure annotations", "error", updateErr)
	}

	// Send failure alert
	r.orchestrator.alertManager.MigrationFailed(ctx,
		deploymentName,
		namespace,
		err,
		r.orchestrator.config.RecoveryTimeout)
}

// updateRecoveryAnnotations updates deployment annotations to track recovery state
func (r *RecoveryManager) updateRecoveryAnnotations(ctx context.Context, deploymentName, namespace, status, reason string) error {
	deployment, err := r.orchestrator.k8sClient.GetClientset().AppsV1().Deployments(namespace).Get(
		ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment: %w", err)
	}

	if deployment.Annotations == nil {
		deployment.Annotations = make(map[string]string)
	}

	now := time.Now().Format(time.RFC3339)

	switch status {
	case "attempting":
		deployment.Annotations[AnnotationLastRecovered] = now

	case "success":
		deployment.Annotations[AnnotationLastRecovered] = now
		deployment.Annotations[AnnotationRecoveryReason] = "successful-recovery"
		// Reset failed attempts counter
		delete(deployment.Annotations, AnnotationFailedAttempts)
		// Clear migration timestamp since we're back on spot
		delete(deployment.Annotations, AnnotationMigratedAt)

	case "failed":
		deployment.Annotations[AnnotationLastRecovered] = now
		deployment.Annotations[AnnotationRecoveryReason] = reason
		deployment.Annotations[AnnotationMigratedAt] = now // Update migration time for rollback

		// Increment failed attempts counter
		failedAttempts := 1
		if attemptsStr, exists := deployment.Annotations[AnnotationFailedAttempts]; exists {
			if attempts, parseErr := time.ParseDuration(attemptsStr + "m"); parseErr == nil {
				failedAttempts = int(attempts.Minutes()) + 1
			}
		}
		deployment.Annotations[AnnotationFailedAttempts] = fmt.Sprintf("%dm", failedAttempts)
	}

	_, err = r.orchestrator.k8sClient.GetClientset().AppsV1().Deployments(namespace).Update(
		ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update deployment annotations: %w", err)
	}

	r.logger.Debug("Updated recovery annotations",
		"deployment", deploymentName,
		"namespace", namespace,
		"status", status,
		"reason", reason,
		"timestamp", now)

	return nil
}

// verifyDeploymentRecovery verifies recovery for a specific deployment with its configuration
func (r *RecoveryManager) verifyDeploymentRecovery(ctx context.Context, deploymentInfo discovery.DeploymentInfo, deploymentConfig *annotations.DeploymentConfig) error {
	// Add deployment-specific verification delay
	verificationDelay := deploymentConfig.GetVerificationDelay()
	if verificationDelay > 0 {
		r.logger.Debug("Waiting before recovery verification",
			"deployment", deploymentInfo.Name,
			"delay", verificationDelay.String())

		// Use context-aware sleep
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(verificationDelay):
			// Continue with verification
		}
	}

	// For single deployment mode, use existing verification logic
	if r.orchestrator.config.IsSingleMode() && deploymentInfo.Name == r.orchestrator.config.DeploymentName {
		return r.orchestrator.verifyMigration(ctx)
	}

	// For multi-deployment mode, use deployment-specific verification
	return r.orchestrator.verifyDeploymentPodHealth(ctx, deploymentInfo.Name)
}
