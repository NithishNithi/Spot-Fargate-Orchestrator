package watcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"spot-fargate-orchestrator/internal/spot/metadata"
)

func TestNewSpotWatcher(t *testing.T) {
	client := metadata.NewMetadataClient("http://test", 2*time.Second)
	interval := 5 * time.Second

	watcher := NewSpotWatcher(client, interval)

	if watcher == nil {
		t.Fatal("Expected watcher to be created, got nil")
	}

	if watcher.checkInterval != interval {
		t.Errorf("Expected check interval %v, got %v", interval, watcher.checkInterval)
	}

	if watcher.metadataClient != client {
		t.Error("Expected metadata client to be set")
	}

	if watcher.eventChan == nil {
		t.Error("Expected event channel to be initialized")
	}

	if watcher.logger == nil {
		t.Error("Expected logger to be initialized")
	}
}

func TestEventChannel(t *testing.T) {
	client := metadata.NewMetadataClient("http://test", 2*time.Second)
	watcher := NewSpotWatcher(client, 5*time.Second)

	eventChan := watcher.EventChannel()
	if eventChan == nil {
		t.Fatal("Expected event channel to be returned")
	}

	// Verify it's read-only
	select {
	case <-eventChan:
		// This should not block or panic
	default:
		// Expected - channel should be empty initially
	}
}

func TestSpotWatcher_Start_ContextCancellation(t *testing.T) {
	// Create a test server that returns 404 (no interruption)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := metadata.NewMetadataClient(server.URL, 2*time.Second)
	watcher := NewSpotWatcher(client, 100*time.Millisecond) // Fast interval for testing

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := watcher.Start(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}

	// Verify channel is closed
	select {
	case _, ok := <-watcher.EventChannel():
		if ok {
			t.Error("Expected channel to be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Channel should be closed immediately after context cancellation")
	}
}

func TestSpotWatcher_Start_NoInterruption(t *testing.T) {
	// Create a test server that returns 404 (no interruption)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest/meta-data/instance-id" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("i-1234567890abcdef0"))
			return
		}
		if r.URL.Path == "/latest/meta-data/spot/instance-action" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := metadata.NewMetadataClient(server.URL, 2*time.Second)
	watcher := NewSpotWatcher(client, 50*time.Millisecond) // Fast interval for testing

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	// Start watcher in goroutine
	done := make(chan error, 1)
	go func() {
		done <- watcher.Start(ctx)
	}()

	// Should not receive any events
	select {
	case event := <-watcher.EventChannel():
		t.Errorf("Expected no events, got %+v", event)
	case <-time.After(100 * time.Millisecond):
		// Expected - no events should be sent
	}

	// Wait for context to expire
	err := <-done
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
}

func TestSpotWatcher_Start_WithInterruption(t *testing.T) {
	// Create interruption notice
	interruptionTime := time.Now().Add(2 * time.Minute)
	notice := metadata.InterruptionNotice{
		Action: "terminate",
		Time:   interruptionTime,
	}
	noticeJSON, _ := json.Marshal(notice)

	// Create a test server that returns interruption notice
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest/meta-data/instance-id" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("i-1234567890abcdef0"))
			return
		}
		if r.URL.Path == "/latest/meta-data/spot/instance-action" {
			w.WriteHeader(http.StatusOK)
			w.Write(noticeJSON)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := metadata.NewMetadataClient(server.URL, 2*time.Second)
	watcher := NewSpotWatcher(client, 50*time.Millisecond) // Fast interval for testing

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Start watcher in goroutine
	go func() {
		watcher.Start(ctx)
	}()

	// Should receive interruption event
	select {
	case event := <-watcher.EventChannel():
		if event.Type != EventSpotInterruption {
			t.Errorf("Expected EventSpotInterruption, got %v", event.Type)
		}
		if event.Notice == nil {
			t.Error("Expected notice to be set")
		}
		if event.Notice.Action != "terminate" {
			t.Errorf("Expected action 'terminate', got %s", event.Notice.Action)
		}
		if event.InstanceID != "i-1234567890abcdef0" {
			t.Errorf("Expected instance ID 'i-1234567890abcdef0', got %s", event.InstanceID)
		}
		if event.TimeRemaining <= 0 {
			t.Errorf("Expected positive time remaining, got %v", event.TimeRemaining)
		}
	case <-time.After(150 * time.Millisecond):
		t.Error("Expected to receive interruption event")
	}
}

