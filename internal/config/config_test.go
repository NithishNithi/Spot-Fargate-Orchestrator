package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig_WithDefaults(t *testing.T) {
	// Clear environment variables
	clearEnvVars()

	// Set required environment variables
	os.Setenv("DEPLOYMENT_NAME", "test-deployment")
	os.Setenv("SERVICE_NAME", "test-service")
	defer clearEnvVars()

	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() failed: %v", err)
	}

	// Verify defaults
	if config.Namespace != "default" {
		t.Errorf("Expected Namespace to be 'default', got '%s'", config.Namespace)
	}

	if config.CheckInterval != 5*time.Second {
		t.Errorf("Expected CheckInterval to be 5s, got %v", config.CheckInterval)
	}

	if config.HealthCheckRetries != 3 {
		t.Errorf("Expected HealthCheckRetries to be 3, got %d", config.HealthCheckRetries)
	}

	if config.LogLevel != "info" {
		t.Errorf("Expected LogLevel to be 'info', got '%s'", config.LogLevel)
	}
}

func TestLoadConfig_WithCustomValues(t *testing.T) {
	// Clear environment variables
	clearEnvVars()

	// Set custom environment variables
	os.Setenv("NAMESPACE", "custom-namespace")
	os.Setenv("DEPLOYMENT_NAME", "custom-deployment")
	os.Setenv("SERVICE_NAME", "custom-service")
	os.Setenv("CHECK_INTERVAL", "10s")
	os.Setenv("HEALTH_CHECK_RETRIES", "5")
	os.Setenv("LOG_LEVEL", "debug")
	defer clearEnvVars()

	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() failed: %v", err)
	}

	// Verify custom values
	if config.Namespace != "custom-namespace" {
		t.Errorf("Expected Namespace to be 'custom-namespace', got '%s'", config.Namespace)
	}

	if config.DeploymentName != "custom-deployment" {
		t.Errorf("Expected DeploymentName to be 'custom-deployment', got '%s'", config.DeploymentName)
	}

	if config.ServiceName != "custom-service" {
		t.Errorf("Expected ServiceName to be 'custom-service', got '%s'", config.ServiceName)
	}

	if config.CheckInterval != 10*time.Second {
		t.Errorf("Expected CheckInterval to be 10s, got %v", config.CheckInterval)
	}

	if config.HealthCheckRetries != 5 {
		t.Errorf("Expected HealthCheckRetries to be 5, got %d", config.HealthCheckRetries)
	}

	if config.LogLevel != "debug" {
		t.Errorf("Expected LogLevel to be 'debug', got '%s'", config.LogLevel)
	}
}

func TestLoadConfig_MissingRequiredFields(t *testing.T) {
	// Clear environment variables
	clearEnvVars()
	defer clearEnvVars()

	// Test missing DEPLOYMENT_NAME
	os.Setenv("SERVICE_NAME", "test-service")
	_, err := LoadConfig()
	if err == nil {
		t.Error("Expected error when DEPLOYMENT_NAME is missing")
	}
	if !strings.Contains(err.Error(), "DEPLOYMENT_NAME is required") {
		t.Errorf("Expected error message about DEPLOYMENT_NAME, got: %v", err)
	}

	// Clear and test missing SERVICE_NAME
	clearEnvVars()
	os.Setenv("DEPLOYMENT_NAME", "test-deployment")
	_, err = LoadConfig()
	if err == nil {
		t.Error("Expected error when SERVICE_NAME is missing")
	}
	if !strings.Contains(err.Error(), "SERVICE_NAME is required") {
		t.Errorf("Expected error message about SERVICE_NAME, got: %v", err)
	}
}

func TestLoadConfig_InvalidValues(t *testing.T) {
	// Clear environment variables
	clearEnvVars()
	defer clearEnvVars()

	// Set required fields
	os.Setenv("DEPLOYMENT_NAME", "test-deployment")
	os.Setenv("SERVICE_NAME", "test-service")

	// Test invalid duration
	os.Setenv("CHECK_INTERVAL", "invalid-duration")
	_, err := LoadConfig()
	if err == nil {
		t.Error("Expected error for invalid CHECK_INTERVAL")
	}
	if !strings.Contains(err.Error(), "invalid CHECK_INTERVAL") {
		t.Errorf("Expected error message about CHECK_INTERVAL, got: %v", err)
	}

	// Test invalid integer
	os.Setenv("CHECK_INTERVAL", "5s") // Reset to valid value
	os.Setenv("HEALTH_CHECK_RETRIES", "not-a-number")
	_, err = LoadConfig()
	if err == nil {
		t.Error("Expected error for invalid HEALTH_CHECK_RETRIES")
	}
	if !strings.Contains(err.Error(), "invalid HEALTH_CHECK_RETRIES") {
		t.Errorf("Expected error message about HEALTH_CHECK_RETRIES, got: %v", err)
	}
}

