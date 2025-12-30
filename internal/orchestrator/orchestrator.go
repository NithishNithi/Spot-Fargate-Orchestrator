package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"spot-fargate-orchestrator/internal/alerts"
	"spot-fargate-orchestrator/internal/config"
	"spot-fargate-orchestrator/internal/health/checker"
	"spot-fargate-orchestrator/internal/kubernetes/annotations"
	"spot-fargate-orchestrator/internal/kubernetes/client"
	"spot-fargate-orchestrator/internal/kubernetes/discovery"
	"spot-fargate-orchestrator/internal/kubernetes/monitor"
	"spot-fargate-orchestrator/internal/kubernetes/patcher"
	"spot-fargate-orchestrator/internal/logger"
	"spot-fargate-orchestrator/internal/orchestrator/state"
	"spot-fargate-orchestrator/internal/spot/watcher"
)

// SpotWatcherInterface defines the interface for spot watchers
type SpotWatcherInterface interface {
	Start(ctx context.Context) error
	EventChannel() <-chan watcher.SpotEvent
}

// Orchestrator coordinates all migration workflows
type Orchestrator struct {
	config            *config.Config
	spotWatcher       SpotWatcherInterface
	patcher           *patcher.DeploymentPatcher
	monitor           *monitor.RolloutMonitor
	healthChecker     *checker.HealthChecker
	k8sClient         *client.K8sClient
	state             *state.State
	annotationManager *annotations.AnnotationManager
	alertManager      *alerts.Manager
	recoveryManager   *RecoveryManager
	discoveryService  *discovery.DiscoveryService
	logger            *logger.Logger
}

