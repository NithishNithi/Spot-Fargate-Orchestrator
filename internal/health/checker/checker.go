package checker

import (
	"fmt"
	"net/http"
	"time"

	"spot-fargate-orchestrator/internal/logger"
)

// HealthCheckResult represents the result of a health check
type HealthCheckResult struct {
	Healthy      bool          `json:"healthy"`
	StatusCode   int           `json:"status_code"`
	ResponseTime time.Duration `json:"response_time"`
	Error        error         `json:"error,omitempty"`
}

// HealthChecker performs HTTP health checks
type HealthChecker struct {
	httpClient *http.Client
	retries    int
	interval   time.Duration
	logger     *logger.Logger
}

// NewHealthChecker creates a new health checker
func NewHealthChecker(retries int, interval time.Duration) *HealthChecker {
	return &HealthChecker{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		retries:  retries,
		interval: interval,
		logger:   logger.NewDefault("health-checker"),
	}
}

// CheckPodHealth checks the health of a specific pod
func (h *HealthChecker) CheckPodHealth(podIP string, port int, path string) (*HealthCheckResult, error) {
	url := fmt.Sprintf("http://%s:%d%s", podIP, port, path)

	h.logger.Debug("Starting pod health check",
		"pod_ip", podIP,
		"port", port,
		"path", path,
		"url", url,
		"retries", h.retries,
	)

	return h.performHealthCheck(url, "pod", podIP)
}

// CheckServiceHealth checks the health of a Kubernetes service
func (h *HealthChecker) CheckServiceHealth(serviceName, namespace string, port int, path string) (*HealthCheckResult, error) {
	// Use Kubernetes DNS naming convention for services
	url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", serviceName, namespace, port, path)

	h.logger.Debug("Starting service health check",
		"service_name", serviceName,
		"namespace", namespace,
		"port", port,
		"path", path,
		"url", url,
		"retries", h.retries,
	)

	return h.performHealthCheck(url, "service", fmt.Sprintf("%s.%s", serviceName, namespace))
}

// performHealthCheck performs the actual HTTP health check with retry logic
func (h *HealthChecker) performHealthCheck(url, targetType, targetName string) (*HealthCheckResult, error) {
	var lastResult *HealthCheckResult
	var lastErr error

	for attempt := 0; attempt <= h.retries; attempt++ {
		if attempt > 0 {
			h.logger.Debug("Retrying health check",
				"target_type", targetType,
				"target_name", targetName,
				"attempt", attempt,
				"max_retries", h.retries,
			)
			time.Sleep(h.interval)
		}

		result, err := h.singleHealthCheck(url)
		lastResult = result
		lastErr = err

		// If the check was successful (healthy), return immediately
		if err == nil && result.Healthy {
			h.logger.Debug("Health check successful",
				"target_type", targetType,
				"target_name", targetName,
				"attempt", attempt,
				"status_code", result.StatusCode,
				"response_time", result.ResponseTime,
			)
			return result, nil
		}

		// Log the failed attempt
		if err != nil {
			h.logger.Warn("Health check attempt failed",
				"target_type", targetType,
				"target_name", targetName,
				"attempt", attempt,
				"error", err.Error(),
			)
		} else {
			h.logger.Warn("Health check returned unhealthy status",
				"target_type", targetType,
				"target_name", targetName,
				"attempt", attempt,
				"status_code", result.StatusCode,
				"response_time", result.ResponseTime,
			)
		}
	}

	// All retries exhausted
	h.logger.Error("Health check failed after all retries",
		"target_type", targetType,
		"target_name", targetName,
		"total_attempts", h.retries+1,
		"final_status_code", lastResult.StatusCode,
		"final_error", lastErr,
	)

	return lastResult, lastErr
}

// singleHealthCheck performs a single HTTP health check
func (h *HealthChecker) singleHealthCheck(url string) (*HealthCheckResult, error) {
	start := time.Now()

	resp, err := h.httpClient.Get(url)
	responseTime := time.Since(start)

	result := &HealthCheckResult{
		ResponseTime: responseTime,
	}

	if err != nil {
		result.Error = err
		result.Healthy = false
		return result, err
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode

	// Consider status codes 200-299 as healthy (Requirements 4.3)
	result.Healthy = resp.StatusCode >= 200 && resp.StatusCode < 300

	if !result.Healthy {
		result.Error = fmt.Errorf("unhealthy status code: %d", resp.StatusCode)
		return result, result.Error
	}

	return result, nil
}
