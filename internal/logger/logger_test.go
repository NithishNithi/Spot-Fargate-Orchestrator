package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name      string
		component string
		level     LogLevel
	}{
		{"debug level", "test-component", LevelDebug},
		{"info level", "orchestrator", LevelInfo},
		{"warn level", "spot-watcher", LevelWarn},
		{"error level", "health-checker", LevelError},
		{"invalid level defaults to info", "test", LogLevel("invalid")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := New(tt.component, tt.level)
			if logger == nil {
				t.Fatal("Expected logger to be created, got nil")
			}
			if logger.Component() != tt.component {
				t.Errorf("Expected component %s, got %s", tt.component, logger.Component())
			}
		})
	}
}

func TestNewDefault(t *testing.T) {
	logger := NewDefault("test-component")
	if logger == nil {
		t.Fatal("Expected logger to be created, got nil")
	}
	if logger.Component() != "test-component" {
		t.Errorf("Expected component test-component, got %s", logger.Component())
	}
}

func TestLoggingMethods(t *testing.T) {
	// Capture output for testing
	var buf bytes.Buffer

	// Create a logger that writes to our buffer
	zerologLogger := zerolog.New(&buf).
		Level(zerolog.DebugLevel).
		With().
		Timestamp().
		Str("component", "test").
		Logger()

	logger := &Logger{
		Logger:    zerologLogger,
		component: "test",
	}

	tests := []struct {
		name     string
		logFunc  func()
		expected string
	}{
		{
			name: "debug message",
			logFunc: func() {
				zerolog.SetGlobalLevel(zerolog.DebugLevel)
				logger.Debug("debug message", "key", "value")
			},
			expected: "debug message",
		},
		{
			name: "info message",
			logFunc: func() {
				logger.Info("info message", "key", "value")
			},
			expected: "info message",
		},
		{
			name: "warn message",
			logFunc: func() {
				logger.Warn("warn message", "key", "value")
			},
			expected: "warn message",
		},
		{
			name: "error message",
			logFunc: func() {
				logger.Error("error message", "key", "value")
			},
			expected: "error message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			tt.logFunc()

			output := buf.String()
			if !strings.Contains(output, tt.expected) {
				t.Errorf("Expected output to contain %q, got %q", tt.expected, output)
			}

			// Verify JSON structure
			var logEntry map[string]interface{}
			if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
				t.Errorf("Expected valid JSON output, got error: %v", err)
			}

			// Verify required fields
			if logEntry["component"] != "test" {
				t.Errorf("Expected component field to be 'test', got %v", logEntry["component"])
			}
			if logEntry["message"] != tt.expected {
				t.Errorf("Expected message field to be %q, got %v", tt.expected, logEntry["message"])
			}
		})
	}
}

func TestWithFields(t *testing.T) {
	var buf bytes.Buffer
	zerologLogger := zerolog.New(&buf).
		Level(zerolog.DebugLevel).
		With().
		Timestamp().
		Str("component", "test").
		Logger()

	logger := &Logger{
		Logger:    zerologLogger,
		component: "test",
	}

	// Create logger with additional fields
	loggerWithFields := logger.WithFields(
		"service", "orchestrator",
		"version", "1.0.0",
	)

	loggerWithFields.Info("test message")

	output := buf.String()
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("Expected valid JSON output, got error: %v", err)
	}

	// Verify additional fields are present
	if logEntry["service"] != "orchestrator" {
		t.Errorf("Expected service field to be 'orchestrator', got %v", logEntry["service"])
	}
	if logEntry["version"] != "1.0.0" {
		t.Errorf("Expected version field to be '1.0.0', got %v", logEntry["version"])
	}
}

func TestWithContext(t *testing.T) {
	logger := NewDefault("test")
	ctx := context.Background()

	contextLogger := logger.WithContext(ctx)
	if contextLogger == nil {
		t.Fatal("Expected context logger to be created, got nil")
	}
	if contextLogger.Component() != "test" {
		t.Errorf("Expected component to be preserved, got %s", contextLogger.Component())
	}
}

