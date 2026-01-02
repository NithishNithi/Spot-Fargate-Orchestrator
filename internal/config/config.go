package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config holds all configuration parameters for the orchestrator
type Config struct {
	// Kubernetes Configuration
	Namespace string `env:"NAMESPACE" default:"default"`

	// Multi-deployment mode configuration
	Mode                 string `env:"MODE" default:"single"`                                 // single | namespace | cluster
	OptInAnnotation      string `env:"OPT_IN_ANNOTATION" default:"spot-orchestrator/enabled"` // Annotation for opt-in
	ServiceLabelSelector string `env:"SERVICE_LABEL_SELECTOR" default:"app"`                  // Label to match deployment to service

	// Single deployment mode (backward compatibility)
	DeploymentName string `env:"DEPLOYMENT_NAME"` // Required only in single mode
	ServiceName    string `env:"SERVICE_NAME"`    // Required only in single mode

	// Monitoring Configuration
	CheckInterval       time.Duration `env:"CHECK_INTERVAL" default:"5s"`
	HealthCheckRetries  int           `env:"HEALTH_CHECK_RETRIES" default:"3"`
	HealthCheckInterval time.Duration `env:"HEALTH_CHECK_INTERVAL" default:"2s"`

	// Migration Configuration
	RolloutTimeout    time.Duration `env:"ROLLOUT_TIMEOUT" default:"120s"`
	VerificationDelay time.Duration `env:"VERIFICATION_DELAY" default:"5s"`

	// Recovery Configuration
	RecoveryEnabled     bool          `env:"RECOVERY_ENABLED" default:"true"`
	RecoveryInterval    time.Duration `env:"RECOVERY_INTERVAL" default:"5m"`
	RecoveryCooldown    time.Duration `env:"RECOVERY_COOLDOWN" default:"45m"`
	RecoveryTimeout     time.Duration `env:"RECOVERY_TIMEOUT" default:"3m"`
	RecoveryBackoffBase time.Duration `env:"RECOVERY_BACKOFF_BASE" default:"15m"`
	RecoveryMaxBackoff  time.Duration `env:"RECOVERY_MAX_BACKOFF" default:"4h"`

	// Compute Configuration
	SpotLabel       string `env:"SPOT_LABEL" default:"spot"`
	FargateLabel    string `env:"FARGATE_LABEL" default:"fargate"`
	ComputeLabelKey string `env:"COMPUTE_LABEL_KEY" default:"compute-type"`

	// AWS Configuration
	AWSRegion   string `env:"AWS_REGION" default:"us-east-1"`
	SQSQueueURL string `env:"SQS_QUEUE_URL" required:"true"`

	// Alerting Configuration
	SlackWebhookURL string `env:"SLACK_WEBHOOK_URL"`
	AlertsEnabled   bool   `env:"ALERTS_ENABLED" default:"true"`

	// Logging Configuration
	LogLevel string `env:"LOG_LEVEL" default:"info"`

	// API Server Configuration
	APIEnabled bool `env:"API_ENABLED" default:"false"`
	APIPort    int  `env:"API_PORT" default:"8080"`

	// Optimization Configuration
	RateLimitingEnabled    bool          `env:"RATE_LIMITING_ENABLED" default:"false"`
	RateLimitingQPS        float64       `env:"RATE_LIMITING_QPS" default:"20.0"`
	RateLimitingBurst      int           `env:"RATE_LIMITING_BURST" default:"50"`
	CachingEnabled         bool          `env:"CACHING_ENABLED" default:"false"`
	CachingDeploymentTTL   time.Duration `env:"CACHING_DEPLOYMENT_TTL" default:"2m"`
	BatchProcessingEnabled bool          `env:"BATCH_PROCESSING_ENABLED" default:"false"`
	BatchSize              int           `env:"BATCH_SIZE" default:"10"`
	BatchTimeout           time.Duration `env:"BATCH_TIMEOUT" default:"30s"`
}

