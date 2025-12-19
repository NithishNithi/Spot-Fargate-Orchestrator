package client

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"spot-fargate-orchestrator/internal/logger"
)

// K8sClient wraps the Kubernetes clientset with configuration detection
type K8sClient struct {
	clientset *kubernetes.Clientset
	namespace string
	logger    *logger.Logger
}

// NewK8sClient creates a new Kubernetes client with automatic configuration detection
func NewK8sClient(namespace string) (*K8sClient, error) {
	logger := logger.NewDefault("k8s-client")
	
	// Try in-cluster configuration first
	config, err := rest.InClusterConfig()
	if err != nil {
		logger.Debug("In-cluster configuration not available, trying out-of-cluster", 
			"error", err.Error())
		
		// Fall back to out-of-cluster configuration
		config, err = buildOutOfClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes configuration: %w", err)
		}
		logger.Info("Using out-of-cluster Kubernetes configuration")
	} else {
		logger.Info("Using in-cluster Kubernetes configuration")
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	logger.Info("Kubernetes client initialized successfully", 
		"namespace", namespace)

	return &K8sClient{
		clientset: clientset,
		namespace: namespace,
		logger:    logger,
	}, nil
}

// buildOutOfClusterConfig creates a Kubernetes configuration for out-of-cluster access
func buildOutOfClusterConfig() (*rest.Config, error) {
	// Try to get kubeconfig from KUBECONFIG environment variable
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		// Fall back to default location
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get user home directory: %w", err)
		}
		kubeconfigPath = filepath.Join(homeDir, ".kube", "config")
	}

	// Check if kubeconfig file exists
	if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("kubeconfig file not found at %s", kubeconfigPath)
	}

	// Build configuration from kubeconfig file
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to build config from kubeconfig: %w", err)
	}

	return config, nil
}

// GetClientset returns the Kubernetes clientset
func (k *K8sClient) GetClientset() *kubernetes.Clientset {
	return k.clientset
}

// GetNamespace returns the configured namespace
func (k *K8sClient) GetNamespace() string {
	return k.namespace
}

// SetNamespace updates the namespace for this client
func (k *K8sClient) SetNamespace(namespace string) {
	k.logger.Debug("Updating namespace", 
		"old_namespace", k.namespace, 
		"new_namespace", namespace)
	k.namespace = namespace
}

// IsReady checks if the Kubernetes client is ready to use
func (k *K8sClient) IsReady() bool {
	return k.clientset != nil
}
