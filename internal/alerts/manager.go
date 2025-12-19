package alerts

import (
	"context"
	"fmt"
	"time"

	"spot-fargate-orchestrator/internal/alerts/slack"
	"spot-fargate-orchestrator/internal/logger"
)

// Manager handles all alerting functionality
type Manager struct {
	slackClient *slack.SlackClient
	logger      *logger.Logger
	enabled     bool
}

// Config holds alerting configuration
type Config struct {
	SlackWebhookURL string
	Enabled         bool
}

// NewManager creates a new alert manager
func NewManager(config Config) *Manager {
	var slackClient *slack.SlackClient
	if config.SlackWebhookURL != "" {
		slackClient = slack.NewSlackClient(config.SlackWebhookURL)
	}

	return &Manager{
		slackClient: slackClient,
		logger:      logger.NewDefault("alert-manager"),
		enabled:     config.Enabled && config.SlackWebhookURL != "",
	}
}

// IsEnabled returns whether alerting is enabled
func (m *Manager) IsEnabled() bool {
	return m.enabled && m.slackClient != nil
}

// SpotInterruptionDetected sends an alert when a spot interruption is detected
func (m *Manager) SpotInterruptionDetected(ctx context.Context, instanceID, nodeName string, affectedPods []string, timeRemaining time.Duration) {
	if !m.IsEnabled() {
		return
	}

	m.logger.Info("Sending spot interruption alert",
		"instance_id", instanceID,
		"node_name", nodeName,
		"affected_pods", len(affectedPods))

	if err := m.slackClient.SendSpotInterruptionAlert(ctx, instanceID, nodeName, affectedPods, timeRemaining); err != nil {
		m.logger.Error("Failed to send spot interruption alert", "error", err)
	}
}

// MigrationStarted sends an alert when migration starts
func (m *Manager) MigrationStarted(ctx context.Context, deployment, namespace, fromType, toType string) {
	if !m.IsEnabled() {
		return
	}

	m.logger.Info("Sending migration start alert",
		"deployment", deployment,
		"namespace", namespace,
		"from_type", fromType,
		"to_type", toType)

	if err := m.slackClient.SendMigrationStartAlert(ctx, deployment, namespace, fromType, toType); err != nil {
		m.logger.Error("Failed to send migration start alert", "error", err)
	}
}

// MigrationCompleted sends an alert when migration completes successfully
func (m *Manager) MigrationCompleted(ctx context.Context, deployment, namespace string, duration time.Duration, podsCount int) {
	if !m.IsEnabled() {
		return
	}

	m.logger.Info("Sending migration success alert",
		"deployment", deployment,
		"namespace", namespace,
		"duration", duration.String(),
		"pods_count", podsCount)

	if err := m.slackClient.SendMigrationSuccessAlert(ctx, deployment, namespace, duration, podsCount); err != nil {
		m.logger.Error("Failed to send migration success alert", "error", err)
	}
}

// MigrationFailed sends an alert when migration fails
func (m *Manager) MigrationFailed(ctx context.Context, deployment, namespace string, err error, duration time.Duration) {
	if !m.IsEnabled() {
		return
	}

	m.logger.Error("Sending migration failure alert",
		"deployment", deployment,
		"namespace", namespace,
		"duration", duration.String(),
		"error", err)

	if alertErr := m.slackClient.SendMigrationFailureAlert(ctx, deployment, namespace, err, duration); alertErr != nil {
		m.logger.Error("Failed to send migration failure alert", "alert_error", alertErr)
	}
}

// HealthCheckFailed sends an alert when health checks fail
func (m *Manager) HealthCheckFailed(ctx context.Context, deployment, namespace string, failedPods, totalPods int) {
	if !m.IsEnabled() {
		return
	}

	m.logger.Warn("Sending health check failure alert",
		"deployment", deployment,
		"namespace", namespace,
		"failed_pods", failedPods,
		"total_pods", totalPods)

	if err := m.slackClient.SendHealthCheckAlert(ctx, deployment, namespace, failedPods, totalPods); err != nil {
		m.logger.Error("Failed to send health check alert", "error", err)
	}
}

// TestAlert sends a test alert to verify configuration
func (m *Manager) TestAlert(ctx context.Context) error {
	if !m.IsEnabled() {
		return fmt.Errorf("alerting is not enabled")
	}

	fields := map[string]string{
		"Status":    "✅ Configuration Valid",
		"Timestamp": time.Now().Format(time.RFC3339),
	}

	return m.slackClient.SendAlert(ctx, slack.AlertLevelInfo, "🧪 Test Alert", "Spot Fargate Orchestrator alerting is configured correctly!", fields)
}
