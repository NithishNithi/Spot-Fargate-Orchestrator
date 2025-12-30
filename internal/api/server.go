package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"spot-fargate-orchestrator/internal/config"
	"spot-fargate-orchestrator/internal/kubernetes/client"
	"spot-fargate-orchestrator/internal/kubernetes/discovery"
	"spot-fargate-orchestrator/internal/logger"
)

// Server represents the HTTP API server
type Server struct {
	config           *config.Config
	k8sClient        *client.K8sClient
	discoveryService *discovery.DiscoveryService
	logger           *logger.Logger
	httpServer       *http.Server
}

// DeploymentInfo represents deployment information for API responses
type DeploymentInfo struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	State       string            `json:"state"`       // spot, fargate
	Enabled     bool              `json:"enabled"`     // sync enabled
	MigratedAt  *time.Time        `json:"migrated_at"` // last migration time
	Reason      string            `json:"reason"`      // migration reason
	Annotations map[string]string `json:"annotations"` // all orchestrator annotations
	Config      DeploymentConfig  `json:"config"`      // per-deployment config overrides
}

// DeploymentConfig represents per-deployment configuration overrides
type DeploymentConfig struct {
	RecoveryCooldown  string `json:"recovery_cooldown"`
	AllowFargate      bool   `json:"allow_fargate"`
	RecoveryEnabled   bool   `json:"recovery_enabled"`
	RolloutTimeout    string `json:"rollout_timeout"`
	VerificationDelay string `json:"verification_delay"`
}

// ListResponse represents the API response for listing deployments
type ListResponse struct {
	Deployments []DeploymentInfo `json:"deployments"`
	Total       int              `json:"total"`
	Enabled     int              `json:"enabled"`
	Namespace   string           `json:"namespace"`
	Timestamp   time.Time        `json:"timestamp"`
}

// NewServer creates a new API server
func NewServer(cfg *config.Config, k8sClient *client.K8sClient) *Server {
	logger := logger.NewDefault("api-server")

	// Initialize discovery service
	discoveryService := discovery.NewDiscoveryService(k8sClient, cfg)

	return &Server{
		config:           cfg,
		k8sClient:        k8sClient,
		discoveryService: discoveryService,
		logger:           logger,
	}
}

// Start starts the HTTP API server
func (s *Server) Start(ctx context.Context, port int) error {
	mux := http.NewServeMux()

	// Register API routes
	mux.HandleFunc("/api/v1/deployments", s.handleListDeployments)
	mux.HandleFunc("/api/v1/deployments/enabled", s.handleListEnabledDeployments)
	mux.HandleFunc("/api/v1/health", s.handleHealth)

	// Add CORS middleware
	handler := s.corsMiddleware(mux)

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: handler,
	}

	s.logger.Info("Starting API server", "port", port)

	// Start server in goroutine
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("API server failed", "error", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s.logger.Info("Shutting down API server")
	return s.httpServer.Shutdown(shutdownCtx)
}