// TOMLConfig represents the TOML file structure
type TOMLConfig struct {
	Kubernetes struct {
		Namespace            string `toml:"namespace"`
		Mode                 string `toml:"mode"`
		OptInAnnotation      string `toml:"opt_in_annotation"`
		ServiceLabelSelector string `toml:"service_label_selector"`
		// Single mode backward compatibility
		DeploymentName string `toml:"deployment_name"`
		ServiceName    string `toml:"service_name"`
	} `toml:"kubernetes"`

	Monitoring struct {
		CheckInterval       string `toml:"check_interval"`
		HealthCheckRetries  int    `toml:"health_check_retries"`
		HealthCheckInterval string `toml:"health_check_interval"`
	} `toml:"monitoring"`

	Migration struct {
		RolloutTimeout    string `toml:"rollout_timeout"`
		VerificationDelay string `toml:"verification_delay"`
	} `toml:"migration"`

	Recovery struct {
		Enabled     bool   `toml:"enabled"`
		Interval    string `toml:"interval"`
		Cooldown    string `toml:"cooldown"`
		Timeout     string `toml:"timeout"`
		BackoffBase string `toml:"backoff_base"`
		MaxBackoff  string `toml:"max_backoff"`
	} `toml:"recovery"`

	Compute struct {
		SpotLabel       string `toml:"spot_label"`
		FargateLabel    string `toml:"fargate_label"`
		ComputeLabelKey string `toml:"compute_label_key"`
	} `toml:"compute"`

	AWS struct {
		Region      string `toml:"region"`
		SQSQueueURL string `toml:"sqs_queue_url"`
	} `toml:"aws"`

	Alerts struct {
		Enabled         bool   `toml:"enabled"`
		SlackWebhookURL string `toml:"slack_webhook_url"`
	} `toml:"alerts"`

	Logging struct {
		Level  string `toml:"level"`
		Format string `toml:"format"`
	} `toml:"logging"`

	API struct {
		Enabled bool `toml:"enabled"`
		Port    int  `toml:"port"`
	} `toml:"api"`

	// Optimization Configuration
	RateLimiting struct {
		Enabled bool    `toml:"enabled"`
		QPS     float64 `toml:"qps"`
		Burst   int     `toml:"burst"`
		Timeout string  `toml:"timeout"`
	} `toml:"rate_limiting"`

	Caching struct {
		Enabled       bool   `toml:"enabled"`
		DeploymentTTL string `toml:"deployment_ttl"`
		NodeTTL       string `toml:"node_ttl"`
		ServiceTTL    string `toml:"service_ttl"`
	} `toml:"caching"`

	BatchProcessing struct {
		Enabled              bool   `toml:"enabled"`
		BatchSize            int    `toml:"batch_size"`
		BatchTimeout         string `toml:"batch_timeout"`
		MaxConcurrentBatches int    `toml:"max_concurrent_batches"`
	} `toml:"batch_processing"`
}

