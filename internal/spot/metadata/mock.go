package metadata

import (
	"os"
	"time"
)

// MockMetadataClient simulates EC2 metadata service for local testing
type MockMetadataClient struct {
	instanceID           string
	isSpot               bool
	simulateInterruption bool
	interruptionTime     time.Time
	startTime            time.Time
}

// NewMockMetadataClient creates a mock metadata client for testing
func NewMockMetadataClient() *MockMetadataClient {
	now := time.Now()
	return &MockMetadataClient{
		instanceID:           "i-mock123456789",
		isSpot:               true,
		simulateInterruption: os.Getenv("SIMULATE_SPOT_INTERRUPTION") == "true",
		interruptionTime:     now.Add(2 * time.Minute), // 2 minutes from interruption trigger
		startTime:            now,
	}
}

// GetSpotInterruptionNotice returns a mock interruption notice if simulation is enabled
func (m *MockMetadataClient) GetSpotInterruptionNotice() (*InterruptionNotice, error) {
	if !m.simulateInterruption {
		return nil, nil // No interruption
	}

	// Wait 30 seconds after start before triggering interruption
	if time.Since(m.startTime) < 30*time.Second {
		return nil, nil // No interruption yet
	}

	// Simulate interruption notice after 30 seconds
	return &InterruptionNotice{
		Action: "terminate",
		Time:   m.interruptionTime,
	}, nil
}

// GetInstanceID returns a mock instance ID
func (m *MockMetadataClient) GetInstanceID() (string, error) {
	return m.instanceID, nil
}

// IsSpotInstance returns true for testing
func (m *MockMetadataClient) IsSpotInstance() (bool, error) {
	return m.isSpot, nil
}

// SetSimulateInterruption enables/disables interruption simulation
func (m *MockMetadataClient) SetSimulateInterruption(simulate bool) {
	now := time.Now()
	m.simulateInterruption = simulate
	m.startTime = now
	if simulate {
		m.interruptionTime = now.Add(30*time.Second + 2*time.Minute) // 30s delay + 2min warning
	}
}

// TriggerInterruption immediately triggers an interruption notice
func (m *MockMetadataClient) TriggerInterruption() {
	now := time.Now()
	m.simulateInterruption = true
	m.startTime = now.Add(-30 * time.Second)      // Set start time to 30 seconds ago to trigger immediately
	m.interruptionTime = now.Add(2 * time.Minute) // 2 minutes from now
}
