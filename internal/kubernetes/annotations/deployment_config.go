package annotations

import (
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"

	"spot-fargate-orchestrator/internal/config"
)

// Per-deployment configuration annotation keys
const (
	// Recovery configuration overrides
	AnnotationRecoveryCooldown = "spot-orchestrator/recovery-cooldown"
	AnnotationAllowFargate     = "spot-orchestrator/allow-fargate"
	AnnotationRecoveryEnabled  = "spot-orchestrator/recovery-enabled"

	// Migration configuration overrides
	AnnotationRolloutTimeout    = "spot-orchestrator/rollout-timeout"
	AnnotationVerificationDelay = "spot-orchestrator/verification-delay"
)

// DeploymentConfig holds per-deployment configuration overrides
type DeploymentConfig struct {
	// Global config (fallback)
	GlobalConfig *config.Config

	// Per-deployment overrides
	RecoveryCooldown  *time.Duration
	AllowFargate      *bool
	RecoveryEnabled   *bool
	RolloutTimeout    *time.Duration
	VerificationDelay *time.Duration
}

// NewDeploymentConfig creates a deployment-specific config from annotations
func NewDeploymentConfig(globalConfig *config.Config, deployment *appsv1.Deployment) *DeploymentConfig {
	dc := &DeploymentConfig{
		GlobalConfig: globalConfig,
	}

	if deployment.Annotations == nil {
		return dc
	}

	// Parse recovery cooldown override
	if cooldownStr, exists := deployment.Annotations[AnnotationRecoveryCooldown]; exists {
		if cooldown, err := time.ParseDuration(cooldownStr); err == nil {
			dc.RecoveryCooldown = &cooldown
		}
	}

	// Parse allow-fargate override
	if allowFargateStr, exists := deployment.Annotations[AnnotationAllowFargate]; exists {
		if allowFargate, err := strconv.ParseBool(allowFargateStr); err == nil {
			dc.AllowFargate = &allowFargate
		}
	}

	// Parse recovery-enabled override
	if recoveryEnabledStr, exists := deployment.Annotations[AnnotationRecoveryEnabled]; exists {
		if recoveryEnabled, err := strconv.ParseBool(recoveryEnabledStr); err == nil {
			dc.RecoveryEnabled = &recoveryEnabled
		}
	}

	// Parse rollout timeout override
	if timeoutStr, exists := deployment.Annotations[AnnotationRolloutTimeout]; exists {
		if timeout, err := time.ParseDuration(timeoutStr); err == nil {
			dc.RolloutTimeout = &timeout
		}
	}

	// Parse verification delay override
	if delayStr, exists := deployment.Annotations[AnnotationVerificationDelay]; exists {
		if delay, err := time.ParseDuration(delayStr); err == nil {
			dc.VerificationDelay = &delay
		}
	}

	return dc
}

// GetRecoveryCooldown returns the recovery cooldown (deployment override or global)
func (dc *DeploymentConfig) GetRecoveryCooldown() time.Duration {
	if dc.RecoveryCooldown != nil {
		return *dc.RecoveryCooldown
	}
	return dc.GlobalConfig.RecoveryCooldown
}

// GetAllowFargate returns whether Fargate migration is allowed (deployment override or global)
func (dc *DeploymentConfig) GetAllowFargate() bool {
	if dc.AllowFargate != nil {
		return *dc.AllowFargate
	}
	return true // Default: allow Fargate migration
}

// GetRecoveryEnabled returns whether recovery is enabled (deployment override or global)
func (dc *DeploymentConfig) GetRecoveryEnabled() bool {
	if dc.RecoveryEnabled != nil {
		return *dc.RecoveryEnabled
	}
	return dc.GlobalConfig.RecoveryEnabled
}

// GetRolloutTimeout returns the rollout timeout (deployment override or global)
func (dc *DeploymentConfig) GetRolloutTimeout() time.Duration {
	if dc.RolloutTimeout != nil {
		return *dc.RolloutTimeout
	}
	return dc.GlobalConfig.RolloutTimeout
}

// GetVerificationDelay returns the verification delay (deployment override or global)
func (dc *DeploymentConfig) GetVerificationDelay() time.Duration {
	if dc.VerificationDelay != nil {
		return *dc.VerificationDelay
	}
	return dc.GlobalConfig.VerificationDelay
}

// IsSpotOnly returns true if this deployment should never migrate to Fargate
func (dc *DeploymentConfig) IsSpotOnly() bool {
	return !dc.GetAllowFargate()
}

// ShouldSkipMigration returns true if this deployment should not be migrated to Fargate
func (dc *DeploymentConfig) ShouldSkipMigration() bool {
	return dc.IsSpotOnly()
}
