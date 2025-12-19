# Design Document: Spot Fargate Orchestrator

## Overview

The Spot Fargate Orchestrator is a Kubernetes-native Go application that provides automated workload migration from AWS EC2 Spot instances to AWS Fargate when spot interruption notices are detected. The system operates as a continuous monitoring service that polls the EC2 metadata service, detects impending spot instance terminations, and orchestrates seamless workload migration through Kubernetes deployment patching.

The architecture follows a event-driven design where spot interruption detection triggers a coordinated migration workflow involving deployment patching, rollout monitoring, and health verification to ensure zero-downtime transitions.

## Architecture

The system follows a modular, event-driven architecture with clear separation of concerns:

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   Spot Watcher  │───▶│   Orchestrator   │───▶│ Deployment      │
│                 │    │                  │    │ Patcher         │
└─────────────────┘    └──────────────────┘    └─────────────────┘
         │                       │                       │
         ▼                       ▼                       ▼
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│ EC2 Metadata    │    │ State Manager    │    │ Kubernetes API  │
│ Service         │    │                  │    │                 │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                                │                       │
                                ▼                       ▼
                     ┌──────────────────┐    ┌─────────────────┐
                     │ Rollout Monitor  │    │ Health Checker  │
                     │                  │    │                 │
                     └──────────────────┘    └─────────────────┘
```

**Key Architectural Principles:**
- **Event-driven**: Spot interruption events trigger migration workflows
- **Modular**: Each component has a single responsibility
- **Resilient**: Graceful error handling and rollback capabilities
- **Observable**: Comprehensive logging and state tracking
- **Configurable**: Environment-specific parameter tuning

## Components and Interfaces

### 1. Configuration Manager (`internal/config`)
**Purpose**: Centralized configuration loading and validation

**Interface**:
```go
type Config struct {
    // Kubernetes Configuration
    Namespace           string
    DeploymentName      string
    ServiceName         string
    
    // Monitoring Configuration
    CheckInterval       time.Duration
    HealthCheckRetries  int
    HealthCheckInterval time.Duration
    
    // Migration Configuration
    RolloutTimeout      time.Duration
    VerificationDelay   time.Duration
    
    // Compute Configuration
    SpotLabel           string
    FargateLabel        string
    ComputeLabelKey     string
    
    // EC2 Metadata Configuration
    MetadataTimeout     time.Duration
    
    // Logging Configuration
    LogLevel            string
}

func LoadConfig() (*Config, error)
func (c *Config) Validate() error
```

### 2. EC2 Metadata Client (`internal/spot/metadata`)
**Purpose**: Interface with AWS EC2 metadata service

**Interface**:
```go
type MetadataClient struct {
    endpoint   string
    httpClient *http.Client
}

type InterruptionNotice struct {
    Action string    `json:"action"`
    Time   time.Time `json:"time"`
}

func NewMetadataClient(endpoint string, timeout time.Duration) *MetadataClient
func (m *MetadataClient) GetSpotInterruptionNotice() (*InterruptionNotice, error)
func (m *MetadataClient) GetInstanceID() (string, error)
func (m *MetadataClient) IsSpotInstance() (bool, error)
```

### 3. Spot Watcher (`internal/spot/watcher`)
**Purpose**: Continuous monitoring for spot interruption notices

**Interface**:
```go
type SpotWatcher struct {
    metadataClient *MetadataClient
    checkInterval  time.Duration
    eventChan      chan SpotEvent
    logger         *logger.Logger
}

type SpotEvent struct {
    Type           EventType
    Timestamp      time.Time
    Notice         *InterruptionNotice
    TimeRemaining  time.Duration
}

func NewSpotWatcher(client *MetadataClient, interval time.Duration) *SpotWatcher
func (w *SpotWatcher) Start(ctx context.Context) error
func (w *SpotWatcher) EventChannel() <-chan SpotEvent
```

### 4. Kubernetes Client (`internal/kubernetes/client`)
**Purpose**: Kubernetes API client wrapper

**Interface**:
```go
type K8sClient struct {
    clientset *kubernetes.Clientset
    namespace string
    logger    *logger.Logger
}

func NewK8sClient(namespace string) (*K8sClient, error)
func (k *K8sClient) GetClientset() *kubernetes.Clientset
func (k *K8sClient) GetNamespace() string
```

### 5. Deployment Patcher (`internal/kubernetes/patcher`)
**Purpose**: Kubernetes deployment modification via JSON patches

**Interface**:
```go
type DeploymentPatcher struct {
    client *K8sClient
    logger *logger.Logger
}

