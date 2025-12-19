package watcher

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"spot-fargate-orchestrator/internal/kubernetes/client"
	"spot-fargate-orchestrator/internal/logger"
	"spot-fargate-orchestrator/internal/spot/eventbridge"
)

// EventBridgeSpotWatcher monitors spot interruptions using AWS EventBridge via SQS
type EventBridgeSpotWatcher struct {
	eventBridgeClient *eventbridge.EventBridgeClient
	k8sClient         *client.K8sClient
	eventChan         chan SpotEvent
	logger            *logger.Logger
	deploymentName    string
	namespace         string
	region            string
}

// NewEventBridgeSpotWatcher creates a new EventBridge-based spot watcher for single deployment
func NewEventBridgeSpotWatcher(k8sClient *client.K8sClient, deploymentName, namespace, region, queueURL string) (*EventBridgeSpotWatcher, error) {
	logger := logger.NewDefault("eventbridge-spot-watcher")

	eventBridgeClient, err := eventbridge.NewEventBridgeClient(region, queueURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create EventBridge client: %w", err)
	}

	return &EventBridgeSpotWatcher{
		eventBridgeClient: eventBridgeClient,
		k8sClient:         k8sClient,
		eventChan:         make(chan SpotEvent, 10),
		logger:            logger,
		deploymentName:    deploymentName,
		namespace:         namespace,
		region:            region,
	}, nil
}

// NewMultiDeploymentEventBridgeSpotWatcher creates a new EventBridge-based spot watcher for multiple deployments
func NewMultiDeploymentEventBridgeSpotWatcher(k8sClient *client.K8sClient, namespace, region, queueURL string) (*EventBridgeSpotWatcher, error) {
	logger := logger.NewDefault("eventbridge-spot-watcher")

	eventBridgeClient, err := eventbridge.NewEventBridgeClient(region, queueURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create EventBridge client: %w", err)
	}

	return &EventBridgeSpotWatcher{
		eventBridgeClient: eventBridgeClient,
		k8sClient:         k8sClient,
		eventChan:         make(chan SpotEvent, 10),
		logger:            logger,
		deploymentName:    "", // Empty for multi-deployment mode
		namespace:         namespace,
		region:            region,
	}, nil
}

// Start begins monitoring for spot interruption events via EventBridge
func (w *EventBridgeSpotWatcher) Start(ctx context.Context) error {
	w.logger.Info("Starting EventBridge spot watcher",
		"deployment", w.deploymentName,
		"namespace", w.namespace,
		"region", w.region)

	w.logger.Info("EventBridge spot watcher started - polling SQS for EC2 Spot Instance Interruption Warnings")

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("EventBridge spot watcher stopping due to context cancellation")
			close(w.eventChan)
			return ctx.Err()

		default:
			if err := w.pollForSpotInterruptions(ctx); err != nil {
				w.logger.Error("Error polling for spot interruptions", "error", err)
				// Sleep briefly before retrying to avoid tight loop on persistent errors
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Second):
					continue
				}
			}
		}
	}
}

// pollForSpotInterruptions polls EventBridge via SQS for spot interruption events
func (w *EventBridgeSpotWatcher) pollForSpotInterruptions(ctx context.Context) error {
	// Get spot interruption events from EventBridge via SQS
	events, err := w.eventBridgeClient.PollForSpotInterruptions(ctx)
	if err != nil {
		return fmt.Errorf("failed to poll for spot interruption events: %w", err)
	}

	if len(events) == 0 {
		// No events - this is normal, continue polling
		return nil
	}

	w.logger.Info("Received spot interruption events from EventBridge",
		"event_count", len(events))

	// Process each event
	for _, event := range events {
		if err := w.processSpotInterruptionEvent(ctx, event); err != nil {
			w.logger.Error("Failed to process spot interruption event",
				"instance_id", event.Detail.InstanceID,
				"error", err)
			// Continue processing other events
		}
	}

	return nil
}

