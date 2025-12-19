package annotations

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"spot-fargate-orchestrator/internal/kubernetes/client"
	"spot-fargate-orchestrator/internal/logger"
)

// Annotation keys for deployment state tracking
const (
	// StateAnnotation tracks current compute state (spot | fargate)
	StateAnnotation = "spot-orchestrator/state"

	// MigratedAtAnnotation tracks when the last migration occurred (RFC3339 timestamp)
	MigratedAtAnnotation = "spot-orchestrator/migrated-at"

	// ReasonAnnotation tracks why the migration occurred (spot-interruption | manual | rollback)
	ReasonAnnotation = "spot-orchestrator/reason"
)

// Migration reasons
const (
	ReasonSpotInterruption = "spot-interruption"
	ReasonManual           = "manual"
	ReasonRollback         = "rollback"
)

// Compute states
const (
	StateSpot    = "spot"
	StateFargate = "fargate"
)

// DeploymentState represents the current state from annotations
type DeploymentState struct {
	State      string    `json:"state"`
	MigratedAt time.Time `json:"migrated_at"`
	Reason     string    `json:"reason"`
}

// AnnotationManager handles deployment annotations for state tracking
type AnnotationManager struct {
	client *client.K8sClient
	logger *logger.Logger
}

// NewAnnotationManager creates a new annotation manager
func NewAnnotationManager(client *client.K8sClient) *AnnotationManager {
	return &AnnotationManager{
		client: client,
		logger: logger.NewDefault("annotation-manager"),
	}
}

// GetDeploymentState reads the current state from deployment annotations
func (am *AnnotationManager) GetDeploymentState(ctx context.Context, deploymentName, namespace string) (*DeploymentState, error) {
	deployment, err := am.client.GetClientset().AppsV1().Deployments(namespace).Get(
		ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}

	state := &DeploymentState{}

	// Get state annotation
	if deployment.Annotations != nil {
		if stateValue, exists := deployment.Annotations[StateAnnotation]; exists {
			state.State = stateValue
		}

		if reasonValue, exists := deployment.Annotations[ReasonAnnotation]; exists {
			state.Reason = reasonValue
		}

		if migratedAtValue, exists := deployment.Annotations[MigratedAtAnnotation]; exists {
			if parsedTime, err := time.Parse(time.RFC3339, migratedAtValue); err == nil {
				state.MigratedAt = parsedTime
			} else {
				am.logger.Warn("Failed to parse migrated-at timestamp",
					"deployment", deploymentName,
					"timestamp", migratedAtValue,
					"error", err)
			}
		}
	}

	// If no state annotation exists, try to detect from deployment spec
	if state.State == "" {
		detectedState, err := am.detectStateFromDeployment(deployment)
		if err != nil {
			am.logger.Warn("Failed to detect state from deployment spec",
				"deployment", deploymentName,
				"error", err)
			return state, nil
		}
		state.State = detectedState

		am.logger.Info("Detected state from deployment spec (no annotations found)",
			"deployment", deploymentName,
			"detected_state", detectedState)
	}

	return state, nil
}