type PatchOperation struct {
    Op    string      `json:"op"`
    Path  string      `json:"path"`
    Value interface{} `json:"value"`
}

func NewDeploymentPatcher(client *K8sClient) *DeploymentPatcher
func (p *DeploymentPatcher) PatchComputeType(ctx context.Context, deploymentName, computeType string) error
```

### 6. Rollout Monitor (`internal/kubernetes/monitor`)
**Purpose**: Real-time deployment rollout status tracking

**Interface**:
```go
type RolloutMonitor struct {
    client *K8sClient
    logger *logger.Logger
}

type RolloutStatus struct {
    Complete bool
    Failed   bool
    Message  string
    Replicas RolloutReplicas
}

func NewRolloutMonitor(client *K8sClient) *RolloutMonitor
func (r *RolloutMonitor) WaitForRollout(ctx context.Context, deploymentName string, timeout time.Duration) error
func (r *RolloutMonitor) GetRolloutStatus(ctx context.Context, deploymentName string) (*RolloutStatus, error)
```

### 7. Health Checker (`internal/health/checker`)
**Purpose**: Application health verification via HTTP endpoints

**Interface**:
```go
type HealthChecker struct {
    httpClient *http.Client
    retries    int
    interval   time.Duration
    logger     *logger.Logger
}

type HealthCheckResult struct {
    Healthy      bool
    StatusCode   int
    ResponseTime time.Duration
    Error        error
}

func NewHealthChecker(retries int, interval time.Duration) *HealthChecker
func (h *HealthChecker) CheckPodHealth(podIP string, port int, path string) (*HealthCheckResult, error)
func (h *HealthChecker) CheckServiceHealth(serviceName, namespace string, port int, path string) (*HealthCheckResult, error)
```

### 8. State Manager (`internal/orchestrator/state`)
**Purpose**: Thread-safe application state tracking

**Interface**:
```go
type State struct {
    mu                 sync.RWMutex
    CurrentComputeType string
    LastMigration      time.Time
    MigrationCount     int
    LastError          string
    Status             string
}

func NewState() *State
func (s *State) GetCurrentComputeType() string
func (s *State) SetCurrentComputeType(computeType string)
func (s *State) RecordMigration()
func (s *State) SetStatus(status string)
```

### 9. Main Orchestrator (`internal/orchestrator/orchestrator`)
**Purpose**: Central coordination of all migration workflows

**Interface**:
```go
type Orchestrator struct {
    config         *config.Config
    spotWatcher    *spot.SpotWatcher
    patcher        *kubernetes.DeploymentPatcher
    monitor        *kubernetes.RolloutMonitor
    healthChecker  *health.HealthChecker
    k8sClient      *kubernetes.K8sClient
    state          *State
    logger         *logger.Logger
}

func NewOrchestrator(cfg *config.Config) (*Orchestrator, error)
func (o *Orchestrator) Start(ctx context.Context) error
func (o *Orchestrator) handleSpotInterruption(ctx context.Context, event spot.SpotEvent) error
```

## Data Models

### Core Data Structures

**Configuration Model**:
```go
type Config struct {
    Namespace           string        `env:"NAMESPACE" default:"default"`
    DeploymentName      string        `env:"DEPLOYMENT_NAME" required:"true"`
    ServiceName         string        `env:"SERVICE_NAME" required:"true"`
    CheckInterval       time.Duration `env:"CHECK_INTERVAL" default:"5s"`
    HealthCheckRetries  int          `env:"HEALTH_CHECK_RETRIES" default:"3"`
    HealthCheckInterval time.Duration `env:"HEALTH_CHECK_INTERVAL" default:"2s"`
    RolloutTimeout      time.Duration `env:"ROLLOUT_TIMEOUT" default:"120s"`
    VerificationDelay   time.Duration `env:"VERIFICATION_DELAY" default:"5s"`
    SpotLabel           string        `env:"SPOT_LABEL" default:"spot"`
    FargateLabel        string        `env:"FARGATE_LABEL" default:"fargate"`
    ComputeLabelKey     string        `env:"COMPUTE_LABEL_KEY" default:"compute-type"`
    MetadataTimeout     time.Duration `env:"METADATA_TIMEOUT" default:"2s"`
    LogLevel            string        `env:"LOG_LEVEL" default:"info"`
}
```

**Event Model**:
```go
type SpotEvent struct {
    Type           EventType         `json:"type"`
    Timestamp      time.Time         `json:"timestamp"`
    Notice         *InterruptionNotice `json:"notice,omitempty"`
    TimeRemaining  time.Duration     `json:"time_remaining"`
    InstanceID     string           `json:"instance_id"`
}

