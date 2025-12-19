package state

import (
	"sync"
	"time"
)

// StateSnapshot represents a point-in-time snapshot of the orchestrator state
type StateSnapshot struct {
	CurrentComputeType string    `json:"current_compute_type"`
	LastMigration      time.Time `json:"last_migration"`
	MigrationCount     int       `json:"migration_count"`
	LastError          string    `json:"last_error,omitempty"`
	Status             string    `json:"status"`
	Timestamp          time.Time `json:"timestamp"`
}

// State manages the orchestrator's internal state
type State struct {
	mu                 sync.RWMutex
	currentComputeType string
	lastMigration      time.Time
	migrationCount     int
	lastError          string
	status             string
}

// NewState creates a new state manager
func NewState() *State {
	return &State{
		status: "initializing",
	}
}

// GetCurrentComputeType returns the current compute type
func (s *State) GetCurrentComputeType() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentComputeType
}

// SetCurrentComputeType sets the current compute type
func (s *State) SetCurrentComputeType(computeType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentComputeType = computeType
}

// RecordMigration records a migration event
func (s *State) RecordMigration() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastMigration = time.Now()
	s.migrationCount++
}

// SetStatus sets the current status
func (s *State) SetStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// SetLastError sets the last error
func (s *State) SetLastError(err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = err
}

// GetSnapshot returns a snapshot of the current state
func (s *State) GetSnapshot() StateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return StateSnapshot{
		CurrentComputeType: s.currentComputeType,
		LastMigration:      s.lastMigration,
		MigrationCount:     s.migrationCount,
		LastError:          s.lastError,
		Status:             s.status,
		Timestamp:          time.Now(),
	}
}