// processSpotInterruptionEvent processes a single spot interruption event
func (w *EventBridgeSpotWatcher) processSpotInterruptionEvent(ctx context.Context, event eventbridge.SpotInterruptionEvent) error {
	instanceID := event.Detail.InstanceID

	w.logger.Info("Processing spot interruption event",
		"instance_id", instanceID,
		"action", event.Detail.InstanceAction,
		"availability_zone", event.Detail.AvailabilityZone,
		"event_time", event.Time.Format(time.RFC3339))

	// Step 1: Find the Kubernetes node for this instance ID
	nodeName, err := w.findNodeByInstanceID(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to find node for instance %s: %w", instanceID, err)
	}

	if nodeName == "" {
		w.logger.Debug("No Kubernetes node found for interrupted instance",
			"instance_id", instanceID)
		return nil // Not an error - instance might not be part of our cluster
	}

	w.logger.Info("Found Kubernetes node for interrupted instance",
		"instance_id", instanceID,
		"node_name", nodeName)

	// Step 2: Find pods on this node that belong to our target deployment
	affectedPods, err := w.findAffectedPods(ctx, nodeName)
	if err != nil {
		return fmt.Errorf("failed to find affected pods on node %s: %w", nodeName, err)
	}

	if len(affectedPods) == 0 {
		w.logger.Debug("No target deployment pods found on interrupted node",
			"instance_id", instanceID,
			"node_name", nodeName,
			"deployment", w.deploymentName)
		return nil // Not an error - our deployment might not be on this node
	}

	w.logger.Info("Found affected pods on interrupted node",
		"instance_id", instanceID,
		"node_name", nodeName,
		"affected_pods", len(affectedPods),
		"deployment", w.deploymentName)

	// Step 3: Calculate time remaining (spot interruptions give 2 minutes warning)
	timeRemaining := time.Until(event.Time.Add(2 * time.Minute))
	if timeRemaining < 0 {
		timeRemaining = 0
	}

	// Step 4: Create and send spot event
	spotEvent := SpotEvent{
		Type:          EventSpotInterruption,
		Timestamp:     time.Now(),
		TimeRemaining: timeRemaining,
		InstanceID:    instanceID,
		NodeName:      nodeName,
		AffectedPods:  affectedPods,
		Notice: &InterruptionNotice{
			Action: event.Detail.InstanceAction,
			Time:   event.Time.Add(2 * time.Minute), // Actual interruption time
		},
	}

	// Send event to channel (non-blocking)
	select {
	case w.eventChan <- spotEvent:
		w.logger.Info("Spot interruption event sent successfully",
			"instance_id", instanceID,
			"node_name", nodeName,
			"time_remaining", timeRemaining.String())
	default:
		w.logger.Warn("Event channel full, dropping spot interruption event",
			"instance_id", instanceID,
			"node_name", nodeName)
	}

	return nil
}

// findNodeByInstanceID finds the Kubernetes node that corresponds to the given EC2 instance ID
func (w *EventBridgeSpotWatcher) findNodeByInstanceID(ctx context.Context, instanceID string) (string, error) {
	// List all nodes in the cluster
	nodes, err := w.k8sClient.GetClientset().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}

	// Look for node with matching providerID
	expectedProviderID := fmt.Sprintf("aws:///%s/%s", w.region, instanceID)

	for _, node := range nodes.Items {
		// Check exact providerID match
		if node.Spec.ProviderID == expectedProviderID {
			return node.Name, nil
		}

		// Also check if providerID contains the instance ID (more flexible matching)
		if strings.Contains(node.Spec.ProviderID, instanceID) {
			return node.Name, nil
		}
	}

	// No matching node found
	return "", nil
}

// getDeploymentLabelSelector returns the correct label selector for the deployment
func (w *EventBridgeSpotWatcher) getDeploymentLabelSelector(ctx context.Context) (string, error) {
	deployment, err := w.k8sClient.GetClientset().AppsV1().Deployments(w.namespace).Get(
		ctx, w.deploymentName, metav1.GetOptions{})
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
	return fmt.Sprintf("app=%s", w.deploymentName), nil
}

// findAffectedPods finds pods belonging to managed deployments that are running on the specified node
func (w *EventBridgeSpotWatcher) findAffectedPods(ctx context.Context, nodeName string) ([]string, error) {
	if w.deploymentName != "" {
		// Single deployment mode - use existing logic
		return w.findSingleDeploymentAffectedPods(ctx, nodeName)
	}

	// Multi-deployment mode - find all opted-in deployments' pods on the node
	return w.findMultiDeploymentAffectedPods(ctx, nodeName)
}

// findSingleDeploymentAffectedPods finds pods for a specific deployment on the node
func (w *EventBridgeSpotWatcher) findSingleDeploymentAffectedPods(ctx context.Context, nodeName string) ([]string, error) {
	// Get the correct label selector for the deployment
	labelSelector, err := w.getDeploymentLabelSelector(ctx)
	if err != nil {
		w.logger.Warn("Failed to get deployment label selector, using fallback", "error", err)
		labelSelector = fmt.Sprintf("app=%s", w.deploymentName)
	}

	// Get pods for the target deployment on the specific node
	pods, err := w.k8sClient.GetClientset().CoreV1().Pods(w.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", nodeName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods for deployment %s on node %s: %w",
			w.deploymentName, nodeName, err)
	}

	var affectedPods []string
	for _, pod := range pods.Items {
		// Only include running pods
		if pod.Status.Phase == corev1.PodRunning {
			affectedPods = append(affectedPods, pod.Name)
		}
	}

	return affectedPods, nil
}

