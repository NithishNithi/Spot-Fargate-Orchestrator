package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"spot-fargate-orchestrator/internal/logger"
)

// SlackClient handles Slack webhook notifications
type SlackClient struct {
	webhookURL string
	httpClient *http.Client
	logger     *logger.Logger
}

// SlackMessage represents a Slack message payload
type SlackMessage struct {
	Text        string       `json:"text,omitempty"`
	Username    string       `json:"username,omitempty"`
	IconEmoji   string       `json:"icon_emoji,omitempty"`
	Channel     string       `json:"channel,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment represents a Slack message attachment
type Attachment struct {
	Color      string   `json:"color,omitempty"`
	Title      string   `json:"title,omitempty"`
	Text       string   `json:"text,omitempty"`
	Timestamp  int64    `json:"ts,omitempty"`
	Footer     string   `json:"footer,omitempty"`
	Fields     []Field  `json:"fields,omitempty"`
	MarkdownIn []string `json:"mrkdwn_in,omitempty"`
}

// Field represents a field in a Slack attachment
type Field struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// AlertLevel represents the severity of an alert
type AlertLevel string

const (
	AlertLevelInfo     AlertLevel = "info"
	AlertLevelWarning  AlertLevel = "warning"
	AlertLevelCritical AlertLevel = "critical"
	AlertLevelSuccess  AlertLevel = "success"
)

// NewSlackClient creates a new Slack client
func NewSlackClient(webhookURL string) *SlackClient {
	return &SlackClient{
		webhookURL: webhookURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger.NewDefault("slack-client"),
	}
}

// SendAlert sends an alert to Slack
func (s *SlackClient) SendAlert(ctx context.Context, level AlertLevel, title, message string, fields map[string]string) error {
	if s.webhookURL == "" {
		s.logger.Debug("Slack webhook URL not configured, skipping alert")
		return nil
	}

	attachment := s.createAttachment(level, title, message, fields)
	slackMsg := SlackMessage{
		Username:    "Spot Fargate Orchestrator",
		IconEmoji:   ":warning:",
		Attachments: []Attachment{attachment},
	}

	return s.sendMessage(ctx, slackMsg)
}

// SendSpotInterruptionAlert sends a spot interruption alert
func (s *SlackClient) SendSpotInterruptionAlert(ctx context.Context, instanceID, nodeName string, affectedPods []string, timeRemaining time.Duration) error {
	fields := map[string]string{
		"Instance ID":     instanceID,
		"Kubernetes Node": nodeName,
		"Affected Pods":   fmt.Sprintf("%d pods", len(affectedPods)),
		"Time Remaining":  timeRemaining.String(),
		"Action":          "Migration to Fargate initiated",
	}

	title := "🚨 EC2 Spot Instance Interruption Detected"
	message := fmt.Sprintf("Spot instance `%s` will be terminated. Migrating %d pods to Fargate.", instanceID, len(affectedPods))

	return s.SendAlert(ctx, AlertLevelCritical, title, message, fields)
}

// SendMigrationStartAlert sends a migration start alert
func (s *SlackClient) SendMigrationStartAlert(ctx context.Context, deployment, namespace, fromType, toType string) error {
	fields := map[string]string{
		"Deployment": deployment,
		"Namespace":  namespace,
		"From":       fromType,
		"To":         toType,
		"Status":     "In Progress",
	}

	title := "🔄 Migration Started"
	message := fmt.Sprintf("Starting migration of deployment `%s` from %s to %s", deployment, fromType, toType)

	return s.SendAlert(ctx, AlertLevelWarning, title, message, fields)
}

// SendMigrationSuccessAlert sends a migration success alert
func (s *SlackClient) SendMigrationSuccessAlert(ctx context.Context, deployment, namespace string, duration time.Duration, podsCount int) error {
	fields := map[string]string{
		"Deployment":    deployment,
		"Namespace":     namespace,
		"Duration":      duration.String(),
		"Migrated Pods": fmt.Sprintf("%d", podsCount),
		"Status":        "✅ Completed Successfully",
	}

	title := "✅ Migration Completed Successfully"
	message := fmt.Sprintf("Deployment `%s` successfully migrated to Fargate in %s", deployment, duration.String())

	return s.SendAlert(ctx, AlertLevelSuccess, title, message, fields)
}

// SendMigrationFailureAlert sends a migration failure alert
func (s *SlackClient) SendMigrationFailureAlert(ctx context.Context, deployment, namespace string, err error, duration time.Duration) error {
	fields := map[string]string{
		"Deployment": deployment,
		"Namespace":  namespace,
		"Duration":   duration.String(),
		"Error":      err.Error(),
		"Status":     "❌ Failed",
		"Action":     "Manual intervention required",
	}

	title := "❌ Migration Failed"
	message := fmt.Sprintf("Migration of deployment `%s` failed after %s", deployment, duration.String())

	return s.SendAlert(ctx, AlertLevelCritical, title, message, fields)
}

// SendHealthCheckAlert sends a health check failure alert
func (s *SlackClient) SendHealthCheckAlert(ctx context.Context, deployment, namespace string, failedPods int, totalPods int) error {
	fields := map[string]string{
		"Deployment":  deployment,
		"Namespace":   namespace,
		"Failed Pods": fmt.Sprintf("%d", failedPods),
		"Total Pods":  fmt.Sprintf("%d", totalPods),
		"Status":      "⚠️ Health Check Failed",
	}

	title := "⚠️ Health Check Failed"
	message := fmt.Sprintf("Health check failed for deployment `%s`: %d/%d pods unhealthy", deployment, failedPods, totalPods)

	return s.SendAlert(ctx, AlertLevelWarning, title, message, fields)
}

// createAttachment creates a Slack attachment based on alert level
func (s *SlackClient) createAttachment(level AlertLevel, title, message string, fields map[string]string) Attachment {
	var color string
	var emoji string

	switch level {
	case AlertLevelInfo:
		color = "#36a64f" // Green
		emoji = "ℹ️"
	case AlertLevelWarning:
		color = "#ff9500" // Orange
		emoji = "⚠️"
	case AlertLevelCritical:
		color = "#ff0000" // Red
		emoji = "🚨"
	case AlertLevelSuccess:
		color = "#36a64f" // Green
		emoji = "✅"
	default:
		color = "#36a64f" // Default green
		emoji = "ℹ️"
	}

	attachment := Attachment{
		Color:      color,
		Title:      fmt.Sprintf("%s %s", emoji, title),
		Text:       message,
		Timestamp:  time.Now().Unix(),
		Footer:     "Spot Fargate Orchestrator",
		MarkdownIn: []string{"text", "fields"},
	}

	// Add fields if provided
	if len(fields) > 0 {
		for key, value := range fields {
			attachment.Fields = append(attachment.Fields, Field{
				Title: key,
				Value: value,
				Short: true,
			})
		}
	}

	return attachment
}

// sendMessage sends a message to Slack
func (s *SlackClient) sendMessage(ctx context.Context, message SlackMessage) error {
	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal Slack message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	s.logger.Debug("Sending Slack alert",
		"webhook_url", s.maskWebhookURL(s.webhookURL),
		"message_size", len(jsonData))

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send Slack message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Slack API returned status %d", resp.StatusCode)
	}

	s.logger.Debug("Slack alert sent successfully",
		"status_code", resp.StatusCode)

	return nil
}

// maskWebhookURL masks the webhook URL for logging (security)
func (s *SlackClient) maskWebhookURL(url string) string {
	if len(url) > 50 {
		return url[:30] + "..." + url[len(url)-10:]
	}
	return "***masked***"
}