// LoadConfig loads configuration from TOML file with environment variable fallback
func LoadConfig() (*Config, error) {
	config := &Config{}

	// Try to load from TOML file first
	tomlConfig, err := loadTOMLConfig("config.toml")
	if err != nil {
		// If TOML file doesn't exist or can't be parsed, use defaults
		fmt.Printf("TOML config not found or invalid (%v), using environment variables and defaults\n", err)
		tomlConfig = &TOMLConfig{} // Use empty TOML config (will use defaults)
	}

	// Load configuration with TOML defaults and environment variable overrides
	config.Namespace = getEnvOrTOMLOrDefault("NAMESPACE", tomlConfig.Kubernetes.Namespace, "default")
	config.Mode = getEnvOrTOMLOrDefault("MODE", tomlConfig.Kubernetes.Mode, "single")
	config.OptInAnnotation = getEnvOrTOMLOrDefault("OPT_IN_ANNOTATION", tomlConfig.Kubernetes.OptInAnnotation, "spot-orchestrator/enabled")
	config.ServiceLabelSelector = getEnvOrTOMLOrDefault("SERVICE_LABEL_SELECTOR", tomlConfig.Kubernetes.ServiceLabelSelector, "app")

	// Single mode backward compatibility
	config.DeploymentName = getEnvOrTOMLOrDefault("DEPLOYMENT_NAME", tomlConfig.Kubernetes.DeploymentName, "")
	config.ServiceName = getEnvOrTOMLOrDefault("SERVICE_NAME", tomlConfig.Kubernetes.ServiceName, "")
	config.SpotLabel = getEnvOrTOMLOrDefault("SPOT_LABEL", tomlConfig.Compute.SpotLabel, "spot")
	config.FargateLabel = getEnvOrTOMLOrDefault("FARGATE_LABEL", tomlConfig.Compute.FargateLabel, "fargate")
	config.ComputeLabelKey = getEnvOrTOMLOrDefault("COMPUTE_LABEL_KEY", tomlConfig.Compute.ComputeLabelKey, "compute-type")
	config.AWSRegion = getEnvOrTOMLOrDefault("AWS_REGION", tomlConfig.AWS.Region, "us-east-1")
	config.SQSQueueURL = getEnvOrTOMLOrDefault("SQS_QUEUE_URL", tomlConfig.AWS.SQSQueueURL, "")
	config.SlackWebhookURL = getEnvOrTOMLOrDefault("SLACK_WEBHOOK_URL", tomlConfig.Alerts.SlackWebhookURL, "")
	config.LogLevel = getEnvOrTOMLOrDefault("LOG_LEVEL", tomlConfig.Logging.Level, "info")

	// Load API configuration
	config.APIEnabled, err = getEnvAsBoolOrTOMLOrDefault("API_ENABLED", tomlConfig.API.Enabled, false)
	if err != nil {
		return nil, fmt.Errorf("invalid API_ENABLED: %w", err)
	}

	config.APIPort, err = getEnvAsIntOrTOMLOrDefault("API_PORT", tomlConfig.API.Port, 8080)
	if err != nil {
		return nil, fmt.Errorf("invalid API_PORT: %w", err)
	}

	// Load optimization configuration
	config.RateLimitingEnabled, err = getEnvAsBoolOrTOMLOrDefault("RATE_LIMITING_ENABLED", tomlConfig.RateLimiting.Enabled, false)
	if err != nil {
		return nil, fmt.Errorf("invalid RATE_LIMITING_ENABLED: %w", err)
	}

	config.RateLimitingQPS, err = getEnvAsFloatOrTOMLOrDefault("RATE_LIMITING_QPS", tomlConfig.RateLimiting.QPS, 20.0)
	if err != nil {
		return nil, fmt.Errorf("invalid RATE_LIMITING_QPS: %w", err)
	}

	config.RateLimitingBurst, err = getEnvAsIntOrTOMLOrDefault("RATE_LIMITING_BURST", tomlConfig.RateLimiting.Burst, 50)
	if err != nil {
		return nil, fmt.Errorf("invalid RATE_LIMITING_BURST: %w", err)
	}

	config.CachingEnabled, err = getEnvAsBoolOrTOMLOrDefault("CACHING_ENABLED", tomlConfig.Caching.Enabled, false)
	if err != nil {
		return nil, fmt.Errorf("invalid CACHING_ENABLED: %w", err)
	}

	config.CachingDeploymentTTL, err = getEnvAsDurationOrTOMLOrDefault("CACHING_DEPLOYMENT_TTL", tomlConfig.Caching.DeploymentTTL, 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("invalid CACHING_DEPLOYMENT_TTL: %w", err)
	}

	config.BatchProcessingEnabled, err = getEnvAsBoolOrTOMLOrDefault("BATCH_PROCESSING_ENABLED", tomlConfig.BatchProcessing.Enabled, false)
	if err != nil {
		return nil, fmt.Errorf("invalid BATCH_PROCESSING_ENABLED: %w", err)
	}

	config.BatchSize, err = getEnvAsIntOrTOMLOrDefault("BATCH_SIZE", tomlConfig.BatchProcessing.BatchSize, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid BATCH_SIZE: %w", err)
	}

	config.BatchTimeout, err = getEnvAsDurationOrTOMLOrDefault("BATCH_TIMEOUT", tomlConfig.BatchProcessing.BatchTimeout, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid BATCH_TIMEOUT: %w", err)
	}

	// Load boolean fields
	config.AlertsEnabled, err = getEnvAsBoolOrTOMLOrDefault("ALERTS_ENABLED", tomlConfig.Alerts.Enabled, true)
	if err != nil {
		return nil, fmt.Errorf("invalid ALERTS_ENABLED: %w", err)
	}

	config.RecoveryEnabled, err = getEnvAsBoolOrTOMLOrDefault("RECOVERY_ENABLED", tomlConfig.Recovery.Enabled, true)
	if err != nil {
		return nil, fmt.Errorf("invalid RECOVERY_ENABLED: %w", err)
	}

	// Load integer fields
	config.HealthCheckRetries, err = getEnvAsIntOrTOMLOrDefault("HEALTH_CHECK_RETRIES", tomlConfig.Monitoring.HealthCheckRetries, 3)
	if err != nil {
		return nil, fmt.Errorf("invalid HEALTH_CHECK_RETRIES: %w", err)
	}

	// Load duration fields
	config.CheckInterval, err = getEnvAsDurationOrTOMLOrDefault("CHECK_INTERVAL", tomlConfig.Monitoring.CheckInterval, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid CHECK_INTERVAL: %w", err)
	}

	config.HealthCheckInterval, err = getEnvAsDurationOrTOMLOrDefault("HEALTH_CHECK_INTERVAL", tomlConfig.Monitoring.HealthCheckInterval, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid HEALTH_CHECK_INTERVAL: %w", err)
	}

	config.RolloutTimeout, err = getEnvAsDurationOrTOMLOrDefault("ROLLOUT_TIMEOUT", tomlConfig.Migration.RolloutTimeout, 120*time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid ROLLOUT_TIMEOUT: %w", err)
	}

	config.VerificationDelay, err = getEnvAsDurationOrTOMLOrDefault("VERIFICATION_DELAY", tomlConfig.Migration.VerificationDelay, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid VERIFICATION_DELAY: %w", err)
	}

	// Load recovery duration fields
	config.RecoveryInterval, err = getEnvAsDurationOrTOMLOrDefault("RECOVERY_INTERVAL", tomlConfig.Recovery.Interval, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("invalid RECOVERY_INTERVAL: %w", err)
	}

	config.RecoveryCooldown, err = getEnvAsDurationOrTOMLOrDefault("RECOVERY_COOLDOWN", tomlConfig.Recovery.Cooldown, 45*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("invalid RECOVERY_COOLDOWN: %w", err)
	}

	config.RecoveryTimeout, err = getEnvAsDurationOrTOMLOrDefault("RECOVERY_TIMEOUT", tomlConfig.Recovery.Timeout, 3*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("invalid RECOVERY_TIMEOUT: %w", err)
	}

	config.RecoveryBackoffBase, err = getEnvAsDurationOrTOMLOrDefault("RECOVERY_BACKOFF_BASE", tomlConfig.Recovery.BackoffBase, 15*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("invalid RECOVERY_BACKOFF_BASE: %w", err)
	}

	config.RecoveryMaxBackoff, err = getEnvAsDurationOrTOMLOrDefault("RECOVERY_MAX_BACKOFF", tomlConfig.Recovery.MaxBackoff, 4*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("invalid RECOVERY_MAX_BACKOFF: %w", err)
	}

	// Validate the configuration
	if err := config.Validate(); err != nil {
		return nil, err
	}

	return config, nil
}