// NewOrchestrator creates a new orchestrator
func NewOrchestrator(cfg *config.Config, k8sClient *client.K8sClient) (*Orchestrator, error) {
	// Initialize logger with configured level
	logger := logger.New("orchestrator", logger.LogLevel(cfg.LogLevel))

	logger.Info("Initializing orchestrator components",
		"namespace", cfg.Namespace,
		"deployment", cfg.DeploymentName,
		"service", cfg.ServiceName)

	// Initialize spot watcher - use EventBridge-based monitoring
	var spotWatcher SpotWatcherInterface
	var err error

	if cfg.IsSingleMode() {
		// Single deployment mode
		spotWatcher, err = watcher.NewEventBridgeSpotWatcher(
			k8sClient,
			cfg.DeploymentName,
			cfg.Namespace,
			cfg.AWSRegion,
			cfg.SQSQueueURL,
		)
	} else {
		// Multi-deployment mode (namespace)
		spotWatcher, err = watcher.NewMultiDeploymentEventBridgeSpotWatcher(
			k8sClient,
			cfg.Namespace,
			cfg.AWSRegion,
			cfg.SQSQueueURL,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to initialize EventBridge spot watcher: %w", err)
	}

	// Initialize deployment patcher
	patcher := patcher.NewDeploymentPatcher(k8sClient)

	// Initialize rollout monitor
	monitor := monitor.NewRolloutMonitor(k8sClient)

	// Initialize health checker
	healthChecker := checker.NewHealthChecker(cfg.HealthCheckRetries, cfg.HealthCheckInterval)

	// Initialize state manager
	state := state.NewState()
	state.SetStatus("healthy")

	// Initialize annotation manager
	annotationManager := annotations.NewAnnotationManager(k8sClient)

	// Initialize alert manager
	alertManager := alerts.NewManager(alerts.Config{
		SlackWebhookURL: cfg.SlackWebhookURL,
		Enabled:         cfg.AlertsEnabled,
	})

	if alertManager.IsEnabled() {
		logger.Info("Slack alerting enabled", "webhook_configured", cfg.SlackWebhookURL != "")
	} else {
		logger.Info("Slack alerting disabled")
	}

	// Initialize discovery service
	discoveryService := discovery.NewDiscoveryService(k8sClient, cfg)

	orchestrator := &Orchestrator{
		config:            cfg,
		spotWatcher:       spotWatcher,
		patcher:           patcher,
		monitor:           monitor,
		healthChecker:     healthChecker,
		k8sClient:         k8sClient,
		state:             state,
		annotationManager: annotationManager,
		alertManager:      alertManager,
		discoveryService:  discoveryService,
		logger:            logger,
	}

	// Initialize recovery manager (needs orchestrator reference)
	recoveryManager := NewRecoveryManager(orchestrator)
	orchestrator.recoveryManager = recoveryManager

	logger.Info("All orchestrator components initialized successfully")

	return orchestrator, nil
}

// Start begins the orchestration process
func (o *Orchestrator) Start(ctx context.Context) error {
	o.logger.Info("Starting orchestrator",
		"deployment", o.config.DeploymentName,
		"namespace", o.config.Namespace,
		"check_interval", o.config.CheckInterval.String())

	// Start spot watcher in a separate goroutine
	spotCtx, spotCancel := context.WithCancel(ctx)
	defer spotCancel()

	go func() {
		if err := o.spotWatcher.Start(spotCtx); err != nil && err != context.Canceled {
			o.logger.Error("Spot watcher failed", "error", err)
		}
	}()

	// Start recovery manager in a separate goroutine
	recoveryCtx, recoveryCancel := context.WithCancel(ctx)
	defer recoveryCancel()

	go func() {
		o.recoveryManager.Start(recoveryCtx)
	}()

	// Main event loop
	o.logger.Info("Orchestrator started, monitoring for spot interruption events")

	for {
		select {
		case <-ctx.Done():
			o.logger.Info("Orchestrator stopping due to context cancellation")
			// Stop recovery manager
			o.recoveryManager.Stop()
			return ctx.Err()

		case event, ok := <-o.spotWatcher.EventChannel():
			if !ok {
				o.logger.Warn("Spot watcher event channel closed, restarting watcher")
				// Restart the spot watcher
				go func() {
					if err := o.spotWatcher.Start(spotCtx); err != nil && err != context.Canceled {
						o.logger.Error("Failed to restart spot watcher", "error", err)
					}
				}()
				continue
			}

			// Handle the spot interruption event
			if err := o.handleSpotInterruption(ctx, event); err != nil {
				o.logger.Error("Failed to handle spot interruption", "error", err)
				o.state.SetLastError(err.Error())
				o.state.SetStatus("error")
			}
		}
	}
}

// handleSpotInterruption handles spot interruption events
func (o *Orchestrator) handleSpotInterruption(ctx context.Context, event watcher.SpotEvent) error {
	o.logger.LogSpotInterruption(event.InstanceID, event.TimeRemaining.String())

	// Send spot interruption alert
	o.alertManager.SpotInterruptionDetected(ctx, event.InstanceID, event.NodeName, event.AffectedPods, event.TimeRemaining)

	// In multi-deployment mode, we need to handle multiple affected deployments
	if o.config.IsMultiDeploymentMode() {
		return o.handleMultiDeploymentSpotInterruption(ctx, event)
	}

	// Single deployment mode (existing logic)
	return o.handleSingleDeploymentSpotInterruption(ctx, event)
}

// handleSingleDeploymentSpotInterruption handles spot interruption for single deployment mode
func (o *Orchestrator) handleSingleDeploymentSpotInterruption(ctx context.Context, event watcher.SpotEvent) error {

	// Set state to migrating
	o.state.SetStatus("migrating")
	currentComputeType := o.state.GetCurrentComputeType()
	targetComputeType := o.config.FargateLabel

	// If current compute type is unknown, try to detect it from annotations first, then deployment spec
	if currentComputeType == "" {
		deploymentState, err := o.annotationManager.GetDeploymentState(ctx, o.config.DeploymentName, o.config.Namespace)
		if err != nil {
			o.logger.Warn("Failed to get deployment state from annotations", "error", err)
		} else if deploymentState.State != "" {
			currentComputeType = deploymentState.State
			o.logger.Info("Detected current compute type from annotations",
				"compute_type", currentComputeType,
				"last_migrated", deploymentState.MigratedAt.Format(time.RFC3339),
				"last_reason", deploymentState.Reason)
		}

		// Fallback to deployment spec detection if annotations don't have state
		if currentComputeType == "" {
			detectedType, err := o.detectCurrentComputeType(ctx)
			if err != nil {
				o.logger.Warn("Failed to detect current compute type, assuming spot", "error", err)
				currentComputeType = o.config.SpotLabel
			} else {
				currentComputeType = detectedType
			}
		}
		o.state.SetCurrentComputeType(currentComputeType)
	}

	o.logger.LogMigrationStart(currentComputeType, targetComputeType)
	migrationStart := time.Now()

	// Send migration start alert
	o.alertManager.MigrationStarted(ctx, o.config.DeploymentName, o.config.Namespace, currentComputeType, targetComputeType)

	// Perform the migration to Fargate
	if err := o.migrateToFargateWithReason(ctx, annotations.ReasonSpotInterruption); err != nil {
		o.logger.LogError("migration", err,
			"current_compute_type", currentComputeType,
			"target_compute_type", targetComputeType,
			"time_remaining", event.TimeRemaining.String())

		// Send migration failure alert
		migrationDuration := time.Since(migrationStart)
		o.alertManager.MigrationFailed(ctx, o.config.DeploymentName, o.config.Namespace, err, migrationDuration)

		// Don't fail completely - keep monitoring for future events
		o.state.SetStatus("error")
		o.state.SetLastError(err.Error())
		return nil
	}

	// Update state on successful migration
	o.state.RecordMigration()
	o.state.SetCurrentComputeType(targetComputeType)
	o.state.SetStatus("healthy")
	o.state.SetLastError("")

	migrationDuration := time.Since(migrationStart)
	o.logger.LogMigrationComplete(migrationDuration.String(), targetComputeType)

	// Send migration success alert
	// Get pod count for the alert
	labelSelector, err := o.getDeploymentLabelSelector(ctx)
	if err != nil {
		o.logger.Warn("Failed to get deployment label selector for alert", "error", err)
		labelSelector = fmt.Sprintf("app=%s", o.config.DeploymentName) // fallback
	}

	pods, err := o.k8sClient.GetClientset().CoreV1().Pods(o.config.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	podsCount := 0
	if err == nil {
		podsCount = len(pods.Items)
	}

	o.alertManager.MigrationCompleted(ctx, o.config.DeploymentName, o.config.Namespace, migrationDuration, podsCount)

	return nil
}

// detectCurrentComputeType detects the current compute type from the deployment
func (o *Orchestrator) detectCurrentComputeType(ctx context.Context) (string, error) {
	deployment, err := o.k8sClient.GetClientset().AppsV1().Deployments(o.config.Namespace).Get(
		ctx, o.config.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get deployment: %w", err)
	}

	if deployment.Spec.Template.Labels != nil {
		if computeType, exists := deployment.Spec.Template.Labels[o.config.ComputeLabelKey]; exists {
			return computeType, nil
		}
	}

	// Check nodeSelector for Fargate
	if deployment.Spec.Template.Spec.NodeSelector != nil {
		if computeType, exists := deployment.Spec.Template.Spec.NodeSelector["eks.amazonaws.com/compute-type"]; exists && computeType == "fargate" {
			return o.config.FargateLabel, nil
		}
	}

	// Check affinity for spot
	if deployment.Spec.Template.Spec.Affinity != nil &&
		deployment.Spec.Template.Spec.Affinity.NodeAffinity != nil {
		return o.config.SpotLabel, nil
	}

	// Default to spot if we can't determine
	return o.config.SpotLabel, nil
}

// migrateToFargate performs the complete migration workflow to Fargate
// TODO: Currently unused - may be used for manual migration feature
// func (o *Orchestrator) migrateToFargate(ctx context.Context) error {
// 	return o.migrateToFargateWithReason(ctx, annotations.ReasonManual)
// }

// migrateToFargateWithReason performs the complete migration workflow to Fargate with a specific reason
func (o *Orchestrator) migrateToFargateWithReason(ctx context.Context, reason string) error {
	o.logger.Info("Starting migration to Fargate",
		"deployment", o.config.DeploymentName,
		"namespace", o.config.Namespace)

	// Step 1: Ensure zero-downtime rollout strategy
	if err := o.ensureZeroDowntimeStrategy(ctx); err != nil {
		return fmt.Errorf("failed to configure zero-downtime strategy: %w", err)
	}

	// Step 2: Patch the deployment to use Fargate
	if err := o.patcher.PatchComputeTypeWithReason(ctx, o.config.DeploymentName, o.config.FargateLabel, reason); err != nil {
		// Attempt rollback on patch failure
		if rollbackErr := o.rollbackZeroDowntimeStrategy(ctx); rollbackErr != nil {
			o.logger.Error("Failed to rollback zero-downtime strategy after patch failure", "error", rollbackErr)
		}
		return fmt.Errorf("failed to patch deployment for Fargate: %w", err)
	}

	o.logger.Info("Deployment patched successfully, waiting for rollout")

	// Step 3: Wait for rollout to complete
	if err := o.monitor.WaitForRollout(ctx, o.config.DeploymentName, o.config.RolloutTimeout); err != nil {
		// Attempt rollback on rollout failure
		if rollbackErr := o.rollbackMigration(ctx); rollbackErr != nil {
			o.logger.Error("Failed to rollback migration after rollout failure", "error", rollbackErr)
		}
		return fmt.Errorf("rollout failed or timed out: %w", err)
	}

	o.logger.Info("Rollout completed successfully, verifying migration")

	// Step 4: Verify migration with comprehensive health checks
	if err := o.verifyMigration(ctx); err != nil {
		// Attempt rollback on verification failure
		if rollbackErr := o.rollbackMigration(ctx); rollbackErr != nil {
			o.logger.Error("Failed to rollback migration after verification failure", "error", rollbackErr)
		}
		return fmt.Errorf("migration verification failed: %w", err)
	}

	o.logger.Info("Migration to Fargate completed successfully")
	return nil
}

// verifyMigration verifies that the migration was successful
func (o *Orchestrator) verifyMigration(ctx context.Context) error {
	// Add a small delay before verification to allow pods to stabilize
	if o.config.VerificationDelay > 0 {
		o.logger.Debug("Waiting before verification",
			"delay", o.config.VerificationDelay.String())

		// Use context-aware sleep
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(o.config.VerificationDelay):
			// Continue with verification
		}
	}

	// Step 1: Verify all pods are healthy
	if err := o.verifyPodHealth(ctx); err != nil {
		return fmt.Errorf("pod health verification failed: %w", err)
	}

	// Step 2: Verify service endpoints route to healthy pods
	if err := o.verifyServiceEndpoints(ctx); err != nil {
		return fmt.Errorf("service endpoint verification failed: %w", err)
	}

	// Step 3: Verify service exists and is properly configured
	_, err := o.k8sClient.GetClientset().CoreV1().Services(o.config.Namespace).Get(
		ctx, o.config.ServiceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("service validation failed: %w", err)
	}

	o.logger.Info("Migration verification successful - all pods are ready and service endpoints are healthy",
		"service", o.config.ServiceName)

	return nil
}

// ensureZeroDowntimeStrategy configures the deployment for zero-downtime rollout
func (o *Orchestrator) ensureZeroDowntimeStrategy(ctx context.Context) error {
	o.logger.Info("Configuring zero-downtime rollout strategy",
		"deployment", o.config.DeploymentName)

	// Get current deployment
	deployment, err := o.k8sClient.GetClientset().AppsV1().Deployments(o.config.Namespace).Get(
		ctx, o.config.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment: %w", err)
	}

	// Check if maxUnavailable is already set to 0
	if deployment.Spec.Strategy.RollingUpdate != nil &&
		deployment.Spec.Strategy.RollingUpdate.MaxUnavailable != nil {
		maxUnavailable := deployment.Spec.Strategy.RollingUpdate.MaxUnavailable.IntValue()
		if maxUnavailable == 0 {
			o.logger.Debug("Zero-downtime strategy already configured")
			return nil
		}
	}

	// Create patch to set maxUnavailable to 0
	patchOps := []map[string]interface{}{
		{
			"op":    "replace",
			"path":  "/spec/strategy/type",
			"value": "RollingUpdate",
		},
		{
			"op":    "replace",
			"path":  "/spec/strategy/rollingUpdate/maxUnavailable",
			"value": 0,
		},
	}

	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		return fmt.Errorf("failed to marshal zero-downtime patch: %w", err)
	}

	// Apply the patch
	_, err = o.k8sClient.GetClientset().AppsV1().Deployments(o.config.Namespace).Patch(
		ctx, o.config.DeploymentName, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to apply zero-downtime strategy patch: %w", err)
	}

	o.logger.Info("Zero-downtime rollout strategy configured successfully")
	return nil
}

