package state

import (
	"sync"
	"testing"
	"time"
)

func TestNewState(t *testing.T) {
	s := NewState()
	if s == nil {
		t.Fatal("NewState() returned nil")
	}

	snapshot := s.GetSnapshot()
	if snapshot.Status != "initializing" {
		t.Errorf("Expected status 'initializing', got '%s'", snapshot.Status)
	}

	if snapshot.CurrentComputeType != "" {
		t.Errorf("Expected empty compute type, got '%s'", snapshot.CurrentComputeType)
	}

	if snapshot.MigrationCount != 0 {
		t.Errorf("Expected migration count 0, got %d", snapshot.MigrationCount)
	}
}

func TestSetCurrentComputeType(t *testing.T) {
	s := NewState()

	s.SetCurrentComputeType("spot")
	if got := s.GetCurrentComputeType(); got != "spot" {
		t.Errorf("Expected 'spot', got '%s'", got)
	}

	s.SetCurrentComputeType("fargate")
	if got := s.GetCurrentComputeType(); got != "fargate" {
		t.Errorf("Expected 'fargate', got '%s'", got)
	}
}

func TestRecordMigration(t *testing.T) {
	s := NewState()

	// Initial state
	snapshot := s.GetSnapshot()
	if snapshot.MigrationCount != 0 {
		t.Errorf("Expected initial migration count 0, got %d", snapshot.MigrationCount)
	}

	// Record first migration
	before := time.Now()
	s.RecordMigration()
	after := time.Now()

	snapshot = s.GetSnapshot()
	if snapshot.MigrationCount != 1 {
		t.Errorf("Expected migration count 1, got %d", snapshot.MigrationCount)
	}

	if snapshot.LastMigration.Before(before) || snapshot.LastMigration.After(after) {
		t.Errorf("Migration timestamp %v not within expected range [%v, %v]",
			snapshot.LastMigration, before, after)
	}

	// Record second migration
	s.RecordMigration()
	snapshot = s.GetSnapshot()
	if snapshot.MigrationCount != 2 {
		t.Errorf("Expected migration count 2, got %d", snapshot.MigrationCount)
	}
}

func TestSetStatus(t *testing.T) {
	s := NewState()

	s.SetStatus("healthy")
	snapshot := s.GetSnapshot()
	if snapshot.Status != "healthy" {
		t.Errorf("Expected status 'healthy', got '%s'", snapshot.Status)
	}

	s.SetStatus("migrating")
	snapshot = s.GetSnapshot()
	if snapshot.Status != "migrating" {
		t.Errorf("Expected status 'migrating', got '%s'", snapshot.Status)
	}
}

func TestSetLastError(t *testing.T) {
	s := NewState()

	s.SetLastError("test error")
	snapshot := s.GetSnapshot()
	if snapshot.LastError != "test error" {
		t.Errorf("Expected error 'test error', got '%s'", snapshot.LastError)
	}

	s.SetLastError("")
	snapshot = s.GetSnapshot()
	if snapshot.LastError != "" {
		t.Errorf("Expected empty error, got '%s'", snapshot.LastError)
	}
}

func TestGetSnapshot(t *testing.T) {
	s := NewState()

	// Set up state
	s.SetCurrentComputeType("spot")
	s.SetStatus("healthy")
	s.SetLastError("previous error")
	s.RecordMigration()

	snapshot := s.GetSnapshot()

	// Verify all fields are captured
	if snapshot.CurrentComputeType != "spot" {
		t.Errorf("Expected compute type 'spot', got '%s'", snapshot.CurrentComputeType)
	}

	if snapshot.Status != "healthy" {
		t.Errorf("Expected status 'healthy', got '%s'", snapshot.Status)
	}

	if snapshot.LastError != "previous error" {
		t.Errorf("Expected error 'previous error', got '%s'", snapshot.LastError)
	}

	if snapshot.MigrationCount != 1 {
		t.Errorf("Expected migration count 1, got %d", snapshot.MigrationCount)
	}

	// Verify timestamp is recent
	if time.Since(snapshot.Timestamp) > time.Second {
		t.Errorf("Snapshot timestamp %v is too old", snapshot.Timestamp)
	}
}

// TestConcurrentAccess verifies thread safety
func TestConcurrentAccess(t *testing.T) {
	s := NewState()

	var wg sync.WaitGroup
	numGoroutines := 100

	// Start multiple goroutines that modify state concurrently
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			// Perform various state operations
			s.SetCurrentComputeType("spot")
			s.SetStatus("migrating")
			s.RecordMigration()
			s.SetLastError("concurrent error")

			// Read operations
			_ = s.GetCurrentComputeType()
			_ = s.GetSnapshot()
		}(i)
	}

	wg.Wait()

	// Verify final state is consistent
	snapshot := s.GetSnapshot()
	if snapshot.MigrationCount != numGoroutines {
		t.Errorf("Expected migration count %d, got %d", numGoroutines, snapshot.MigrationCount)
	}
}
