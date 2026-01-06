package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"spot-fargate-orchestrator/internal/api"
	"spot-fargate-orchestrator/internal/config"
	"spot-fargate-orchestrator/internal/kubernetes/client"
	"spot-fargate-orchestrator/internal/logger"
	"spot-fargate-orchestrator/internal/orchestrator"
)

// Build information - set by ldflags during build
var (
	Version   = "dev"
	BuildTime = "unknown"
	GoVersion = "unknown"
)

func main() {
	// Check for version flag
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("Spot Fargate Orchestrator\n")
		fmt.Printf("Version: %s\n", Version)
		fmt.Printf("Build Time: %s\n", BuildTime)
		fmt.Printf("Go Version: %s\n", GoVersion)
		os.Exit(0)
	}

	// Initialize temporary logger for startup (before config is loaded)
	// Check if LOG_FORMAT env var is set to use text format even before config load
	tempFormat := "json" // default
	if envFormat := os.Getenv("LOG_FORMAT"); envFormat != "" {
		tempFormat = envFormat
	}
	tempLogger := logger.NewWithFormat("main", logger.LevelInfo, tempFormat)
	tempLogger.Info("Spot Fargate Orchestrator starting...",
		"version", Version,
		"build_time", BuildTime,
		"go_version", GoVersion)

	// Load configuration from environment variables
	cfg, err := config.LoadConfig()
	if err != nil {
		tempLogger.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Re-initialize logger with config settings
	mainLogger := logger.NewWithFormat("main", logger.LogLevel(cfg.LogLevel), cfg.LogFormat)

	// Set global logger configuration for all components
	logger.SetGlobalConfig(logger.LogLevel(cfg.LogLevel), cfg.LogFormat)

	mainLogger.Info("Configuration loaded successfully",
		"namespace", cfg.Namespace,
		"deployment", cfg.DeploymentName,
		"service", cfg.ServiceName,
		"log_level", cfg.LogLevel,
		"log_format", cfg.LogFormat)

	// Initialize Kubernetes client once for both validation and orchestrator
	k8sClient, err := client.NewK8sClient(cfg.Namespace)
	if err != nil {
		mainLogger.Error("Failed to initialize Kubernetes client", "error", err)
		os.Exit(1)
	}

	// Perform startup validation - check target deployment (only for single mode)
	if cfg.IsSingleMode() {
		if err := validateTargetDeployment(cfg, k8sClient, mainLogger); err != nil {
			mainLogger.Error("Target deployment validation failed", "error", err)
			os.Exit(1)
		}
	} else {
		mainLogger.Info("Running in multi-deployment mode, skipping single deployment validation",
			"mode", cfg.Mode,
			"namespace", cfg.Namespace)
	}

	// Create context that listens for interrupt signals from the OS
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Initialize orchestrator with existing k8s client
	orch, err := orchestrator.NewOrchestrator(cfg, k8sClient)
	if err != nil {
		mainLogger.Error("Failed to initialize orchestrator", "error", err)
		os.Exit(1)
	}

	mainLogger.Info("Orchestrator initialized successfully, starting monitoring loop")

	// Start API server if enabled
	var apiServerDone chan error
	if cfg.APIEnabled {
		mainLogger.Info("Starting API server", "port", cfg.APIPort)
		apiServer := api.NewServer(cfg, k8sClient)
		apiServerDone = make(chan error, 1)
		go func() {
			apiServerDone <- apiServer.Start(ctx, cfg.APIPort)
		}()
	}

	// Start orchestrator in a separate goroutine
	orchestratorDone := make(chan error, 1)
	go func() {
		orchestratorDone <- orch.Start(ctx)
	}()

	// Wait for either interrupt signal or orchestrator completion
	select {
	case <-ctx.Done():
		mainLogger.Info("Received shutdown signal, initiating graceful shutdown")

		// Give the orchestrator a moment to clean up
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		select {
		case <-orchestratorDone:
			mainLogger.Info("Orchestrator stopped cleanly")
		case <-shutdownCtx.Done():
			mainLogger.Warn("Orchestrator shutdown timeout exceeded")
		}

	case err := <-orchestratorDone:
		if err != nil && err != context.Canceled {
			mainLogger.Error("Orchestrator stopped with error", "error", err)
			os.Exit(1)
		}
		mainLogger.Info("Orchestrator stopped normally")
	}

	mainLogger.Info("Spot Fargate Orchestrator shutdown complete")
}