// rollbackZeroDowntimeStrategy restores the original rollout strategy
func (o *Orchestrator) rollbackZeroDowntimeStrategy(ctx context.Context) error {
	o.logger.Info("Rolling back zero-downtime strategy",
		"deployment", o.config.DeploymentName)

	// Reset to default rolling update strategy
	patchOps := []map[string]interface{}{
		{
			"op":    "replace",
			"path":  "/spec/strategy/rollingUpdate/maxUnavailable",
			"value": "25%",
		},
	}

	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		return fmt.Errorf("failed to marshal rollback patch: %w", err)
	}

	_, err = o.k8sClient.GetClientset().AppsV1().Deployments(o.config.Namespace).Patch(
		ctx, o.config.DeploymentName, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to rollback zero-downtime strategy: %w", err)
	}

	o.logger.Info("Zero-downtime strategy rollback completed")
	return nil
}

// rollbackMigration attempts to rollback the migration to the previous compute type
func (o *Orchestrator) rollbackMigration(ctx context.Context) error {
	currentComputeType := o.state.GetCurrentComputeType()
	var rollbackComputeType string

	// Determine rollback target
	if currentComputeType == o.config.FargateLabel {
		rollbackComputeType = o.config.SpotLabel
	} else {
		rollbackComputeType = o.config.FargateLabel
	}

	o.logger.Warn("Attempting migration rollback",
		"current_compute_type", currentComputeType,
		"rollback_to", rollbackComputeType)

	// Patch back to previous compute type
	if err := o.patcher.PatchComputeTypeWithReason(ctx, o.config.DeploymentName, rollbackComputeType, annotations.ReasonRollback); err != nil {
		return fmt.Errorf("failed to patch deployment for rollback: %w", err)
	}

	// Wait for rollback rollout
	if err := o.monitor.WaitForRollout(ctx, o.config.DeploymentName, o.config.RolloutTimeout); err != nil {
		return fmt.Errorf("rollback rollout failed: %w", err)
	}

	// Restore original rollout strategy
	if err := o.rollbackZeroDowntimeStrategy(ctx); err != nil {
		o.logger.Warn("Failed to restore original rollout strategy", "error", err)
	}

	o.logger.Info("Migration rollback completed successfully")
	return nil
}

