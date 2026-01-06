package patcher

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"spot-fargate-orchestrator/internal/kubernetes/client"
	"spot-fargate-orchestrator/internal/logger"
)

// NodeSelectorConflict represents a conflicting nodeSelector configuration
type NodeSelectorConflict struct {
	DeploymentName string
	Namespace      string
	ConflictType   string
	NodeSelectors  map[string]string
	Reason         string
}

// NodeSelectorValidator validates and detects nodeSelector conflicts
type NodeSelectorValidator struct {
	client *client.K8sClient
	logger *logger.Logger
}

// NewNodeSelectorValidator creates a new nodeSelector validator
func NewNodeSelectorValidator(client *client.K8sClient) *NodeSelectorValidator {
	return &NodeSelectorValidator{
		client: client,
		logger: logger.NewDefault("node-selector-validator"),
	}
}

// ValidateDeploymentNodeSelector checks for conflicting nodeSelector configurations
func (v *NodeSelectorValidator) ValidateDeploymentNodeSelector(ctx context.Context, deploymentName, namespace string) (*NodeSelectorConflict, error) {
	deployment, err := v.client.GetClientset().AppsV1().Deployments(namespace).Get(
		ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}

	return v.validateNodeSelector(deployment), nil
}

// ValidateAllDeployments checks all deployments in a namespace for nodeSelector conflicts
func (v *NodeSelectorValidator) ValidateAllDeployments(ctx context.Context, namespace string) ([]NodeSelectorConflict, error) {
	deployments, err := v.client.GetClientset().AppsV1().Deployments(namespace).List(
		ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments in namespace %s: %w", namespace, err)
	}

	var conflicts []NodeSelectorConflict
	for _, deployment := range deployments.Items {
		if conflict := v.validateNodeSelector(&deployment); conflict != nil {
			conflicts = append(conflicts, *conflict)
		}
	}

	return conflicts, nil
}

// validateNodeSelector checks a single deployment for nodeSelector conflicts
func (v *NodeSelectorValidator) validateNodeSelector(deployment *appsv1.Deployment) *NodeSelectorConflict {
	nodeSelector := deployment.Spec.Template.Spec.NodeSelector
	if nodeSelector == nil || len(nodeSelector) == 0 {
		return nil
	}

	// Check for the specific Fargate + Spot conflict
	hasSpot := false
	hasFargate := false

	if capacityType, exists := nodeSelector["capacity-type"]; exists && capacityType == "spot" {
		hasSpot = true
	}

	if computeType, exists := nodeSelector["eks.amazonaws.com/compute-type"]; exists && computeType == "fargate" {
		hasFargate = true
	}

	// Detect the impossible combination
	if hasSpot && hasFargate {
		v.logger.Error("CRITICAL: Detected impossible nodeSelector combination",
			"deployment", deployment.Name,
			"namespace", deployment.Namespace,
			"node_selectors", nodeSelector,
			"issue", "Cannot schedule on both Spot instances and Fargate")

		return &NodeSelectorConflict{
			DeploymentName: deployment.Name,
			Namespace:      deployment.Namespace,
			ConflictType:   "spot-fargate-conflict",
			NodeSelectors:  nodeSelector,
			Reason:         "Deployment has both capacity-type=spot and eks.amazonaws.com/compute-type=fargate, which is impossible to satisfy",
		}
	}

	// Check for other potential conflicts
	if otherConflict := v.detectOtherConflicts(deployment, nodeSelector); otherConflict != nil {
		return otherConflict
	}

	return nil
}

// detectOtherConflicts checks for other types of nodeSelector conflicts
func (v *NodeSelectorValidator) detectOtherConflicts(deployment *appsv1.Deployment, nodeSelector map[string]string) *NodeSelectorConflict {
	// Check for multiple capacity-type values (shouldn't happen but worth checking)
	if len(nodeSelector) > 3 { // More selectors than expected might indicate issues
		v.logger.Warn("Deployment has unusually many nodeSelectors",
			"deployment", deployment.Name,
			"namespace", deployment.Namespace,
			"selector_count", len(nodeSelector),
			"selectors", nodeSelector)
	}

	// Check for conflicting instance types
	if instanceType, exists := nodeSelector["node.kubernetes.io/instance-type"]; exists {
		if capacityType, hasCapacity := nodeSelector["capacity-type"]; hasCapacity {
			// Some instance types don't support spot
			if capacityType == "spot" && strings.HasPrefix(instanceType, "t2.") {
				return &NodeSelectorConflict{
					DeploymentName: deployment.Name,
					Namespace:      deployment.Namespace,
					ConflictType:   "instance-type-spot-conflict",
					NodeSelectors:  nodeSelector,
					Reason:         fmt.Sprintf("Instance type %s does not support spot capacity", instanceType),
				}
			}
		}
	}

	return nil
}

// FixConflict attempts to fix a nodeSelector conflict by choosing the most appropriate configuration
func (v *NodeSelectorValidator) FixConflict(ctx context.Context, conflict *NodeSelectorConflict, preferredComputeType string) error {
	v.logger.Info("Attempting to fix nodeSelector conflict",
		"deployment", conflict.DeploymentName,
		"namespace", conflict.Namespace,
		"conflict_type", conflict.ConflictType,
		"preferred_compute_type", preferredComputeType)

	patcher := NewDeploymentPatcher(v.client)

	// Fix based on conflict type
	switch conflict.ConflictType {
	case "spot-fargate-conflict":
		// Choose based on preference, defaulting to spot if no preference
		targetComputeType := "spot"
		if preferredComputeType == "fargate" {
			targetComputeType = "fargate"
		}

		v.logger.Info("Fixing spot-fargate conflict by migrating to preferred compute type",
			"deployment", conflict.DeploymentName,
			"target_compute_type", targetComputeType)

		return patcher.PatchComputeTypeWithReason(ctx, conflict.DeploymentName, targetComputeType, "conflict-resolution")

	case "instance-type-spot-conflict":
		// Remove the conflicting instance type selector and keep spot
		v.logger.Info("Fixing instance-type-spot conflict by removing instance type constraint",
			"deployment", conflict.DeploymentName)

		return patcher.PatchComputeTypeWithReason(ctx, conflict.DeploymentName, "spot", "conflict-resolution")

	default:
		return fmt.Errorf("unknown conflict type: %s", conflict.ConflictType)
	}
}

// LogConflictSummary logs a summary of all detected conflicts
func (v *NodeSelectorValidator) LogConflictSummary(conflicts []NodeSelectorConflict) {
	if len(conflicts) == 0 {
		v.logger.Info("No nodeSelector conflicts detected")
		return
	}

	v.logger.Error("NODESELCTOR CONFLICTS DETECTED",
		"total_conflicts", len(conflicts))

	conflictTypes := make(map[string]int)
	for _, conflict := range conflicts {
		conflictTypes[conflict.ConflictType]++

		v.logger.Error("NodeSelector conflict details",
			"deployment", conflict.DeploymentName,
			"namespace", conflict.Namespace,
			"conflict_type", conflict.ConflictType,
			"reason", conflict.Reason,
			"node_selectors", conflict.NodeSelectors)
	}

	v.logger.Error("Conflict summary by type", "conflict_types", conflictTypes)
}
