package patcher

import (
	"context"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"spot-fargate-orchestrator/internal/kubernetes/annotations"
	"spot-fargate-orchestrator/internal/kubernetes/client"
	"spot-fargate-orchestrator/internal/logger"
)

// PatchOperation represents a JSON patch operation
type PatchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}

// DeploymentPatcher handles Kubernetes deployment patching
type DeploymentPatcher struct {
	client            *client.K8sClient
	annotationManager *annotations.AnnotationManager
	logger            *logger.Logger
}

// NewDeploymentPatcher creates a new deployment patcher
func NewDeploymentPatcher(client *client.K8sClient) *DeploymentPatcher {
	return &DeploymentPatcher{
		client:            client,
		annotationManager: annotations.NewAnnotationManager(client),
		logger:            logger.NewDefault("deployment-patcher"),
	}
}

// PatchComputeType patches a deployment to change compute type
func (p *DeploymentPatcher) PatchComputeType(ctx context.Context, deploymentName, computeType string) error {
	return p.PatchComputeTypeWithReason(ctx, deploymentName, computeType, annotations.ReasonManual)
}

// PatchComputeTypeWithReason patches a deployment to change compute type with a specific reason
func (p *DeploymentPatcher) PatchComputeTypeWithReason(ctx context.Context, deploymentName, computeType, reason string) error {
	p.logger.Info("Starting deployment patch",
		"deployment", deploymentName,
		"target_compute_type", computeType,
		"reason", reason,
		"namespace", p.client.GetNamespace())

	// Get current deployment to understand its state
	deployment, err := p.client.GetClientset().AppsV1().Deployments(p.client.GetNamespace()).Get(
		ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}

	p.logger.Debug("Retrieved current deployment",
		"deployment", deploymentName,
		"current_replicas", *deployment.Spec.Replicas)

	// Generate patch operations based on target compute type
	patchOps, err := p.generatePatchOperations(deployment, computeType)
	if err != nil {
		return fmt.Errorf("failed to generate patch operations: %w", err)
	}

	if len(patchOps) == 0 {
		p.logger.Info("No patch operations needed, deployment already configured for target compute type",
			"deployment", deploymentName,
			"compute_type", computeType)
		return nil
	}

	// Convert patch operations to JSON
	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		return fmt.Errorf("failed to marshal patch operations: %w", err)
	}

	p.logger.Debug("Generated patch operations",
		"deployment", deploymentName,
		"patch", string(patchBytes),
		"operations_count", len(patchOps))

	// Apply the patch
	_, err = p.client.GetClientset().AppsV1().Deployments(p.client.GetNamespace()).Patch(
		ctx, deploymentName, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to apply patch to deployment %s: %w", deploymentName, err)
	}

	p.logger.Info("Successfully patched deployment",
		"deployment", deploymentName,
		"target_compute_type", computeType,
		"operations_applied", len(patchOps))

	// Update deployment annotations to track the migration
	var annotationState string
	switch computeType {
	case "fargate":
		annotationState = annotations.StateFargate
	case "spot":
		annotationState = annotations.StateSpot
	default:
		annotationState = computeType // Use the compute type directly if it doesn't match known types
	}

	if err := p.annotationManager.SetDeploymentState(ctx, deploymentName, p.client.GetNamespace(), annotationState, reason); err != nil {
		p.logger.Warn("Failed to update deployment annotations after successful patch",
			"deployment", deploymentName,
			"state", annotationState,
			"reason", reason,
			"error", err)
		// Don't fail the entire operation if annotation update fails
	}

	return nil
}