// getDeploymentLabelSelector returns the correct label selector for the deployment
func (o *Orchestrator) getDeploymentLabelSelector(ctx context.Context) (string, error) {
	deployment, err := o.k8sClient.GetClientset().AppsV1().Deployments(o.config.Namespace).Get(
		ctx, o.config.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get deployment: %w", err)
	}

	if deployment.Spec.Selector != nil && deployment.Spec.Selector.MatchLabels != nil {
		var selectorParts []string
		for key, value := range deployment.Spec.Selector.MatchLabels {
			selectorParts = append(selectorParts, fmt.Sprintf("%s=%s", key, value))
		}
		return strings.Join(selectorParts, ","), nil
	}

	// Fallback to app=deployment-name if no selector found
	return fmt.Sprintf("app=%s", o.config.DeploymentName), nil
}

// verifyPodHealth checks the health of all pods in the deployment using Kubernetes pod status
func (o *Orchestrator) verifyPodHealth(ctx context.Context) error {
	o.logger.Debug("Verifying pod health for deployment using Kubernetes pod status",
		"deployment", o.config.DeploymentName)

	// Get the correct label selector for the deployment
	labelSelector, err := o.getDeploymentLabelSelector(ctx)
	if err != nil {
		return fmt.Errorf("failed to get deployment label selector: %w", err)
	}

	// Get pods for the deployment
	pods, err := o.k8sClient.GetClientset().CoreV1().Pods(o.config.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found for deployment %s", o.config.DeploymentName)
	}

	healthyPods := 0
	for _, pod := range pods.Items {
		// Skip pods that are not running
		if pod.Status.Phase != corev1.PodRunning {
			o.logger.Debug("Skipping non-running pod",
				"pod", pod.Name,
				"phase", pod.Status.Phase)
			continue
		}

		// Check if pod is ready using Kubernetes readiness status
		isPodReady := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				isPodReady = true
				break
			}
		}

		if isPodReady {
			healthyPods++
			o.logger.Debug("Pod is healthy (Ready condition is True)",
				"pod", pod.Name,
				"pod_ip", pod.Status.PodIP,
				"node", pod.Spec.NodeName)
		} else {
			o.logger.Warn("Pod is not ready",
				"pod", pod.Name,
				"pod_ip", pod.Status.PodIP,
				"conditions", pod.Status.Conditions)
		}
	}

	if healthyPods == 0 {
		return fmt.Errorf("no healthy pods found for deployment %s", o.config.DeploymentName)
	}

	o.logger.Info("Pod health verification completed using Kubernetes pod status",
		"deployment", o.config.DeploymentName,
		"healthy_pods", healthyPods,
		"total_pods", len(pods.Items))

	return nil
}

// verifyServiceEndpoints ensures service endpoints route to healthy pods
func (o *Orchestrator) verifyServiceEndpoints(ctx context.Context) error {
	o.logger.Debug("Verifying service endpoints using Kubernetes endpoint status",
		"service", o.config.ServiceName)

	// Get service endpoints
	endpoints, err := o.k8sClient.GetClientset().CoreV1().Endpoints(o.config.Namespace).Get(
		ctx, o.config.ServiceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get service endpoints: %w", err)
	}

	if len(endpoints.Subsets) == 0 {
		return fmt.Errorf("service %s has no endpoint subsets", o.config.ServiceName)
	}

	totalEndpoints := 0
	readyEndpoints := 0

	for _, subset := range endpoints.Subsets {
		totalEndpoints += len(subset.Addresses) + len(subset.NotReadyAddresses)
		readyEndpoints += len(subset.Addresses)

		// Log ready endpoints (Kubernetes already validated these)
		for _, addr := range subset.Addresses {
			o.logger.Debug("Service endpoint is ready",
				"endpoint_ip", addr.IP,
				"target_ref", addr.TargetRef)
		}

		// Log not ready endpoints
		for _, addr := range subset.NotReadyAddresses {
			o.logger.Debug("Service endpoint is not ready",
				"endpoint_ip", addr.IP,
				"target_ref", addr.TargetRef)
		}
	}

	if readyEndpoints == 0 {
		return fmt.Errorf("service %s has no ready endpoints", o.config.ServiceName)
	}

	o.logger.Info("Service endpoint verification completed using Kubernetes endpoint status",
		"service", o.config.ServiceName,
		"ready_endpoints", readyEndpoints,
		"total_endpoints", totalEndpoints)

	return nil
}

// GetDeploymentState returns the current deployment state from annotations
func (o *Orchestrator) GetDeploymentState(ctx context.Context) (*annotations.DeploymentState, error) {
	return o.annotationManager.GetDeploymentState(ctx, o.config.DeploymentName, o.config.Namespace)
}

// SetDeploymentState manually sets the deployment state (for manual migrations)
func (o *Orchestrator) SetDeploymentState(ctx context.Context, state, reason string) error {
	if !annotations.IsValidState(state) {
		return fmt.Errorf("invalid state: %s (must be %s or %s)", state, annotations.StateSpot, annotations.StateFargate)
	}

	if !annotations.IsValidReason(reason) {
		return fmt.Errorf("invalid reason: %s (must be %s, %s, or %s)",
			reason, annotations.ReasonSpotInterruption, annotations.ReasonManual, annotations.ReasonRollback)
	}

	return o.annotationManager.SetDeploymentState(ctx, o.config.DeploymentName, o.config.Namespace, state, reason)
}

// DeploymentConfigInfo holds deployment info with its configuration
type DeploymentConfigInfo struct {
	Name      string
	Namespace string
	PodNames  []string
	Config    *annotations.DeploymentConfig
}

// groupPodsByDeployment groups affected pods by their parent deployment
func (o *Orchestrator) groupPodsByDeployment(ctx context.Context, affectedPods []string) (map[string][]string, error) {
	deploymentPods := make(map[string][]string)

	for _, podName := range affectedPods {
		// Get the pod to find its deployment
		pod, err := o.k8sClient.GetClientset().CoreV1().Pods(o.config.Namespace).Get(
			ctx, podName, metav1.GetOptions{})
		if err != nil {
			o.logger.Warn("Failed to get pod for deployment mapping", "pod", podName, "error", err)
			continue
		}

		// Find the deployment name from owner references
		deploymentName := o.getDeploymentNameFromPod(pod)
		if deploymentName == "" {
			o.logger.Debug("Pod does not belong to a deployment", "pod", podName)
			continue
		}

		// Add pod to deployment group
		deploymentPods[deploymentName] = append(deploymentPods[deploymentName], podName)
	}

	return deploymentPods, nil
}