type EventType string
const (
    EventSpotInterruption EventType = "SPOT_INTERRUPTION"
    EventHealthCheck      EventType = "HEALTH_CHECK"
)
```

**State Model**:
```go
type StateSnapshot struct {
    CurrentComputeType string    `json:"current_compute_type"`
    LastMigration      time.Time `json:"last_migration"`
    MigrationCount     int       `json:"migration_count"`
    LastError          string    `json:"last_error,omitempty"`
    Status             string    `json:"status"`
    Timestamp          time.Time `json:"timestamp"`
}
```

**Patch Model**:
```go
type ComputeTypePatch struct {
    Operations []PatchOperation `json:"operations"`
    Target     string          `json:"target"`
    Source     string          `json:"source"`
}

type PatchOperation struct {
    Op    string      `json:"op"`    // "replace", "add", "remove"
    Path  string      `json:"path"`  // JSON path
    Value interface{} `json:"value"` // New value
}
```

### Migration Workflow State Machine

```
┌─────────────┐    spot interruption    ┌─────────────┐
│   HEALTHY   │────────────────────────▶│  MIGRATING  │
│   (spot)    │                         │             │
└─────────────┘                         └─────────────┘
       ▲                                       │
       │                                       │ success
       │ rollback/recovery                     ▼
┌─────────────┐                         ┌─────────────┐
│    ERROR    │                         │   HEALTHY   │
│             │◀────────────────────────│  (fargate)  │
└─────────────┘         failure         └─────────────┘
```

**State Transitions**:
- `HEALTHY (spot)` → `MIGRATING`: Spot interruption detected
- `MIGRATING` → `HEALTHY (fargate)`: Migration completed successfully
- `MIGRATING` → `ERROR`: Migration failed
- `ERROR` → `HEALTHY (spot)`: Manual recovery or rollback
- `HEALTHY (fargate)` → `MIGRATING`: Cost optimization migration back to spot
## Correctness Properties

*A property is a characteristic or behavior that should hold true across all valid executions of a system-essentially, a formal statement about what the system should do. Properties serve as the bridge between human-readable specifications and machine-verifiable correctness guarantees.*

### Property Reflection

After analyzing all acceptance criteria, several properties can be consolidated to eliminate redundancy:

- **Polling and timing properties** can be combined into a single comprehensive polling behavior property
- **Patch generation properties** for different compute types can be unified into a single patch correctness property
- **Health check properties** can be consolidated into comprehensive health verification properties
- **State management properties** can be combined into thread-safety and state consistency properties

### Core Properties

**Property 1: Metadata polling consistency**
*For any* configured check interval, the metadata service should be queried at regular intervals matching the configuration, and interruption notices should be correctly parsed when present
**Validates: Requirements 1.2, 1.4, 1.5**

**Property 2: Deployment patch correctness**
*For any* deployment and target compute type, the generated JSON patch should correctly update the compute-type label and appropriate node selection mechanisms (nodeSelector for Fargate, affinity for spot)
**Validates: Requirements 2.1, 2.2, 2.3, 2.4, 2.5**

**Property 3: Rollout monitoring accuracy**
*For any* deployment rollout, the monitor should correctly track replica counts and detect completion when all replicas are updated, ready, and available with the correct deployment conditions
**Validates: Requirements 3.2, 3.3, 3.4, 3.5**

**Property 4: Health check reliability**
*For any* set of pods and health endpoints, health checks should correctly identify healthy pods (status codes 200-299) and retry failed checks up to the configured limit
**Validates: Requirements 4.1, 4.2, 4.3, 4.4**

**Property 5: Structured logging completeness**
*For any* system operation, log messages should contain required structured fields (timestamp, level, component) and operation-specific context information
**Validates: Requirements 5.1, 5.5**

**Property 6: State management thread safety**
*For any* concurrent access pattern, state operations should be thread-safe and provide consistent snapshots without blocking other operations
**Validates: Requirements 6.2, 6.3, 6.4, 6.5**

**Property 7: Configuration validation robustness**
*For any* missing required configuration parameters, the system should return meaningful validation errors that identify the specific missing or invalid fields
**Validates: Requirements 7.2**

**Property 8: Zero-downtime migration preservation**
*For any* migration scenario, old pods should remain available until new pods are verified healthy, and service endpoints should always route to healthy pods
**Validates: Requirements 8.2, 8.4**

## Error Handling

### Error Categories and Strategies

**1. Network Errors**
- **EC2 Metadata Service Unreachable**: Retry with exponential backoff, continue monitoring
- **Kubernetes API Unreachable**: Retry with exponential backoff, alert after threshold
- **Health Check Failures**: Retry up to configured limit, consider rollback if persistent

**2. Configuration Errors**
- **Missing Required Configuration**: Fail fast with detailed error messages
- **Invalid Configuration Values**: Validate at startup, provide correction suggestions
- **Kubernetes Resource Not Found**: Alert operators, attempt recovery

**3. Migration Errors**
- **Patch Application Failure**: Retry once, alert if still failing, preserve existing workload
- **Rollout Timeout**: Alert with diagnostic information, maintain old pods
- **Health Check Failures**: Consider rollback, alert operations team

**4. Concurrency Errors**
- **State Access Conflicts**: Use mutex-based synchronization, ensure atomic operations
- **Multiple Migration Attempts**: Use state machine to prevent concurrent migrations

### Error Recovery Patterns

**Graceful Degradation**:
```go
// Example error handling pattern
func (o *Orchestrator) handleSpotInterruption(ctx context.Context, event SpotEvent) error {
    // Set state to migrating
    o.state.SetStatus("migrating")
    
    // Attempt migration with rollback on failure
    if err := o.migrateToFargate(ctx); err != nil {
        o.logger.Error("Migration failed, maintaining current state", 
            "error", err, "time_remaining", event.TimeRemaining)
        o.state.SetStatus("error")
        o.state.SetLastError(err.Error())
        
        // Don't fail completely - keep monitoring
        return nil
    }
    
    o.state.SetStatus("healthy")
    return nil
}
```

**Circuit Breaker Pattern**:
- Implement circuit breakers for external service calls (EC2 metadata, Kubernetes API)
- Open circuit after consecutive failures, half-open for testing recovery
- Prevent cascading failures and resource exhaustion

## Testing Strategy

### Dual Testing Approach

The testing strategy employs both unit testing and property-based testing to ensure comprehensive coverage:

**Unit Testing**:
- Test specific examples and edge cases for each component
- Mock external dependencies (Kubernetes API, EC2 metadata service)
- Verify integration points between components
- Test error conditions and failure scenarios

**Property-Based Testing**:
- Use **Testify** and **gopter** libraries for property-based testing in Go
- Configure each property-based test to run a minimum of 100 iterations
- Tag each property-based test with comments referencing design document properties
- Use format: `**Feature: spot-fargate-orchestrator, Property {number}: {property_text}**`

**Testing Framework Configuration**:
```go
// Property-based test configuration
func TestProperty(t *testing.T) {
    parameters := gopter.DefaultTestParameters()
    parameters.MinSuccessfulTests = 100
    parameters.MaxSize = 50
    
    properties := gopter.NewProperties(parameters)
    // Test implementation
}
```

**Key Testing Areas**:

1. **Metadata Client Testing**:
   - Unit tests: Specific JSON responses, 404 handling, timeout scenarios
   - Property tests: Various valid interruption notices, timeout handling

2. **Deployment Patcher Testing**:
   - Unit tests: Specific patch operations, Kubernetes API errors
   - Property tests: Patch correctness across different deployment configurations

3. **Rollout Monitor Testing**:
   - Unit tests: Specific rollout states, timeout conditions
   - Property tests: Rollout detection across various deployment scenarios

4. **Health Checker Testing**:
   - Unit tests: Specific HTTP responses, retry scenarios
   - Property tests: Health determination across various response patterns

5. **State Management Testing**:
   - Unit tests: Specific state transitions, error conditions
   - Property tests: Thread safety under concurrent access patterns

6. **Integration Testing**:
   - End-to-end migration workflows using test Kubernetes clusters
   - Mock spot interruption scenarios
   - Verify zero-downtime behavior

**Test Data Generation**:
- Generate realistic Kubernetes deployment specifications
- Create varied interruption notice scenarios
- Simulate different network failure patterns
- Generate concurrent access patterns for state testing

### Testing Requirements

- Each correctness property MUST be implemented by a SINGLE property-based test
- Property-based tests MUST be tagged with explicit references to design document properties
- Unit tests and property tests are complementary and both MUST be included
- Property-based tests MUST run a minimum of 100 iterations
- Tests MUST verify real functionality without mocks where possible for core logic