// generatePatchOperations creates the JSON patch operations for compute type change
func (p *DeploymentPatcher) generatePatchOperations(deployment *appsv1.Deployment, targetComputeType string) ([]PatchOperation, error) {
	var patchOps []PatchOperation

	// Determine current compute type from labels
	currentComputeType := ""
	if deployment.Spec.Template.Labels != nil {
		currentComputeType = deployment.Spec.Template.Labels["compute-type"]
	}

	// Skip if already the target compute type
	if currentComputeType == targetComputeType {
		return patchOps, nil
	}

	p.logger.Debug("Generating patch operations",
		"current_compute_type", currentComputeType,
		"target_compute_type", targetComputeType)

	// Ensure labels exist in the template
	if deployment.Spec.Template.Labels == nil {
		patchOps = append(patchOps, PatchOperation{
			Op:    "add",
			Path:  "/spec/template/metadata/labels",
			Value: map[string]string{},
		})
	}

	// Update compute-type label
	patchOps = append(patchOps, PatchOperation{
		Op:    "replace",
		Path:  "/spec/template/metadata/labels/compute-type",
		Value: targetComputeType,
	})

	// Generate compute-type specific patches
	switch targetComputeType {
	case "fargate":
		patchOps = append(patchOps, p.generateFargatePatches(deployment)...)
	case "spot":
		patchOps = append(patchOps, p.generateSpotPatches(deployment)...)
	default:
		return nil, fmt.Errorf("unsupported compute type: %s", targetComputeType)
	}

	return patchOps, nil
}

// generateFargatePatches creates patches for Fargate compute type
func (p *DeploymentPatcher) generateFargatePatches(deployment *appsv1.Deployment) []PatchOperation {
	var patchOps []PatchOperation

	// Add Fargate nodeSelector
	if deployment.Spec.Template.Spec.NodeSelector == nil {
		patchOps = append(patchOps, PatchOperation{
			Op:    "add",
			Path:  "/spec/template/spec/nodeSelector",
			Value: map[string]string{},
		})
	}

	patchOps = append(patchOps, PatchOperation{
		Op:    "replace",
		Path:  "/spec/template/spec/nodeSelector/eks.amazonaws.com~1compute-type",
		Value: "fargate",
	})

	// Remove spot nodeSelector if it exists
	if deployment.Spec.Template.Spec.NodeSelector != nil {
		if _, exists := deployment.Spec.Template.Spec.NodeSelector["capacity-type"]; exists {
			patchOps = append(patchOps, PatchOperation{
				Op:   "remove",
				Path: "/spec/template/spec/nodeSelector/capacity-type",
			})
		}
	}

	// Remove spot tolerations if they exist
	if deployment.Spec.Template.Spec.Tolerations != nil {
		// Remove spot toleration by setting empty tolerations array
		patchOps = append(patchOps, PatchOperation{
			Op:    "replace",
			Path:  "/spec/template/spec/tolerations",
			Value: []corev1.Toleration{},
		})
	}

	return patchOps
}

// generateSpotPatches creates patches for spot compute type
func (p *DeploymentPatcher) generateSpotPatches(deployment *appsv1.Deployment) []PatchOperation {
	var patchOps []PatchOperation

	// Remove Fargate nodeSelector if it exists
	if deployment.Spec.Template.Spec.NodeSelector != nil {
		if _, exists := deployment.Spec.Template.Spec.NodeSelector["eks.amazonaws.com/compute-type"]; exists {
			patchOps = append(patchOps, PatchOperation{
				Op:   "remove",
				Path: "/spec/template/spec/nodeSelector/eks.amazonaws.com~1compute-type",
			})
		}
	}

	// Add spot nodeSelector
	if deployment.Spec.Template.Spec.NodeSelector == nil {
		patchOps = append(patchOps, PatchOperation{
			Op:    "add",
			Path:  "/spec/template/spec/nodeSelector",
			Value: map[string]string{},
		})
	}

	patchOps = append(patchOps, PatchOperation{
		Op:    "replace",
		Path:  "/spec/template/spec/nodeSelector/capacity-type",
		Value: "spot",
	})

	// Add spot tolerations
	spotTolerations := []corev1.Toleration{
		{
			Key:      "capacity-type",
			Operator: corev1.TolerationOpEqual,
			Value:    "spot",
			Effect:   corev1.TaintEffectNoSchedule,
		},
	}

	if deployment.Spec.Template.Spec.Tolerations == nil {
		patchOps = append(patchOps, PatchOperation{
			Op:    "add",
			Path:  "/spec/template/spec/tolerations",
			Value: spotTolerations,
		})
	} else {
		patchOps = append(patchOps, PatchOperation{
			Op:    "replace",
			Path:  "/spec/template/spec/tolerations",
			Value: spotTolerations,
		})
	}

	return patchOps
}