// getDeploymentNameFromPod extracts deployment name from pod owner references
func (o *Orchestrator) getDeploymentNameFromPod(pod *corev1.Pod) string {
	// Walk up the ownership chain: Pod → ReplicaSet → Deployment
	for _, ownerRef := range pod.OwnerReferences {
		if ownerRef.Kind == "ReplicaSet" {
			// Get the ReplicaSet to find its owner (Deployment)
			rs, err := o.k8sClient.GetClientset().AppsV1().ReplicaSets(pod.Namespace).Get(
				context.Background(), ownerRef.Name, metav1.GetOptions{})
			if err != nil {
				o.logger.Debug("Failed to get ReplicaSet", "replicaset", ownerRef.Name, "error", err)
				continue
			}

			// Check ReplicaSet's owner references for Deployment
			for _, rsOwnerRef := range rs.OwnerReferences {
				if rsOwnerRef.Kind == "Deployment" {
					return rsOwnerRef.Name
				}
			}
		}
	}

	return ""
}

// getDeploymentConfigs gets deployment configurations for affected deployments
func (o *Orchestrator) getDeploymentConfigs(ctx context.Context, deploymentPods map[string][]string) ([]DeploymentConfigInfo, error) {
	var deploymentConfigs []DeploymentConfigInfo

	// Get configuration for each deployment
	for deploymentName, podNames := range deploymentPods {
		deployment, err := o.k8sClient.GetClientset().AppsV1().Deployments(o.config.Namespace).Get(
			ctx, deploymentName, metav1.GetOptions{})
		if err != nil {
			o.logger.Warn("Failed to get deployment for configuration", "deployment", deploymentName, "error", err)
			continue
		}

		// Create deployment-specific configuration
		deploymentConfig := annotations.NewDeploymentConfig(o.config, deployment)

		// Skip if Fargate migration is not allowed
		if deploymentConfig.IsSpotOnly() {
			o.logger.Info("Deployment is spot-only, skipping Fargate migration",
				"deployment", deploymentName,
				"allow_fargate", deploymentConfig.GetAllowFargate())
			continue
		}

		deploymentConfigs = append(deploymentConfigs, DeploymentConfigInfo{
			Name:      deploymentName,
			Namespace: o.config.Namespace,
			PodNames:  podNames,
			Config:    deploymentConfig,
		})
	}

	return deploymentConfigs, nil
}

// migrateDeploymentToFargate migrates a specific deployment to Fargate during spot interruption
// TODO: Currently unused - may be used for single deployment migration feature
/*
func (o *Orchestrator) migrateDeploymentToFargate(ctx context.Context, deploymentInfo DeploymentConfigInfo, event watcher.SpotEvent) error {
	o.logger.Info("Migrating deployment to Fargate due to spot interruption",
		"deployment", deploymentInfo.Name,
		"namespace", deploymentInfo.Namespace,
		"affected_pods", len(deploymentInfo.PodNames),
		"instance_id", event.InstanceID)

	migrationStart := time.Now()

	// Send migration start alert
	o.alertManager.MigrationStarted(ctx,
		deploymentInfo.Name,
		deploymentInfo.Namespace,
		o.config.SpotLabel,
		o.config.FargateLabel)

	// Step 1: Ensure zero-downtime strategy for this deployment
	if err := o.ensureZeroDowntimeStrategyForDeployment(ctx, deploymentInfo.Name); err != nil {
		o.logger.Error("Failed to configure zero-downtime strategy",
			"deployment", deploymentInfo.Name,
			"error", err)
		return fmt.Errorf("failed to configure zero-downtime strategy: %w", err)
	}

	// Step 2: Patch the deployment to use Fargate
	if err := o.patcher.PatchComputeTypeWithReason(ctx, deploymentInfo.Name, o.config.FargateLabel, annotations.ReasonSpotInterruption); err != nil {
		// Attempt rollback on patch failure
		if rollbackErr := o.rollbackZeroDowntimeStrategyForDeployment(ctx, deploymentInfo.Name); rollbackErr != nil {
			o.logger.Error("Failed to rollback zero-downtime strategy after patch failure",
				"deployment", deploymentInfo.Name,
				"error", rollbackErr)
		}
		return fmt.Errorf("failed to patch deployment for Fargate: %w", err)
	}

	o.logger.Info("Deployment patched successfully, waiting for rollout",
		"deployment", deploymentInfo.Name)

	// Step 3: Wait for rollout to complete (using deployment-specific timeout)
	rolloutTimeout := deploymentInfo.Config.GetRolloutTimeout()
	if err := o.monitor.WaitForRollout(ctx, deploymentInfo.Name, rolloutTimeout); err != nil {
		// Attempt rollback on rollout failure
		if rollbackErr := o.rollbackDeploymentMigration(ctx, deploymentInfo.Name); rollbackErr != nil {
			o.logger.Error("Failed to rollback migration after rollout failure",
				"deployment", deploymentInfo.Name,
				"error", rollbackErr)
		}
		return fmt.Errorf("rollout failed or timed out: %w", err)
	}

	o.logger.Info("Rollout completed successfully, verifying migration",
		"deployment", deploymentInfo.Name)

	// Step 4: Verify migration with deployment-specific verification delay
	if err := o.verifyDeploymentMigration(ctx, deploymentInfo); err != nil {
		// Attempt rollback on verification failure
		if rollbackErr := o.rollbackDeploymentMigration(ctx, deploymentInfo.Name); rollbackErr != nil {
			o.logger.Error("Failed to rollback migration after verification failure",
				"deployment", deploymentInfo.Name,
				"error", rollbackErr)
		}
		return fmt.Errorf("migration verification failed: %w", err)
	}

	migrationDuration := time.Since(migrationStart)
	o.logger.Info("Migration to Fargate completed successfully",
		"deployment", deploymentInfo.Name,
		"duration", migrationDuration.String())

	// Send migration success alert
	pods, err := o.k8sClient.GetClientset().CoreV1().Pods(deploymentInfo.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", deploymentInfo.Name), // Simplified selector
	})
	podsCount := 0
	if err == nil {
		podsCount = len(pods.Items)
	}

	o.alertManager.MigrationCompleted(ctx,
		deploymentInfo.Name,
		deploymentInfo.Namespace,
		migrationDuration,
		podsCount)

	return nil
}
*/

