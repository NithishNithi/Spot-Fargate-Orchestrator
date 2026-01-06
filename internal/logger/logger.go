package logger

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/rs/zerolog"
)

// Global configuration for all loggers
var (
	globalLogLevel  LogLevel = LevelInfo
	globalLogFormat string   = "json"
	globalMutex     sync.RWMutex
)

// SetGlobalConfig sets the global logging configuration for all new loggers
func SetGlobalConfig(level LogLevel, format string) {
	globalMutex.Lock()
	defer globalMutex.Unlock()
	globalLogLevel = level
	globalLogFormat = format
}

// Logger wraps zerolog.Logger with additional functionality for structured logging
type Logger struct {
	zerolog.Logger
	component string
}

// LogLevel represents the logging level
type LogLevel string

const (
	LevelDebug LogLevel = "debug"
	LevelInfo  LogLevel = "info"
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
)

// New creates a new Logger instance with the specified component name and log level
func New(component string, level LogLevel) *Logger {
	return NewWithFormat(component, level, "")
}

// NewWithFormat creates a new Logger instance with specified component, level, and format
func NewWithFormat(component string, level LogLevel, format string) *Logger {
	var zerologLevel zerolog.Level
	switch strings.ToLower(string(level)) {
	case "debug":
		zerologLevel = zerolog.DebugLevel
	case "info":
		zerologLevel = zerolog.InfoLevel
	case "warn":
		zerologLevel = zerolog.WarnLevel
	case "error":
		zerologLevel = zerolog.ErrorLevel
	default:
		zerologLevel = zerolog.InfoLevel
	}

	// Set global log level
	zerolog.SetGlobalLevel(zerologLevel)

	// Determine output format
	var logger zerolog.Logger

	// Check if we should use console output
	if shouldUseConsoleOutput(format) {
		// Use colored console writer for text format
		consoleWriter := zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: "2006-01-02T15:04:05.000Z",
			NoColor:    false,
		}
		logger = zerolog.New(consoleWriter).
			Level(zerologLevel).
			With().
			Timestamp().
			Str("component", component).
			Logger()
	} else {
		// Use JSON output
		zerolog.TimeFieldFormat = "2006-01-02T15:04:05.000Z"
		logger = zerolog.New(os.Stdout).
			Level(zerologLevel).
			With().
			Timestamp().
			Str("component", component).
			Logger()
	}

	return &Logger{
		Logger:    logger,
		component: component,
	}
}

// shouldUseConsoleOutput determines if we should use console (text) output
func shouldUseConsoleOutput(configFormat string) bool {
	// Priority 1: Explicit config format parameter
	switch strings.ToLower(configFormat) {
	case "text", "console":
		return true
	case "json":
		return false
	}

	// Priority 2: Environment variable
	logFormat := strings.ToLower(os.Getenv("LOG_FORMAT"))
	switch logFormat {
	case "text", "console":
		return true
	case "json":
		return false
	}

	// Priority 3: Default behavior - JSON for production, console for development
	// Use console if stdout is a terminal (development mode)
	if fileInfo, err := os.Stdout.Stat(); err == nil {
		return (fileInfo.Mode() & os.ModeCharDevice) != 0
	}

	return false
}

// NewDefault creates a logger with default settings (info level)
func NewDefault(component string) *Logger {
	globalMutex.RLock()
	level := globalLogLevel
	format := globalLogFormat
	globalMutex.RUnlock()

	return NewWithFormat(component, level, format)
}

// Debug logs a debug message with structured fields
func (l *Logger) Debug(msg string, fields ...interface{}) {
	event := l.Logger.Debug()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Info logs an info message with structured fields
func (l *Logger) Info(msg string, fields ...interface{}) {
	event := l.Logger.Info()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Warn logs a warning message with structured fields
func (l *Logger) Warn(msg string, fields ...interface{}) {
	event := l.Logger.Warn()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Error logs an error message with structured fields
func (l *Logger) Error(msg string, fields ...interface{}) {
	event := l.Logger.Error()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// WithContext returns a new logger with context values
func (l *Logger) WithContext(ctx context.Context) *Logger {
	return &Logger{
		Logger:    l.Logger.With().Logger(),
		component: l.component,
	}
}

// WithFields returns a new logger with additional default fields
func (l *Logger) WithFields(fields ...interface{}) *Logger {
	event := l.Logger.With()
	l.addFieldsToContext(event, fields...)
	return &Logger{
		Logger:    event.Logger(),
		component: l.component,
	}
}

// addFields adds key-value pairs to a zerolog event
func (l *Logger) addFields(event *zerolog.Event, fields ...interface{}) {
	for i := 0; i < len(fields); i += 2 {
		if i+1 < len(fields) {
			key, ok := fields[i].(string)
			if !ok {
				continue
			}
			value := fields[i+1]

			switch v := value.(type) {
			case string:
				event.Str(key, v)
			case int:
				event.Int(key, v)
			case int64:
				event.Int64(key, v)
			case float64:
				event.Float64(key, v)
			case bool:
				event.Bool(key, v)
			case error:
				event.Err(v)
			default:
				event.Interface(key, v)
			}
		}
	}
}

// addFieldsToContext adds key-value pairs to a zerolog context
func (l *Logger) addFieldsToContext(ctx zerolog.Context, fields ...interface{}) {
	for i := 0; i < len(fields); i += 2 {
		if i+1 < len(fields) {
			key, ok := fields[i].(string)
			if !ok {
				continue
			}
			value := fields[i+1]

			switch v := value.(type) {
			case string:
				ctx = ctx.Str(key, v)
			case int:
				ctx = ctx.Int(key, v)
			case int64:
				ctx = ctx.Int64(key, v)
			case float64:
				ctx = ctx.Float64(key, v)
			case bool:
				ctx = ctx.Bool(key, v)
			case error:
				ctx = ctx.AnErr(key, v)
			default:
				ctx = ctx.Interface(key, v)
			}
		}
	}
}

// Component returns the component name for this logger
func (l *Logger) Component() string {
	return l.component
}

// LogSpotInterruption logs spot interruption details with structured fields
func (l *Logger) LogSpotInterruption(instanceID string, timeRemaining string) {
	l.Info("Spot interruption detected",
		"instance_id", instanceID,
		"time_remaining", timeRemaining,
		"event_type", "spot_interruption",
	)
}

// LogMigrationStart logs migration start with compute type details
func (l *Logger) LogMigrationStart(currentType, targetType string) {
	l.Info("Migration started",
		"current_compute_type", currentType,
		"target_compute_type", targetType,
		"event_type", "migration_start",
	)
}

// LogMigrationComplete logs migration completion with duration
func (l *Logger) LogMigrationComplete(duration string, finalState string) {
	l.Info("Migration completed",
		"duration", duration,
		"final_state", finalState,
		"event_type", "migration_complete",
	)
}

// LogError logs error details with sufficient context for troubleshooting
func (l *Logger) LogError(operation string, err error, context ...interface{}) {
	fields := []interface{}{
		"operation", operation,
		"error", err.Error(),
		"event_type", "error",
	}

	// Add additional context fields
	fields = append(fields, context...)

	l.Error("Operation failed", fields...)
}