// loadTOMLConfig loads configuration from a TOML file
func loadTOMLConfig(filename string) (*TOMLConfig, error) {
	var config TOMLConfig

	// Check if file exists
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return nil, fmt.Errorf("TOML config file %s does not exist", filename)
	}

	// Decode TOML file
	if _, err := toml.DecodeFile(filename, &config); err != nil {
		return nil, fmt.Errorf("failed to decode TOML config file %s: %w", filename, err)
	}

	return &config, nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	var errors []string

	// Validate mode
	validModes := []string{"single", "namespace", "cluster"}
	modeValid := false
	for _, mode := range validModes {
		if strings.ToLower(c.Mode) == mode {
			modeValid = true
			break
		}
	}
	if !modeValid {
		errors = append(errors, fmt.Sprintf("MODE must be one of: %s", strings.Join(validModes, ", ")))
	}

	// Validate required fields based on mode
	switch strings.ToLower(c.Mode) {
	case "single":
		// Single mode requires deployment and service names
		if c.DeploymentName == "" {
			errors = append(errors, "DEPLOYMENT_NAME is required in single mode")
		}
		if c.ServiceName == "" {
			errors = append(errors, "SERVICE_NAME is required in single mode")
		}
	case "namespace", "cluster":
		// Multi-deployment modes require opt-in annotation and service selector
		if c.OptInAnnotation == "" {
			errors = append(errors, "OPT_IN_ANNOTATION is required in namespace/cluster mode")
		}
		if c.ServiceLabelSelector == "" {
			errors = append(errors, "SERVICE_LABEL_SELECTOR is required in namespace/cluster mode")
		}
	}

	if c.SQSQueueURL == "" {
		errors = append(errors, "SQS_QUEUE_URL is required but not set")
	}

	// Validate positive durations
	if c.CheckInterval <= 0 {
		errors = append(errors, "CHECK_INTERVAL must be positive")
	}

	if c.HealthCheckInterval <= 0 {
		errors = append(errors, "HEALTH_CHECK_INTERVAL must be positive")
	}

	if c.RolloutTimeout <= 0 {
		errors = append(errors, "ROLLOUT_TIMEOUT must be positive")
	}

	if c.VerificationDelay < 0 {
		errors = append(errors, "VERIFICATION_DELAY must be non-negative")
	}

	// Validate recovery durations (only if recovery is enabled)
	if c.RecoveryEnabled {
		if c.RecoveryInterval <= 0 {
			errors = append(errors, "RECOVERY_INTERVAL must be positive when recovery is enabled")
		}

		if c.RecoveryCooldown <= 0 {
			errors = append(errors, "RECOVERY_COOLDOWN must be positive when recovery is enabled")
		}

		if c.RecoveryTimeout <= 0 {
			errors = append(errors, "RECOVERY_TIMEOUT must be positive when recovery is enabled")
		}

		if c.RecoveryBackoffBase <= 0 {
			errors = append(errors, "RECOVERY_BACKOFF_BASE must be positive when recovery is enabled")
		}

		if c.RecoveryMaxBackoff <= 0 {
			errors = append(errors, "RECOVERY_MAX_BACKOFF must be positive when recovery is enabled")
		}

		// Logical validation: cooldown should be reasonable compared to interval
		if c.RecoveryCooldown < c.RecoveryInterval {
			errors = append(errors, "RECOVERY_COOLDOWN should be greater than RECOVERY_INTERVAL")
		}

		// Max backoff should be greater than base backoff
		if c.RecoveryMaxBackoff < c.RecoveryBackoffBase {
			errors = append(errors, "RECOVERY_MAX_BACKOFF should be greater than RECOVERY_BACKOFF_BASE")
		}
	}

	// Validate positive integers
	if c.HealthCheckRetries < 0 {
		errors = append(errors, "HEALTH_CHECK_RETRIES must be non-negative")
	}

	// Validate log level
	validLogLevels := []string{"debug", "info", "warn", "error"}
	logLevelValid := false
	for _, level := range validLogLevels {
		if strings.ToLower(c.LogLevel) == level {
			logLevelValid = true
			break
		}
	}
	if !logLevelValid {
		errors = append(errors, fmt.Sprintf("LOG_LEVEL must be one of: %s", strings.Join(validLogLevels, ", ")))
	}

	// Validate namespace format (basic Kubernetes namespace validation)
	if c.Namespace == "" {
		errors = append(errors, "NAMESPACE cannot be empty")
	}

	// Validate labels are not empty
	if c.SpotLabel == "" {
		errors = append(errors, "SPOT_LABEL cannot be empty")
	}

	if c.FargateLabel == "" {
		errors = append(errors, "FARGATE_LABEL cannot be empty")
	}

	if c.ComputeLabelKey == "" {
		errors = append(errors, "COMPUTE_LABEL_KEY cannot be empty")
	}

	// Validate API configuration
	if c.APIEnabled {
		if c.APIPort <= 0 || c.APIPort > 65535 {
			errors = append(errors, "API_PORT must be between 1 and 65535 when API is enabled")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("configuration validation failed:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

// IsSingleMode returns true if orchestrator is in single deployment mode
func (c *Config) IsSingleMode() bool {
	return strings.ToLower(c.Mode) == "single"
}

// IsNamespaceMode returns true if orchestrator is in namespace-wide mode
func (c *Config) IsNamespaceMode() bool {
	return strings.ToLower(c.Mode) == "namespace"
}

// IsClusterMode returns true if orchestrator is in cluster-wide mode
func (c *Config) IsClusterMode() bool {
	return strings.ToLower(c.Mode) == "cluster"
}

// IsMultiDeploymentMode returns true if orchestrator manages multiple deployments
func (c *Config) IsMultiDeploymentMode() bool {
	return c.IsNamespaceMode() || c.IsClusterMode()
}

// Helper functions for environment variable and TOML parsing

// TODO: Currently unused utility functions - may be used for future config enhancements
/*
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
*/

func getEnvOrTOMLOrDefault(envKey, tomlValue, defaultValue string) string {
	// Environment variable takes precedence
	if value := os.Getenv(envKey); value != "" {
		return value
	}
	// Then TOML value
	if tomlValue != "" {
		return tomlValue
	}
	// Finally default
	return defaultValue
}

/*
func getEnvAsIntOrDefault(key string, defaultValue int) (int, error) {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue, nil
	}

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %s as integer: %w", key, err)
	}

	return value, nil
}
*/

func getEnvAsIntOrTOMLOrDefault(envKey string, tomlValue int, defaultValue int) (int, error) {
	// Environment variable takes precedence
	if valueStr := os.Getenv(envKey); valueStr != "" {
		value, err := strconv.Atoi(valueStr)
		if err != nil {
			return 0, fmt.Errorf("cannot parse %s as integer: %w", envKey, err)
		}
		return value, nil
	}
	// Then TOML value (if not zero)
	if tomlValue != 0 {
		return tomlValue, nil
	}
	// Finally default
	return defaultValue, nil
}

/*
func getEnvAsDurationOrDefault(key string, defaultValue time.Duration) (time.Duration, error) {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue, nil
	}

	value, err := time.ParseDuration(valueStr)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %s as duration: %w", key, err)
	}

	return value, nil
}
*/

func getEnvAsDurationOrTOMLOrDefault(envKey string, tomlValue string, defaultValue time.Duration) (time.Duration, error) {
	// Environment variable takes precedence
	if valueStr := os.Getenv(envKey); valueStr != "" {
		value, err := time.ParseDuration(valueStr)
		if err != nil {
			return 0, fmt.Errorf("cannot parse %s as duration: %w", envKey, err)
		}
		return value, nil
	}
	// Then TOML value
	if tomlValue != "" {
		value, err := time.ParseDuration(tomlValue)
		if err != nil {
			return 0, fmt.Errorf("cannot parse TOML %s as duration: %w", envKey, err)
		}
		return value, nil
	}
	// Finally default
	return defaultValue, nil
}

/*
func getEnvAsBoolOrDefault(key string, defaultValue bool) (bool, error) {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue, nil
	}

	value, err := strconv.ParseBool(valueStr)
	if err != nil {
		return false, fmt.Errorf("cannot parse %s as boolean: %w", key, err)
	}

	return value, nil
}
*/

func getEnvAsBoolOrTOMLOrDefault(envKey string, tomlValue bool, defaultValue bool) (bool, error) {
	// Environment variable takes precedence
	if valueStr := os.Getenv(envKey); valueStr != "" {
		value, err := strconv.ParseBool(valueStr)
		if err != nil {
			return false, fmt.Errorf("cannot parse %s as boolean: %w", envKey, err)
		}
		return value, nil
	}
	// For booleans, we use the TOML value directly since it's already parsed
	// If TOML wasn't loaded or the field wasn't set, tomlValue will be false (zero value)
	// In that case, we use the defaultValue
	if tomlValue || defaultValue {
		return tomlValue || defaultValue, nil
	}
	return defaultValue, nil
}

func getEnvAsFloatOrTOMLOrDefault(envKey string, tomlValue float64, defaultValue float64) (float64, error) {
	// Environment variable takes precedence
	if valueStr := os.Getenv(envKey); valueStr != "" {
		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot parse %s as float: %w", envKey, err)
		}
		return value, nil
	}
	// Then TOML value (if not zero)
	if tomlValue != 0 {
		return tomlValue, nil
	}
	// Finally default
	return defaultValue, nil
}