// handleMultiDeploymentSpotInterruption handles spot interruption for multiple deployments
func (o *Orchestrator) handleMultiDeploymentSpotInterruption(ctx context.Context, event watcher.SpotEvent) error {
	o.logger.Info("Handling multi-deployment spot interruption",
		"instance_id", event.InstanceID,
		"node_name", event.NodeName,
		"affected_pods", len(event.AffectedPods))

	if len(event.AffectedPods) == 0 {
		o.logger.Info("No affected pods found for spot interruption")
		return nil
	}

	// Step 1: Group affected pods by deployment
	deploymentPods, err := o.groupPodsByDeployment(ctx, event.AffectedPods)
	if err != nil {
		return fmt.Errorf("failed to group pods by deployment: %w", err)
	}

	if len(deploymentPods) == 0 {
		o.logger.Info("No opted-in deployments affected by spot interruption")
		return nil
	}

	o.logger.Info("Found affected deployments",
		"deployment_count", len(deploymentPods),
		"instance_id", event.InstanceID)

	// Step 2: Get deployment configurations
	deploymentConfigs, err := o.getDeploymentConfigs(ctx, deploymentPods)
	if err != nil {
		return fmt.Errorf("failed to get deployment configurations: %w", err)
	}

	// TWO-PHASE MIGRATION APPROACH
	// Phase 1: FAN-OUT (fast, non-blocking) - Patch all deployments immediately
	// Phase 2: OBSERVE (parallel) - Monitor rollouts independently

	return o.executeTwoPhaseMigration(ctx, deploymentConfigs, event)
}

// ensureZeroDowntimeStrategyForDeployment configures zero-downtime rollout for a specific deployment
func (o *Orchestrator) ensureZeroDowntimeStrategyForDeployment(ctx context.Context, deploymentName string) error {
	o.logger.Info("Configuring zero-downtime rollout strategy for deployment",
		"deployment", deploymentName)

	// Get current deployment
	deployment, err := o.k8sClient.GetClientset().AppsV1().Deployments(o.config.Namespace).Get(
		ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment: %w", err)
	}

	// Check if maxUnavailable is already set to 0
	if deployment.Spec.Strategy.RollingUpdate != nil &&
		deployment.Spec.Strategy.RollingUpdate.MaxUnavailable != nil {
		maxUnavailable := deployment.Spec.Strategy.RollingUpdate.MaxUnavailable.IntValue()
		if maxUnavailable == 0 {
			o.logger.Debug("Zero-downtime strategy already configured for deployment",
				"deployment", deploymentName)
			return nil
		}
	}

	// Create patch to set maxUnavailable to 0
	patchOps := []map[string]interface{}{
		{
			"op":    "replace",
			"path":  "/spec/strategy/type",
			"value": "RollingUpdate",
		},
		{
			"op":    "replace",
			"path":  "/spec/strategy/rollingUpdate/maxUnavailable",
			"value": 0,
		},
	}

	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		return fmt.Errorf("failed to marshal zero-downtime patch: %w", err)
	}

	// Apply the patch
	_, err = o.k8sClient.GetClientset().AppsV1().Deployments(o.config.Namespace).Patch(
		ctx, deploymentName, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to apply zero-downtime strategy patch: %w", err)
	}

	o.logger.Info("Zero-downtime rollout strategy configured successfully for deployment",
		"deployment", deploymentName)
	return nil
}

// rollbackZeroDowntimeStrategyForDeployment restores original rollout strategy for a specific deployment
func (o *Orchestrator) rollbackZeroDowntimeStrategyForDeployment(ctx context.Context, deploymentName string) error {
	o.logger.Info("Rolling back zero-downtime strategy for deployment",
		"deployment", deploymentName)

	// Reset to default rolling update strategy
	patchOps := []map[string]interface{}{
		{
			"op":    "replace",
			"path":  "/spec/strategy/rollingUpdate/maxUnavailable",
			"value": "25%",
		},
	}

	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		return fmt.Errorf("failed to marshal rollback patch: %w", err)
	}

	_, err = o.k8sClient.GetClientset().AppsV1().Deployments(o.config.Namespace).Patch(
		ctx, deploymentName, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to rollback zero-downtime strategy: %w", err)
	}

	o.logger.Info("Zero-downtime strategy rollback completed for deployment",
		"deployment", deploymentName)
	return nil
}

// rollbackDeploymentMigration attempts to rollback migration for a specific deployment
func (o *Orchestrator) rollbackDeploymentMigration(ctx context.Context, deploymentName string) error {
	o.logger.Warn("Attempting migration rollback for deployment",
		"deployment", deploymentName,
		"rollback_to", o.config.SpotLabel)

	// Patch back to spot
	if err := o.patcher.PatchComputeTypeWithReason(ctx, deploymentName, o.config.SpotLabel, annotations.ReasonRollback); err != nil {
		return fmt.Errorf("failed to patch deployment for rollback: %w", err)
	}

	// Wait for rollback rollout
	if err := o.monitor.WaitForRollout(ctx, deploymentName, o.config.RolloutTimeout); err != nil {
		return fmt.Errorf("rollback rollout failed: %w", err)
	}

	// Restore original rollout strategy
	if err := o.rollbackZeroDowntimeStrategyForDeployment(ctx, deploymentName); err != nil {
		o.logger.Warn("Failed to restore original rollout strategy for deployment",
			"deployment", deploymentName,
			"error", err)
	}

	o.logger.Info("Migration rollback completed successfully for deployment",
		"deployment", deploymentName)
	return nil
}