func TestValidate_InvalidLogLevel(t *testing.T) {
	config := &Config{
		Namespace:           "default",
		DeploymentName:      "test-deployment",
		ServiceName:         "test-service",
		CheckInterval:       5 * time.Second,
		HealthCheckRetries:  3,
		HealthCheckInterval: 2 * time.Second,
		RolloutTimeout:      120 * time.Second,
		VerificationDelay:   5 * time.Second,
		SpotLabel:           "spot",
		FargateLabel:        "fargate",
		ComputeLabelKey:     "compute-type",
		LogLevel:            "invalid-level",
	}

	err := config.Validate()
	if err == nil {
		t.Error("Expected validation error for invalid log level")
	}
	if !strings.Contains(err.Error(), "LOG_LEVEL must be one of") {
		t.Errorf("Expected error message about LOG_LEVEL, got: %v", err)
	}
}

func TestValidate_NegativeValues(t *testing.T) {
	config := &Config{
		Namespace:           "default",
		DeploymentName:      "test-deployment",
		ServiceName:         "test-service",
		CheckInterval:       -1 * time.Second, // Invalid
		HealthCheckRetries:  -1,               // Invalid
		HealthCheckInterval: 2 * time.Second,
		RolloutTimeout:      120 * time.Second,
		VerificationDelay:   5 * time.Second,
		SpotLabel:           "spot",
		FargateLabel:        "fargate",
		ComputeLabelKey:     "compute-type",
		LogLevel:            "info",
	}

	err := config.Validate()
	if err == nil {
		t.Error("Expected validation error for negative values")
	}

	errorMsg := err.Error()
	if !strings.Contains(errorMsg, "CHECK_INTERVAL must be positive") {
		t.Error("Expected error about CHECK_INTERVAL")
	}
	if !strings.Contains(errorMsg, "HEALTH_CHECK_RETRIES must be non-negative") {
		t.Error("Expected error about HEALTH_CHECK_RETRIES")
	}
}

func TestValidate_EmptyLabels(t *testing.T) {
	config := &Config{
		Namespace:           "default",
		DeploymentName:      "test-deployment",
		ServiceName:         "test-service",
		CheckInterval:       5 * time.Second,
		HealthCheckRetries:  3,
		HealthCheckInterval: 2 * time.Second,
		RolloutTimeout:      120 * time.Second,
		VerificationDelay:   5 * time.Second,
		SpotLabel:           "", // Invalid
		FargateLabel:        "", // Invalid
		ComputeLabelKey:     "", // Invalid
		LogLevel:            "info",
	}

	err := config.Validate()
	if err == nil {
		t.Error("Expected validation error for empty labels")
	}

	errorMsg := err.Error()
	if !strings.Contains(errorMsg, "SPOT_LABEL cannot be empty") {
		t.Error("Expected error about SPOT_LABEL")
	}
	if !strings.Contains(errorMsg, "FARGATE_LABEL cannot be empty") {
		t.Error("Expected error about FARGATE_LABEL")
	}
	if !strings.Contains(errorMsg, "COMPUTE_LABEL_KEY cannot be empty") {
		t.Error("Expected error about COMPUTE_LABEL_KEY")
	}
	if !strings.Contains(errorMsg, "METADATA_ENDPOINT cannot be empty") {
		t.Error("Expected error about METADATA_ENDPOINT")
	}
}

func clearEnvVars() {
	envVars := []string{
		"NAMESPACE", "DEPLOYMENT_NAME", "SERVICE_NAME",
		"CHECK_INTERVAL", "HEALTH_CHECK_RETRIES", "HEALTH_CHECK_INTERVAL",
		"ROLLOUT_TIMEOUT", "VERIFICATION_DELAY",
		"SPOT_LABEL", "FARGATE_LABEL", "COMPUTE_LABEL_KEY",
		"METADATA_ENDPOINT", "METADATA_TIMEOUT",
		"LOG_LEVEL",
	}

	for _, envVar := range envVars {
		os.Unsetenv(envVar)
	}
}