// SetDeploymentState updates deployment annotations with new state
func (am *AnnotationManager) SetDeploymentState(ctx context.Context, deploymentName, namespace, state, reason string) error {
	now := time.Now().UTC()

	am.logger.Info("Setting deployment state annotations",
		"deployment", deploymentName,
		"namespace", namespace,
		"state", state,
		"reason", reason,
		"timestamp", now.Format(time.RFC3339))

	// Create patch operations for annotations
	patchOps := []map[string]interface{}{
		{
			"op":    "replace",
			"path":  "/metadata/annotations/" + escapeJSONPointer(StateAnnotation),
			"value": state,
		},
		{
			"op":    "replace",
			"path":  "/metadata/annotations/" + escapeJSONPointer(MigratedAtAnnotation),
			"value": now.Format(time.RFC3339),
		},
		{
			"op":    "replace",
			"path":  "/metadata/annotations/" + escapeJSONPointer(ReasonAnnotation),
			"value": reason,
		},
	}

	// Check if annotations exist, if not, create the annotations object first
	deployment, err := am.client.GetClientset().AppsV1().Deployments(namespace).Get(
		ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}

	if deployment.Annotations == nil {
		// Add annotations object first
		patchOps = []map[string]interface{}{
			{
				"op":    "add",
				"path":  "/metadata/annotations",
				"value": map[string]string{},
			},
		}

		// Apply the annotations object patch first
		patchBytes, err := json.Marshal(patchOps)
		if err != nil {
			return fmt.Errorf("failed to marshal annotations patch: %w", err)
		}

		_, err = am.client.GetClientset().AppsV1().Deployments(namespace).Patch(
			ctx, deploymentName, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to create annotations object: %w", err)
		}

		// Now create the actual annotation patches
		patchOps = []map[string]interface{}{
			{
				"op":    "add",
				"path":  "/metadata/annotations/" + escapeJSONPointer(StateAnnotation),
				"value": state,
			},
			{
				"op":    "add",
				"path":  "/metadata/annotations/" + escapeJSONPointer(MigratedAtAnnotation),
				"value": now.Format(time.RFC3339),
			},
			{
				"op":    "add",
				"path":  "/metadata/annotations/" + escapeJSONPointer(ReasonAnnotation),
				"value": reason,
			},
		}
	}

	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		return fmt.Errorf("failed to marshal state patch: %w", err)
	}

	_, err = am.client.GetClientset().AppsV1().Deployments(namespace).Patch(
		ctx, deploymentName, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to apply state annotations: %w", err)
	}

	am.logger.Info("Successfully updated deployment state annotations",
		"deployment", deploymentName,
		"state", state,
		"reason", reason)

	return nil
}

// detectStateFromDeployment attempts to detect current state from deployment spec
func (am *AnnotationManager) detectStateFromDeployment(deployment *appsv1.Deployment) (string, error) {
	// Check nodeSelector for Fargate
	if deployment.Spec.Template.Spec.NodeSelector != nil {
		if computeType, exists := deployment.Spec.Template.Spec.NodeSelector["eks.amazonaws.com/compute-type"]; exists && computeType == "fargate" {
			return StateFargate, nil
		}

		if capacityType, exists := deployment.Spec.Template.Spec.NodeSelector["capacity-type"]; exists && capacityType == "spot" {
			return StateSpot, nil
		}
	}

	// Check labels for compute-type
	if deployment.Spec.Template.Labels != nil {
		if computeType, exists := deployment.Spec.Template.Labels["compute-type"]; exists {
			switch computeType {
			case "fargate":
				return StateFargate, nil
			case "spot":
				return StateSpot, nil
			}
		}
	}

	// Check tolerations for spot
	if deployment.Spec.Template.Spec.Tolerations != nil {
		for _, toleration := range deployment.Spec.Template.Spec.Tolerations {
			if toleration.Key == "capacity-type" && toleration.Value == "spot" {
				return StateSpot, nil
			}
		}
	}

	return "", fmt.Errorf("unable to detect compute state from deployment spec")
}

// escapeJSONPointer escapes special characters for JSON Pointer (RFC 6901)
func escapeJSONPointer(s string) string {
	// Replace ~ with ~0 and / with ~1
	result := ""
	for _, char := range s {
		switch char {
		case '~':
			result += "~0"
		case '/':
			result += "~1"
		default:
			result += string(char)
		}
	}
	return result
}

// IsValidState checks if the given state is valid
func IsValidState(state string) bool {
	return state == StateSpot || state == StateFargate
}

// IsValidReason checks if the given reason is valid
func IsValidReason(reason string) bool {
	return reason == ReasonSpotInterruption || reason == ReasonManual || reason == ReasonRollback
}