func TestSpotWatcher_Start_MetadataError(t *testing.T) {
	// Create a test server that returns errors
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest/meta-data/instance-id" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("i-1234567890abcdef0"))
			return
		}
		if r.URL.Path == "/latest/meta-data/spot/instance-action" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := metadata.NewMetadataClient(server.URL, 2*time.Second)
	watcher := NewSpotWatcher(client, 50*time.Millisecond) // Fast interval for testing

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	// Start watcher in goroutine
	done := make(chan error, 1)
	go func() {
		done <- watcher.Start(ctx)
	}()

	// Should not receive any events due to errors
	select {
	case event := <-watcher.EventChannel():
		t.Errorf("Expected no events due to errors, got %+v", event)
	case <-time.After(100 * time.Millisecond):
		// Expected - no events should be sent when there are errors
	}

	// Watcher should continue running despite errors
	err := <-done
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
}

func TestSpotWatcher_Start_InstanceIDError(t *testing.T) {
	// Create a test server that fails to return instance ID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest/meta-data/instance-id" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/latest/meta-data/spot/instance-action" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := metadata.NewMetadataClient(server.URL, 2*time.Second)
	watcher := NewSpotWatcher(client, 50*time.Millisecond) // Fast interval for testing

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	// Start watcher in goroutine - should continue even with instance ID error
	done := make(chan error, 1)
	go func() {
		done <- watcher.Start(ctx)
	}()

	// Wait for context to expire - watcher should continue running
	err := <-done
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
}

func TestSpotWatcher_EventChannelFull(t *testing.T) {
	// Create interruption notice
	interruptionTime := time.Now().Add(2 * time.Minute)
	notice := metadata.InterruptionNotice{
		Action: "terminate",
		Time:   interruptionTime,
	}
	noticeJSON, _ := json.Marshal(notice)

	// Create a test server that returns interruption notice
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest/meta-data/instance-id" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("i-1234567890abcdef0"))
			return
		}
		if r.URL.Path == "/latest/meta-data/spot/instance-action" {
			w.WriteHeader(http.StatusOK)
			w.Write(noticeJSON)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := metadata.NewMetadataClient(server.URL, 2*time.Second)

	// Create watcher with small buffer
	watcher := NewSpotWatcher(client, 10*time.Millisecond)
	// Replace the event channel with a smaller buffer for testing
	watcher.eventChan = make(chan SpotEvent, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Start watcher in goroutine
	go func() {
		watcher.Start(ctx)
	}()

	// Read first event to fill buffer
	select {
	case <-watcher.EventChannel():
		// Expected - first event received
	case <-time.After(50 * time.Millisecond):
		t.Error("Expected to receive first event")
	}

	// Let more events accumulate (should be dropped due to full channel)
	time.Sleep(50 * time.Millisecond)

	// Watcher should continue running even when channel is full
	// This test verifies the non-blocking send behavior
}

func TestCheckForInterruption_TimeRemaining(t *testing.T) {
	tests := []struct {
		name             string
		interruptionTime time.Time
		expectedPositive bool
	}{
		{
			name:             "future interruption",
			interruptionTime: time.Now().Add(2 * time.Minute),
			expectedPositive: true,
		},
		{
			name:             "past interruption",
			interruptionTime: time.Now().Add(-1 * time.Minute),
			expectedPositive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notice := metadata.InterruptionNotice{
				Action: "terminate",
				Time:   tt.interruptionTime,
			}
			noticeJSON, _ := json.Marshal(notice)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/latest/meta-data/spot/instance-action" {
					w.WriteHeader(http.StatusOK)
					w.Write(noticeJSON)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			client := metadata.NewMetadataClient(server.URL, 2*time.Second)
			watcher := NewSpotWatcher(client, 5*time.Second)

			err := watcher.checkForInterruption()
			if err != nil {
				t.Errorf("checkForInterruption failed: %v", err)
			}

			select {
			case event := <-watcher.EventChannel():
				if tt.expectedPositive && event.TimeRemaining <= 0 {
					t.Errorf("Expected positive time remaining, got %v", event.TimeRemaining)
				}
				if !tt.expectedPositive && event.TimeRemaining != 0 {
					t.Errorf("Expected zero time remaining for past interruption, got %v", event.TimeRemaining)
				}
			case <-time.After(100 * time.Millisecond):
				t.Error("Expected to receive event")
			}
		})
	}
}