// verifyDeploymentMigration verifies migration for a specific deployment with its config
func (o *Orchestrator) verifyDeploymentMigration(ctx context.Context, deploymentInfo DeploymentConfigInfo) error {
	// Add deployment-specific verification delay
	verificationDelay := deploymentInfo.Config.GetVerificationDelay()
	if verificationDelay > 0 {
		o.logger.Debug("Waiting before verification for deployment",
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

	// Step 1: Verify all pods are healthy for this deployment
	if err := o.verifyDeploymentPodHealth(ctx, deploymentInfo.Name); err != nil {
		return fmt.Errorf("pod health verification failed for deployment %s: %w", deploymentInfo.Name, err)
	}

	// Step 2: Verify service endpoints if service exists
	// Try to find service for this deployment (assuming service name matches deployment name)
	_, err := o.k8sClient.GetClientset().CoreV1().Services(o.config.Namespace).Get(
		ctx, deploymentInfo.Name, metav1.GetOptions{})
	if err == nil {
		// Service exists, verify its endpoints
		if err := o.verifyDeploymentServiceEndpoints(ctx, deploymentInfo.Name); err != nil {
			return fmt.Errorf("service endpoint verification failed for deployment %s: %w", deploymentInfo.Name, err)
		}
	} else {
		o.logger.Debug("No service found for deployment, skipping service verification",
			"deployment", deploymentInfo.Name)
	}

	o.logger.Info("Migration verification successful for deployment",
		"deployment", deploymentInfo.Name)

	return nil
}

// verifyDeploymentPodHealth checks health of pods for a specific deployment
func (o *Orchestrator) verifyDeploymentPodHealth(ctx context.Context, deploymentName string) error {
	o.logger.Debug("Verifying pod health for deployment",
		"deployment", deploymentName)

	// Get deployment to find correct label selector
	deployment, err := o.k8sClient.GetClientset().AppsV1().Deployments(o.config.Namespace).Get(
		ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment: %w", err)
	}

	// Build label selector from deployment's selector
	var labelSelector string
	if deployment.Spec.Selector != nil && deployment.Spec.Selector.MatchLabels != nil {
		var selectorParts []string
		for key, value := range deployment.Spec.Selector.MatchLabels {
			selectorParts = append(selectorParts, fmt.Sprintf("%s=%s", key, value))
		}
		labelSelector = strings.Join(selectorParts, ",")
	} else {
		// Fallback to app=deployment-name
		labelSelector = fmt.Sprintf("app=%s", deploymentName)
	}

	// Get pods for the deployment
	pods, err := o.k8sClient.GetClientset().CoreV1().Pods(o.config.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found for deployment %s", deploymentName)
	}

	healthyPods := 0
	for _, pod := range pods.Items {
		// Skip pods that are not running
		if pod.Status.Phase != corev1.PodRunning {
			o.logger.Debug("Skipping non-running pod",
				"deployment", deploymentName,
				"pod", pod.Name,
				"phase", pod.Status.Phase)
			continue
		}

		// Check if pod is ready using Kubernetes readiness status
		isPodReady := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				isPodReady = true
				break
			}
		}

		if isPodReady {
			healthyPods++
			o.logger.Debug("Pod is healthy for deployment",
				"deployment", deploymentName,
				"pod", pod.Name,
				"pod_ip", pod.Status.PodIP,
				"node", pod.Spec.NodeName)
		} else {
			o.logger.Warn("Pod is not ready for deployment",
				"deployment", deploymentName,
				"pod", pod.Name,
				"pod_ip", pod.Status.PodIP,
				"conditions", pod.Status.Conditions)
		}
	}

	if healthyPods == 0 {
		return fmt.Errorf("no healthy pods found for deployment %s", deploymentName)
	}

	o.logger.Info("Pod health verification completed for deployment",
		"deployment", deploymentName,
		"healthy_pods", healthyPods,
		"total_pods", len(pods.Items))

	return nil
}

// verifyDeploymentServiceEndpoints verifies service endpoints for a specific deployment
func (o *Orchestrator) verifyDeploymentServiceEndpoints(ctx context.Context, serviceName string) error {
	o.logger.Debug("Verifying service endpoints for deployment service",
		"service", serviceName)

	// Get service endpoints
	endpoints, err := o.k8sClient.GetClientset().CoreV1().Endpoints(o.config.Namespace).Get(
		ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get service endpoints: %w", err)
	}

	if len(endpoints.Subsets) == 0 {
		return fmt.Errorf("service %s has no endpoint subsets", serviceName)
	}

	totalEndpoints := 0
	readyEndpoints := 0

	for _, subset := range endpoints.Subsets {
		totalEndpoints += len(subset.Addresses) + len(subset.NotReadyAddresses)
		readyEndpoints += len(subset.Addresses)

		// Log ready endpoints
		for _, addr := range subset.Addresses {
			o.logger.Debug("Service endpoint is ready for deployment service",
				"service", serviceName,
				"endpoint_ip", addr.IP,
				"target_ref", addr.TargetRef)
		}

		// Log not ready endpoints
		for _, addr := range subset.NotReadyAddresses {
			o.logger.Debug("Service endpoint is not ready for deployment service",
				"service", serviceName,
				"endpoint_ip", addr.IP,
				"target_ref", addr.TargetRef)
		}
	}

	if readyEndpoints == 0 {
		return fmt.Errorf("service %s has no ready endpoints", serviceName)
	}

	o.logger.Info("Service endpoint verification completed for deployment service",
		"service", serviceName,
		"ready_endpoints", readyEndpoints,
		"total_endpoints", totalEndpoints)

	return nil
}

// executeTwoPhaseMigration implements the industry-standard two-phase migration approach
func (o *Orchestrator) executeTwoPhaseMigration(ctx context.Context, deploymentConfigs []DeploymentConfigInfo, event watcher.SpotEvent) error {
	o.logger.Info("Starting TWO-PHASE MIGRATION for spot interruption",
		"deployment_count", len(deploymentConfigs),
		"instance_id", event.InstanceID,
		"time_remaining", event.TimeRemaining.String())

	// PHASE 1: FAN-OUT (fast, non-blocking)
	// Patch all deployments to Fargate immediately without waiting for rollouts
	patchedDeployments, err := o.phaseFanOut(ctx, deploymentConfigs, event)
	if err != nil {
		return fmt.Errorf("phase 1 (fan-out) failed: %w", err)
	}

	if len(patchedDeployments) == 0 {
		o.logger.Info("No deployments were successfully patched")
		return nil
	}

	// PHASE 2: OBSERVE (parallel)
	// Monitor all rollouts in parallel, each deployment succeeds/fails independently
	return o.phaseObserve(ctx, patchedDeployments, event)
}