// handleListDeployments handles GET /api/v1/deployments - lists all deployments in namespace
func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.logger.Info("API request received", "endpoint", "/api/v1/deployments", "method", r.Method)

	deployments, err := s.getAllDeployments(r.Context())
	if err != nil {
		s.logger.Error("Failed to get deployments", "error", err)
		http.Error(w, fmt.Sprintf("Failed to get deployments: %v", err), http.StatusInternalServerError)
		return
	}

	enabledCount := 0
	for _, dep := range deployments {
		if dep.Enabled {
			enabledCount++
		}
	}

	response := ListResponse{
		Deployments: deployments,
		Total:       len(deployments),
		Enabled:     enabledCount,
		Namespace:   s.config.Namespace,
		Timestamp:   time.Now().UTC(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error("Failed to encode response", "error", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}

	s.logger.Info("API request completed", "endpoint", "/api/v1/deployments", "total", len(deployments), "enabled", enabledCount)
}

// handleListEnabledDeployments handles GET /api/v1/deployments/enabled - lists only sync-enabled deployments
func (s *Server) handleListEnabledDeployments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.logger.Info("API request received", "endpoint", "/api/v1/deployments/enabled", "method", r.Method)

	allDeployments, err := s.getAllDeployments(r.Context())
	if err != nil {
		s.logger.Error("Failed to get deployments", "error", err)
		http.Error(w, fmt.Sprintf("Failed to get deployments: %v", err), http.StatusInternalServerError)
		return
	}

	// Filter only enabled deployments
	var enabledDeployments []DeploymentInfo
	for _, dep := range allDeployments {
		if dep.Enabled {
			enabledDeployments = append(enabledDeployments, dep)
		}
	}

	response := ListResponse{
		Deployments: enabledDeployments,
		Total:       len(allDeployments),
		Enabled:     len(enabledDeployments),
		Namespace:   s.config.Namespace,
		Timestamp:   time.Now().UTC(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error("Failed to encode response", "error", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}

	s.logger.Info("API request completed", "endpoint", "/api/v1/deployments/enabled", "total", len(allDeployments), "enabled", len(enabledDeployments))
}

// handleHealth handles GET /api/v1/health - health check endpoint
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().UTC(),
		"namespace": s.config.Namespace,
		"mode":      s.config.Mode,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(health); err != nil {
		s.logger.Error("Failed to encode health response", "error", err)
		http.Error(w, "Failed to encode health response", http.StatusInternalServerError)
		return
	}

}

// corsMiddleware adds CORS headers
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// getAllDeployments gets all deployments in the namespace with their orchestrator status
func (s *Server) getAllDeployments(ctx context.Context) ([]DeploymentInfo, error) {
	// Get all deployments in namespace
	deploymentList, err := s.k8sClient.GetClientset().AppsV1().Deployments(s.config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}

	var deployments []DeploymentInfo

	for _, deployment := range deploymentList.Items {
		depInfo := DeploymentInfo{
			Name:        deployment.Name,
			Namespace:   deployment.Namespace,
			Annotations: make(map[string]string),
		}

		// Check if sync is enabled
		if deployment.Annotations != nil {
			if enabled, exists := deployment.Annotations[s.config.OptInAnnotation]; exists && enabled == "true" {
				depInfo.Enabled = true
			}

			// Extract orchestrator annotations
			for key, value := range deployment.Annotations {
				if strings.HasPrefix(key, "spot-orchestrator") {
					depInfo.Annotations[key] = value
				}
			}

			// Parse state and migration info
			if state, exists := deployment.Annotations["spot-orchestrator/state"]; exists {
				depInfo.State = state
			} else {
				// Detect current state from deployment spec
				depInfo.State = s.detectCurrentState(&deployment)
			}

			if reason, exists := deployment.Annotations["spot-orchestrator/reason"]; exists {
				depInfo.Reason = reason
			}

			if migratedAtStr, exists := deployment.Annotations["spot-orchestrator/migrated-at"]; exists {
				if migratedAt, parseErr := time.Parse(time.RFC3339, migratedAtStr); parseErr == nil {
					depInfo.MigratedAt = &migratedAt
				}
			}

			// Parse per-deployment configuration
			depInfo.Config = s.parseDeploymentConfig(&deployment)
		}

		// Default state if not set
		if depInfo.State == "" {
			depInfo.State = s.detectCurrentState(&deployment)
		}

		deployments = append(deployments, depInfo)
	}

	return deployments, nil
}

// detectCurrentState detects current compute state from deployment spec
func (s *Server) detectCurrentState(deployment *appsv1.Deployment) string {
	// Check pod template labels
	if deployment.Spec.Template.Labels != nil {
		if computeType, exists := deployment.Spec.Template.Labels[s.config.ComputeLabelKey]; exists {
			return computeType
		}
	}

	// Check nodeSelector for Fargate
	if deployment.Spec.Template.Spec.NodeSelector != nil {
		if computeType, exists := deployment.Spec.Template.Spec.NodeSelector["eks.amazonaws.com/compute-type"]; exists && computeType == "fargate" {
			return s.config.FargateLabel
		}
	}

	// Default to spot
	return s.config.SpotLabel
}

// parseDeploymentConfig parses per-deployment configuration from annotations
func (s *Server) parseDeploymentConfig(deployment *appsv1.Deployment) DeploymentConfig {
	config := DeploymentConfig{
		// Set defaults from global config
		RecoveryCooldown:  s.config.RecoveryCooldown.String(),
		AllowFargate:      true, // Default: allow Fargate
		RecoveryEnabled:   s.config.RecoveryEnabled,
		RolloutTimeout:    s.config.RolloutTimeout.String(),
		VerificationDelay: s.config.VerificationDelay.String(),
	}

	if deployment.Annotations == nil {
		return config
	}

	// Parse overrides from annotations
	if cooldown, exists := deployment.Annotations["spot-orchestrator/recovery-cooldown"]; exists {
		config.RecoveryCooldown = cooldown
	}

	if allowFargateStr, exists := deployment.Annotations["spot-orchestrator/allow-fargate"]; exists {
		if allowFargate, err := strconv.ParseBool(allowFargateStr); err == nil {
			config.AllowFargate = allowFargate
		}
	}

	if recoveryEnabledStr, exists := deployment.Annotations["spot-orchestrator/recovery-enabled"]; exists {
		if recoveryEnabled, err := strconv.ParseBool(recoveryEnabledStr); err == nil {
			config.RecoveryEnabled = recoveryEnabled
		}
	}

	if timeout, exists := deployment.Annotations["spot-orchestrator/rollout-timeout"]; exists {
		config.RolloutTimeout = timeout
	}

	if delay, exists := deployment.Annotations["spot-orchestrator/verification-delay"]; exists {
		config.VerificationDelay = delay
	}

	return config
}