func TestSpecializedLoggingMethods(t *testing.T) {
	var buf bytes.Buffer
	zerologLogger := zerolog.New(&buf).
		Level(zerolog.DebugLevel).
		With().
		Timestamp().
		Str("component", "test").
		Logger()

	logger := &Logger{
		Logger:    zerologLogger,
		component: "test",
	}

	t.Run("LogSpotInterruption", func(t *testing.T) {
		buf.Reset()
		logger.LogSpotInterruption("i-1234567890abcdef0", "120s")

		output := buf.String()
		var logEntry map[string]interface{}
		if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
			t.Fatalf("Expected valid JSON output, got error: %v", err)
		}

		if logEntry["instance_id"] != "i-1234567890abcdef0" {
			t.Errorf("Expected instance_id field, got %v", logEntry["instance_id"])
		}
		if logEntry["time_remaining"] != "120s" {
			t.Errorf("Expected time_remaining field, got %v", logEntry["time_remaining"])
		}
		if logEntry["event_type"] != "spot_interruption" {
			t.Errorf("Expected event_type field, got %v", logEntry["event_type"])
		}
	})

	t.Run("LogMigrationStart", func(t *testing.T) {
		buf.Reset()
		logger.LogMigrationStart("spot", "fargate")

		output := buf.String()
		var logEntry map[string]interface{}
		if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
			t.Fatalf("Expected valid JSON output, got error: %v", err)
		}

		if logEntry["current_compute_type"] != "spot" {
			t.Errorf("Expected current_compute_type field, got %v", logEntry["current_compute_type"])
		}
		if logEntry["target_compute_type"] != "fargate" {
			t.Errorf("Expected target_compute_type field, got %v", logEntry["target_compute_type"])
		}
		if logEntry["event_type"] != "migration_start" {
			t.Errorf("Expected event_type field, got %v", logEntry["event_type"])
		}
	})

	t.Run("LogMigrationComplete", func(t *testing.T) {
		buf.Reset()
		logger.LogMigrationComplete("45s", "healthy")

		output := buf.String()
		var logEntry map[string]interface{}
		if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
			t.Fatalf("Expected valid JSON output, got error: %v", err)
		}

		if logEntry["duration"] != "45s" {
			t.Errorf("Expected duration field, got %v", logEntry["duration"])
		}
		if logEntry["final_state"] != "healthy" {
			t.Errorf("Expected final_state field, got %v", logEntry["final_state"])
		}
		if logEntry["event_type"] != "migration_complete" {
			t.Errorf("Expected event_type field, got %v", logEntry["event_type"])
		}
	})

	t.Run("LogError", func(t *testing.T) {
		buf.Reset()
		testErr := errors.New("test error")
		logger.LogError("test operation", testErr, "context_key", "context_value")

		output := buf.String()
		var logEntry map[string]interface{}
		if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
			t.Fatalf("Expected valid JSON output, got error: %v", err)
		}

		if logEntry["operation"] != "test operation" {
			t.Errorf("Expected operation field, got %v", logEntry["operation"])
		}
		if logEntry["error"] != "test error" {
			t.Errorf("Expected error field, got %v", logEntry["error"])
		}
		if logEntry["event_type"] != "error" {
			t.Errorf("Expected event_type field, got %v", logEntry["event_type"])
		}
		if logEntry["context_key"] != "context_value" {
			t.Errorf("Expected context_key field, got %v", logEntry["context_key"])
		}
	})
}

func TestLogLevels(t *testing.T) {
	// Test that different log levels work correctly
	tests := []struct {
		name      string
		level     LogLevel
		logFunc   func(*Logger)
		shouldLog bool
	}{
		{
			name:  "debug level logs debug messages",
			level: LevelDebug,
			logFunc: func(l *Logger) {
				l.Debug("debug message")
			},
			shouldLog: true,
		},
		{
			name:  "info level does not log debug messages",
			level: LevelInfo,
			logFunc: func(l *Logger) {
				l.Debug("debug message")
			},
			shouldLog: false,
		},
		{
			name:  "info level logs info messages",
			level: LevelInfo,
			logFunc: func(l *Logger) {
				l.Info("info message")
			},
			shouldLog: true,
		},
		{
			name:  "error level only logs error messages",
			level: LevelError,
			logFunc: func(l *Logger) {
				l.Info("info message")
			},
			shouldLog: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Redirect stdout to capture output
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			logger := New("test", tt.level)
			tt.logFunc(logger)

			w.Close()
			os.Stdout = oldStdout

			buf := make([]byte, 1024)
			n, _ := r.Read(buf)
			output := string(buf[:n])

			hasOutput := len(strings.TrimSpace(output)) > 0
			if hasOutput != tt.shouldLog {
				t.Errorf("Expected shouldLog=%v, but got output=%v", tt.shouldLog, hasOutput)
			}
		})
	}
}
