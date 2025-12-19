package discovery

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"spot-fargate-orchestrator/internal/config"
	"spot-fargate-orchestrator/internal/kubernetes/client"
	"spot-fargate-orchestrator/internal/logger"
)

// DeploymentInfo represents a discovered deployment
type DeploymentInfo struct {
	Name      string
	Namespace string
	Service   string
	Labels    map[string]string
}

// DiscoveryService handles deployment discovery
type DiscoveryService struct {
	client *client.K8sClient
	config *config.Config
	logger *logger.Logger
}

// NewDiscoveryService creates a new discovery service
func NewDiscoveryService(client *client.K8sClient, config *config.Config) *DiscoveryService {
	return &DiscoveryService{
		client: client,
		config: config,
		logger: logger.NewDefault("discovery"),
	}
}

// DiscoverManagedDeployments finds all deployments that should be managed
func (d *DiscoveryService) DiscoverManagedDeployments(ctx context.Context) ([]DeploymentInfo, error) {
	if d.config.IsSingleMode() {
		return d.getSingleDeployment(ctx)
	}

	if d.config.IsNamespaceMode() {
		return d.getNamespaceDeployments(ctx)
	}

	return nil, fmt.Errorf("unsupported mode: %s", d.config.Mode)
}

// getSingleDeployment returns the single configured deployment
func (d *DiscoveryService) getSingleDeployment(ctx context.Context) ([]DeploymentInfo, error) {
	d.logger.Debug("Using single deployment mode",
		"deployment", d.config.DeploymentName,
		"service", d.config.ServiceName)

	// Verify the deployment exists
	deployment, err := d.client.GetClientset().AppsV1().Deployments(d.config.Namespace).Get(
		ctx, d.config.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get single deployment %s: %w", d.config.DeploymentName, err)
	}

	return []DeploymentInfo{
		{
			Name:      d.config.DeploymentName,
			Namespace: d.config.Namespace,
			Service:   d.config.ServiceName,
			Labels:    deployment.Labels,
		},
	}, nil
}

// getNamespaceDeployments discovers deployments in the namespace with opt-in annotation
func (d *DiscoveryService) getNamespaceDeployments(ctx context.Context) ([]DeploymentInfo, error) {
	d.logger.Debug("Discovering deployments in namespace mode",
		"namespace", d.config.Namespace,
		"opt_in_annotation", d.config.OptInAnnotation)

	// List all deployments in the namespace
	deployments, err := d.client.GetClientset().AppsV1().Deployments(d.config.Namespace).List(
		ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments in namespace %s: %w", d.config.Namespace, err)
	}

	var managedDeployments []DeploymentInfo

	for _, deployment := range deployments.Items {
		// Check if deployment has opt-in annotation
		if !d.hasOptInAnnotation(&deployment) {
			d.logger.Debug("Skipping deployment without opt-in annotation",
				"deployment", deployment.Name)
			continue
		}

		// Find the corresponding service
		serviceName, err := d.findServiceForDeployment(ctx, &deployment)
		if err != nil {
			d.logger.Warn("Failed to find service for deployment, skipping",
				"deployment", deployment.Name,
				"error", err)
			continue
		}

		managedDeployments = append(managedDeployments, DeploymentInfo{
			Name:      deployment.Name,
			Namespace: deployment.Namespace,
			Service:   serviceName,
			Labels:    deployment.Labels,
		})

		d.logger.Debug("Discovered managed deployment",
			"deployment", deployment.Name,
			"service", serviceName)
	}

	d.logger.Info("Deployment discovery completed",
		"namespace", d.config.Namespace,
		"total_deployments", len(deployments.Items),
		"managed_deployments", len(managedDeployments))

	return managedDeployments, nil
}

// hasOptInAnnotation checks if deployment has the opt-in annotation
func (d *DiscoveryService) hasOptInAnnotation(deployment *appsv1.Deployment) bool {
	if deployment.Annotations == nil {
		return false
	}

	value, exists := deployment.Annotations[d.config.OptInAnnotation]
	if !exists {
		return false
	}

	// Accept "true", "yes", "1" as valid opt-in values
	normalizedValue := strings.ToLower(strings.TrimSpace(value))
	return normalizedValue == "true" || normalizedValue == "yes" || normalizedValue == "1"
}

// findServiceForDeployment finds the service that matches the deployment
func (d *DiscoveryService) findServiceForDeployment(ctx context.Context, deployment *appsv1.Deployment) (string, error) {
	// Get the label value that we'll use to match the service
	if deployment.Spec.Selector == nil || deployment.Spec.Selector.MatchLabels == nil {
		return "", fmt.Errorf("deployment %s has no selector labels", deployment.Name)
	}

	// Get the value of the service label selector (e.g., "app" label)
	labelValue, exists := deployment.Spec.Selector.MatchLabels[d.config.ServiceLabelSelector]
	if !exists {
		return "", fmt.Errorf("deployment %s does not have label %s", deployment.Name, d.config.ServiceLabelSelector)
	}

	// List services in the namespace
	services, err := d.client.GetClientset().CoreV1().Services(deployment.Namespace).List(
		ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list services: %w", err)
	}

	// Find service with matching selector
	for _, service := range services.Items {
		if service.Spec.Selector == nil {
			continue
		}

		// Check if service selector matches the deployment label
		if serviceValue, exists := service.Spec.Selector[d.config.ServiceLabelSelector]; exists {
			if serviceValue == labelValue {
				d.logger.Debug("Found matching service",
					"deployment", deployment.Name,
					"service", service.Name,
					"label_selector", d.config.ServiceLabelSelector,
					"label_value", labelValue)
				return service.Name, nil
			}
		}
	}

	return "", fmt.Errorf("no service found with selector %s=%s for deployment %s",
		d.config.ServiceLabelSelector, labelValue, deployment.Name)
}

// GetDeploymentByName returns a specific deployment info by name
func (d *DiscoveryService) GetDeploymentByName(ctx context.Context, deploymentName string) (*DeploymentInfo, error) {
	deployments, err := d.DiscoverManagedDeployments(ctx)
	if err != nil {
		return nil, err
	}

	for _, deployment := range deployments {
		if deployment.Name == deploymentName {
			return &deployment, nil
		}
	}

	return nil, fmt.Errorf("deployment %s not found in managed deployments", deploymentName)
}
