package watcher

import (
	"context"
	"fmt"
	"time"

	"spot-fargate-orchestrator/internal/logger"
	"spot-fargate-orchestrator/internal/spot/metadata"
)

// EventType represents the type of spot event
type EventType string

const (
	EventSpotInterruption EventType = "SPOT_INTERRUPTION"
	EventHealthCheck      EventType = "HEALTH_CHECK"
)

// InterruptionNotice represents a spot interruption notice
type InterruptionNotice struct {
	Action string    `json:"action"`
	Time   time.Time `json:"time"`
}

// SpotEvent represents an event from the spot watcher
type SpotEvent struct {
	Type          EventType           `json:"type"`
	Timestamp     time.Time           `json:"timestamp"`
	Notice        *InterruptionNotice `json:"notice,omitempty"`
	TimeRemaining time.Duration       `json:"time_remaining"`
	InstanceID    string              `json:"instance_id"`
	NodeName      string              `json:"node_name,omitempty"`     // Kubernetes node name
	AffectedPods  []string            `json:"affected_pods,omitempty"` // List of affected pod names
}

// SpotWatcher monitors for spot interruption notices
type SpotWatcher struct {
	metadataClient metadata.MetadataClientInterface
	checkInterval  time.Duration
	eventChan      chan SpotEvent
	logger         *logger.Logger
}

// NewSpotWatcher creates a new spot watcher
func NewSpotWatcher(client metadata.MetadataClientInterface, interval time.Duration) *SpotWatcher {
	return &SpotWatcher{
		metadataClient: client,
		checkInterval:  interval,
		eventChan:      make(chan SpotEvent, 10),
		logger:         logger.NewDefault("spot-watcher"),
	}
}

// Start begins monitoring for spot interruption notices
// Implements continuous monitoring loop with context cancellation support
func (w *SpotWatcher) Start(ctx context.Context) error {
	w.logger.Info("Starting spot watcher",
		"check_interval", w.checkInterval.String(),
	)

	w.logger.Info("Spot watcher started successfully - monitoring for spot interruptions affecting target deployment")

	ticker := time.NewTicker(w.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("Spot watcher stopping due to context cancellation")
			close(w.eventChan)
			return ctx.Err()

		case <-ticker.C:
			if err := w.checkForInterruption(); err != nil {
				w.logger.Error("Error checking for spot interruption", "error", err)
				// Continue monitoring even if individual checks fail
			}
		}
	}
}

// checkForInterruption polls the metadata service and sends events if interruption is detected
func (w *SpotWatcher) checkForInterruption() error {
	notice, err := w.metadataClient.GetSpotInterruptionNotice()
	if err != nil {
		return fmt.Errorf("failed to check spot interruption notice: %w", err)
	}

	// No interruption notice found
	if notice == nil {
		w.logger.Debug("No spot interruption notice found")
		return nil
	}

	// Calculate time remaining until termination
	timeRemaining := time.Until(notice.Time)
	if timeRemaining < 0 {
		timeRemaining = 0
	}

	// Get instance ID for this specific interruption
	instanceID := "unknown"
	if id, err := w.metadataClient.GetInstanceID(); err == nil {
		instanceID = id
	}

	w.logger.Info("Spot interruption notice detected",
		"action", notice.Action,
		"termination_time", notice.Time.Format(time.RFC3339),
		"time_remaining", timeRemaining.String(),
		"affected_instance", instanceID,
	)

	// Create and send spot event
	event := SpotEvent{
		Type:      EventSpotInterruption,
		Timestamp: time.Now(),
		Notice: &InterruptionNotice{
			Action: notice.Action,
			Time:   notice.Time,
		},
		TimeRemaining: timeRemaining,
		InstanceID:    instanceID,
	}

	// Send event to channel (non-blocking to prevent deadlock)
	select {
	case w.eventChan <- event:
		w.logger.Info("Spot interruption event sent successfully")
	default:
		w.logger.Warn("Event channel full, dropping spot interruption event")
	}

	return nil
}

// EventChannel returns the channel for receiving spot events
func (w *SpotWatcher) EventChannel() <-chan SpotEvent {
	return w.eventChan
}