// validateTargetDeployment performs startup validation to ensure the target
// deployment exists and has pods running on spot nodes (single mode only)
func validateTargetDeployment(cfg *config.Config, k8sClient *client.K8sClient, logger *logger.Logger) error {
	// Skip validation for local development
	if os.Getenv("SKIP_SPOT_VALIDATION") == "true" {
		logger.Info("Skipping target deployment validation (local development mode)")
		return nil
	}

	// This function should only be called for single mode
	if !cfg.IsSingleMode() {
		return fmt.Errorf("validateTargetDeployment called for non-single mode: %s", cfg.Mode)
	}

	logger.Info("Validating target deployment configuration (single mode)",
		"namespace", cfg.Namespace,
		"deployment", cfg.DeploymentName,
		"service", cfg.ServiceName)

	// Check if deployment exists
	deployment, err := k8sClient.GetClientset().AppsV1().Deployments(cfg.Namespace).Get(
		context.Background(), cfg.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get target deployment %s: %w", cfg.DeploymentName, err)
	}

	logger.Info("Target deployment found",
		"deployment", cfg.DeploymentName,
		"replicas", *deployment.Spec.Replicas,
		"ready_replicas", deployment.Status.ReadyReplicas)

	// Check if service exists
	_, err = k8sClient.GetClientset().CoreV1().Services(cfg.Namespace).Get(
		context.Background(), cfg.ServiceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get target service %s: %w", cfg.ServiceName, err)
	}

	logger.Info("Target service found", "service", cfg.ServiceName)

	// Check if deployment has pods running on spot nodes
	// Use the deployment's actual label selector instead of assuming app=deployment-name
	labelSelector := ""
	if deployment.Spec.Selector != nil && deployment.Spec.Selector.MatchLabels != nil {
		var selectorParts []string
		for key, value := range deployment.Spec.Selector.MatchLabels {
			selectorParts = append(selectorParts, fmt.Sprintf("%s=%s", key, value))
		}
		labelSelector = strings.Join(selectorParts, ",")
	} else {
		// Fallback to app=deployment-name if no selector found
		labelSelector = fmt.Sprintf("app=%s", cfg.DeploymentName)
	}

	pods, err := k8sClient.GetClientset().CoreV1().Pods(cfg.Namespace).List(
		context.Background(), metav1.ListOptions{
			LabelSelector: labelSelector,
		})
	if err != nil {
		return fmt.Errorf("failed to list pods for deployment %s: %w", cfg.DeploymentName, err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found for deployment %s", cfg.DeploymentName)
	}

	spotPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		// Check if pod is scheduled on a spot node
		if pod.Spec.NodeName != "" {
			node, err := k8sClient.GetClientset().CoreV1().Nodes().Get(
				context.Background(), pod.Spec.NodeName, metav1.GetOptions{})
			if err != nil {
				logger.Warn("Failed to get node information",
					"pod", pod.Name,
					"node", pod.Spec.NodeName,
					"error", err)
				continue
			}

			// Check node labels for spot instance indicators
			if isSpotNode(node) {
				spotPods++
				logger.Debug("Pod running on spot node",
					"pod", pod.Name,
					"node", pod.Spec.NodeName)
			}
		}
	}

	if spotPods == 0 {
		logger.Warn("No pods found running on spot nodes - orchestrator will monitor but may not detect interruptions")
	} else {
		logger.Info("Target deployment validation successful",
			"total_pods", len(pods.Items),
			"spot_pods", spotPods)
	}

	return nil
}

// isSpotNode checks if a node is a spot instance based on labels and taints
func isSpotNode(node *corev1.Node) bool {
	// Check for common spot node labels
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
	}

	return false
}