// phaseFanOut patches all deployments to Fargate as quickly as possible
func (o *Orchestrator) phaseFanOut(ctx context.Context, deploymentConfigs []DeploymentConfigInfo, _ watcher.SpotEvent) ([]DeploymentConfigInfo, error) {
	o.logger.Info("PHASE 1: FAN-OUT - Patching all deployments to Fargate immediately",
		"deployment_count", len(deploymentConfigs))

	var patchedDeployments []DeploymentConfigInfo
	fanOutStart := time.Now()

	for _, deploymentInfo := range deploymentConfigs {
		o.logger.Info("Patching deployment to Fargate (non-blocking)",
			"deployment", deploymentInfo.Name,
			"affected_pods", len(deploymentInfo.PodNames))

		// Send migration start alert
		o.alertManager.MigrationStarted(ctx,
			deploymentInfo.Name,
			deploymentInfo.Namespace,
			o.config.SpotLabel,
			o.config.FargateLabel)

		// Step 1: Configure zero-downtime strategy
		if err := o.ensureZeroDowntimeStrategyForDeployment(ctx, deploymentInfo.Name); err != nil {
			o.logger.Error("Failed to configure zero-downtime strategy, skipping deployment",
				"deployment", deploymentInfo.Name,
				"error", err)
			continue
		}

		// Step 2: Patch deployment to Fargate (DO NOT WAIT for rollout)
		if err := o.patcher.PatchComputeTypeWithReason(ctx, deploymentInfo.Name, o.config.FargateLabel, annotations.ReasonSpotInterruption); err != nil {
			o.logger.Error("Failed to patch deployment to Fargate, skipping",
				"deployment", deploymentInfo.Name,
				"error", err)

			// Attempt rollback of zero-downtime strategy
			if rollbackErr := o.rollbackZeroDowntimeStrategyForDeployment(ctx, deploymentInfo.Name); rollbackErr != nil {
				o.logger.Error("Failed to rollback zero-downtime strategy",
					"deployment", deploymentInfo.Name,
					"error", rollbackErr)
			}
			continue
		}

		// Step 3: Record successful patch (DO NOT wait for rollout completion)
		o.logger.Info("Deployment successfully patched to Fargate",
			"deployment", deploymentInfo.Name,
			"patch_time", time.Since(fanOutStart))

		patchedDeployments = append(patchedDeployments, deploymentInfo)
	}

	fanOutDuration := time.Since(fanOutStart)
	o.logger.Info("PHASE 1: FAN-OUT completed",
		"total_deployments", len(deploymentConfigs),
		"successfully_patched", len(patchedDeployments),
		"failed_patches", len(deploymentConfigs)-len(patchedDeployments),
		"fan_out_duration", fanOutDuration)

	return patchedDeployments, nil
}

// phaseObserve monitors all rollouts in parallel
func (o *Orchestrator) phaseObserve(ctx context.Context, patchedDeployments []DeploymentConfigInfo, event watcher.SpotEvent) error {
	o.logger.Info("PHASE 2: OBSERVE - Monitoring rollouts in parallel",
		"deployment_count", len(patchedDeployments))

	// Create a channel to collect results from parallel monitoring
	type migrationResult struct {
		deploymentName string
		success        bool
		error          error
		duration       time.Duration
	}

	resultChan := make(chan migrationResult, len(patchedDeployments))
	observeStart := time.Now()

	// Start parallel monitoring for each deployment
	for _, deploymentInfo := range patchedDeployments {
		go func(depInfo DeploymentConfigInfo) {
			result := migrationResult{
				deploymentName: depInfo.Name,
			}

			migrationStart := time.Now()

			// Monitor this deployment's rollout and verification
			if err := o.monitorDeploymentMigration(ctx, depInfo); err != nil {
				result.success = false
				result.error = err
				result.duration = time.Since(migrationStart)

				o.logger.Error("Deployment migration failed during observation",
					"deployment", depInfo.Name,
					"error", err,
					"duration", result.duration)

				// Attempt rollback for failed deployment
				if rollbackErr := o.rollbackDeploymentMigration(ctx, depInfo.Name); rollbackErr != nil {
					o.logger.Error("Failed to rollback deployment after migration failure",
						"deployment", depInfo.Name,
						"rollback_error", rollbackErr)
				}
			} else {
				result.success = true
				result.duration = time.Since(migrationStart)

				o.logger.Info("Deployment migration completed successfully",
					"deployment", depInfo.Name,
					"duration", result.duration)

				// Send success alert
				pods, err := o.k8sClient.GetClientset().CoreV1().Pods(depInfo.Namespace).List(ctx, metav1.ListOptions{
					LabelSelector: fmt.Sprintf("app=%s", depInfo.Name),
				})
				podsCount := 0
				if err == nil {
					podsCount = len(pods.Items)
				}

				o.alertManager.MigrationCompleted(ctx,
					depInfo.Name,
					depInfo.Namespace,
					result.duration,
					podsCount)
			}

			resultChan <- result
		}(deploymentInfo)
	}

	// Collect results from all parallel migrations
	var successCount, failureCount int
	var totalDuration time.Duration

	for i := 0; i < len(patchedDeployments); i++ {
		result := <-resultChan

		if result.success {
			successCount++
		} else {
			failureCount++
		}

		if result.duration > totalDuration {
			totalDuration = result.duration
		}
	}

	observeDuration := time.Since(observeStart)

	o.logger.Info("PHASE 2: OBSERVE completed",
		"total_deployments", len(patchedDeployments),
		"successful_migrations", successCount,
		"failed_migrations", failureCount,
		"observe_duration", observeDuration,
		"longest_migration", totalDuration,
		"instance_id", event.InstanceID)

	// Return success if at least one deployment migrated successfully
	if successCount > 0 {
		return nil
	}

	return fmt.Errorf("all %d deployment migrations failed", len(patchedDeployments))
}

// monitorDeploymentMigration monitors a single deployment's rollout and verification
func (o *Orchestrator) monitorDeploymentMigration(ctx context.Context, deploymentInfo DeploymentConfigInfo) error {
	// Step 1: Wait for rollout to complete (using deployment-specific timeout)
	rolloutTimeout := deploymentInfo.Config.GetRolloutTimeout()

	o.logger.Debug("Waiting for deployment rollout",
		"deployment", deploymentInfo.Name,
		"timeout", rolloutTimeout)

	if err := o.monitor.WaitForRollout(ctx, deploymentInfo.Name, rolloutTimeout); err != nil {
		return fmt.Errorf("rollout failed or timed out: %w", err)
	}

	o.logger.Debug("Rollout completed, verifying migration",
		"deployment", deploymentInfo.Name)

	// Step 2: Verify migration with deployment-specific verification delay
	if err := o.verifyDeploymentMigration(ctx, deploymentInfo); err != nil {
		return fmt.Errorf("migration verification failed: %w", err)
	}

	return nil
}