// findMultiDeploymentAffectedPods finds pods from opted-in deployments on the node
func (w *EventBridgeSpotWatcher) findMultiDeploymentAffectedPods(ctx context.Context, nodeName string) ([]string, error) {
	w.logger.Debug("Finding affected pods for multi-deployment mode",
		"node", nodeName,
		"namespace", w.namespace)

	// Step 1: Get ALL pods on the interrupted node
	allPods, err := w.k8sClient.GetClientset().CoreV1().Pods(w.namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", nodeName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list all pods on node %s: %w", nodeName, err)
	}

	w.logger.Debug("Found pods on interrupted node",
		"node", nodeName,
		"total_pods", len(allPods.Items))

	// Step 2: Get all opted-in deployments
	optedInDeployments, err := w.getOptedInDeployments(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get opted-in deployments: %w", err)
	}

	w.logger.Debug("Found opted-in deployments",
		"count", len(optedInDeployments))

	// Step 3: Filter pods - only include those belonging to opted-in deployments
	var affectedPods []string
	for _, pod := range allPods.Items {
		// Only include running pods
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		// Check if this pod belongs to an opted-in deployment
		if w.isPodFromOptedInDeployment(&pod, optedInDeployments) {
			affectedPods = append(affectedPods, pod.Name)
			w.logger.Debug("Pod belongs to opted-in deployment",
				"pod", pod.Name,
				"deployment", w.getDeploymentNameFromPod(&pod))
		} else {
			w.logger.Debug("Pod does not belong to opted-in deployment, skipping",
				"pod", pod.Name)
		}
	}

	w.logger.Info("Multi-deployment affected pods analysis",
		"node", nodeName,
		"total_pods_on_node", len(allPods.Items),
		"opted_in_deployments", len(optedInDeployments),
		"affected_pods", len(affectedPods))

	return affectedPods, nil
}

// getOptedInDeployments returns all deployments with opt-in annotation in the namespace
func (w *EventBridgeSpotWatcher) getOptedInDeployments(ctx context.Context) (map[string]bool, error) {
	// List all deployments in the namespace
	deployments, err := w.k8sClient.GetClientset().AppsV1().Deployments(w.namespace).List(
		ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}

	optedInDeployments := make(map[string]bool)

	for _, deployment := range deployments.Items {
		// Check if deployment has opt-in annotation
		if w.hasOptInAnnotation(&deployment) {
			optedInDeployments[deployment.Name] = true
			w.logger.Debug("Found opted-in deployment", "deployment", deployment.Name)
		}
	}

	return optedInDeployments, nil
}

// hasOptInAnnotation checks if deployment has the opt-in annotation
func (w *EventBridgeSpotWatcher) hasOptInAnnotation(deployment *appsv1.Deployment) bool {
	if deployment.Annotations == nil {
		return false
	}

	// For multi-deployment mode, we need to check the standard opt-in annotation
	// Since we don't have access to config here, we'll use the standard annotation
	value, exists := deployment.Annotations["spot-orchestrator/enabled"]
	if !exists {
		return false
	}

	// Accept "true", "yes", "1" as valid opt-in values
	normalizedValue := strings.ToLower(strings.TrimSpace(value))
	return normalizedValue == "true" || normalizedValue == "yes" || normalizedValue == "1"
}

// isPodFromOptedInDeployment checks if a pod belongs to an opted-in deployment
func (w *EventBridgeSpotWatcher) isPodFromOptedInDeployment(pod *corev1.Pod, optedInDeployments map[string]bool) bool {
	deploymentName := w.getDeploymentNameFromPod(pod)
	if deploymentName == "" {
		return false
	}

	return optedInDeployments[deploymentName]
}

// getDeploymentNameFromPod extracts the deployment name from pod's owner references
func (w *EventBridgeSpotWatcher) getDeploymentNameFromPod(pod *corev1.Pod) string {
	// Walk up the ownership chain: Pod → ReplicaSet → Deployment
	for _, ownerRef := range pod.OwnerReferences {
		if ownerRef.Kind == "ReplicaSet" {
			// Get the ReplicaSet to find its owner (Deployment)
			rs, err := w.k8sClient.GetClientset().AppsV1().ReplicaSets(pod.Namespace).Get(
				context.Background(), ownerRef.Name, metav1.GetOptions{})
			if err != nil {
				w.logger.Debug("Failed to get ReplicaSet", "replicaset", ownerRef.Name, "error", err)
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

// EventChannel returns the channel for receiving spot events
func (w *EventBridgeSpotWatcher) EventChannel() <-chan SpotEvent {
	return w.eventChan
}
