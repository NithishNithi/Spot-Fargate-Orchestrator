# Implementation Plan

- [x] 1. Set up project structure and core interfaces
  - Create Go module with required dependencies (k8s.io/client-go, k8s.io/api, k8s.io/apimachinery)
  - Create directory structure: cmd/, internal/, pkg/, deployments/
  - Initialize basic project files (go.mod, Dockerfile, README.md)
  - _Requirements: 7.1, 7.4_

- [x] 2. Implement configuration management
  - Create Config struct with all required fields and environment variable bindings
  - Implement LoadConfig() function with environment variable parsing
  - Add configuration validation with meaningful error messages for missing required fields
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5_

- [ ]* 2.1 Write property test for configuration validation
  - **Property 7: Configuration validation robustness**
  - **Validates: Requirements 7.2**

- [x] 3. Implement structured logging system
  - Create Logger struct using Go's slog package for structured logging
  - Implement logging methods (Debug, Info, Warn, Error) with structured fields
  - Add support for component-specific loggers with default fields
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5_

- [ ]* 3.1 Write property test for structured logging
  - **Property 5: Structured logging completeness**
  - **Validates: Requirements 5.1, 5.5**

- [x] 4. Implement EC2 metadata client
  - Create MetadataClient struct with HTTP client and timeout configuration
  - Implement GetSpotInterruptionNotice() with proper 404 handling and JSON parsing
  - Add GetInstanceID() and IsSpotInstance() methods for instance identification
  - _Requirements: 1.1, 1.3, 1.4, 1.5_

- [ ]* 4.1 Write property test for metadata polling
  - **Property 1: Metadata polling consistency**
  - **Validates: Requirements 1.2, 1.4, 1.5**

- [x] 5. Implement spot watcher with event system
  - Create SpotWatcher struct with configurable polling interval
  - Implement continuous monitoring loop with context cancellation support
  - Add event channel for spot interruption notifications with time remaining calculation
  - _Requirements: 1.2, 1.4, 1.5_

- [x] 6. Implement Kubernetes client wrapper
  - Create K8sClient struct with in-cluster and out-of-cluster configuration detection
  - Initialize Kubernetes clientset with proper error handling
  - Add namespace management and client accessor methods
  - _Requirements: 2.1, 3.1, 4.5_

- [x] 7. Implement deployment patcher
  - Create DeploymentPatcher with JSON patch generation for compute type changes
  - Implement PatchComputeType() method with spot-to-Fargate and Fargate-to-spot logic
  - Add proper nodeSelector and affinity handling for different compute types
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5_

- [ ]* 7.1 Write property test for deployment patching
  - **Property 2: Deployment patch correctness**
  - **Validates: Requirements 2.1, 2.2, 2.3, 2.4, 2.5**

- [x] 8. Implement rollout monitoring
  - Create RolloutMonitor struct for tracking deployment rollout status
  - Implement WaitForRollout() with timeout handling and completion detection
  - Add GetRolloutStatus() method for real-time rollout state checking
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5_

- [ ]* 8.1 Write property test for rollout monitoring
  - **Property 3: Rollout monitoring accuracy**
  - **Validates: Requirements 3.2, 3.3, 3.4, 3.5**

- [x] 9. Implement health checker
  - Create HealthChecker struct with configurable retry logic and HTTP client
  - Implement CheckPodHealth() and CheckServiceHealth() methods with proper status code handling
  - Add retry mechanism with configurable attempts and intervals
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5_

- [ ]* 9.1 Write property test for health checking
  - **Property 4: Health check reliability**
  - **Validates: Requirements 4.1, 4.2, 4.3, 4.4**

- [x] 10. Implement state management
  - Create State struct with thread-safe operations using sync.RWMutex
  - Implement state tracking methods for compute type, migration history, and status
  - Add GetState() method for atomic state snapshots
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5_

- [ ]* 10.1 Write property test for state management
  - **Property 6: State management thread safety**
  - **Validates: Requirements 6.2, 6.3, 6.4, 6.5**

- [x] 11. Implement main orchestrator logic
  - Create Orchestrator struct that coordinates all components
  - Implement Start() method with event loop for handling spot interruption events
  - Add handleSpotInterruption() method with complete migration workflow
  - _Requirements: 1.2, 2.1, 3.1, 4.1, 5.2, 5.3, 5.4, 6.2, 6.3_

- [x] 12. Implement migration workflow
  - Create migrateToFargate() method with deployment patching and rollout monitoring
  - Add verifyMigration() method with health checks and service endpoint verification
  - Implement error handling and rollback capabilities for failed migrations
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5_

- [ ]* 12.1 Write property test for zero-downtime migration
  - **Property 8: Zero-downtime migration preservation**
  - **Validates: Requirements 8.2, 8.4**

- [x] 13. Create main application entry point
  - Implement main.go with configuration loading, signal handling, and graceful shutdown
  - Add startup validation to check if running on spot instance
  - Initialize orchestrator and start monitoring loop with proper error handling
  - _Requirements: 1.1, 7.1, 7.2_

- [ ] 14. Checkpoint - Ensure all tests pass
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 15. Create Kubernetes deployment manifests
  - Create ServiceAccount, Role, and RoleBinding for required Kubernetes permissions
  - Implement Deployment manifest for orchestrator with Fargate nodeSelector
  - Add environment variable configuration for target deployment and service
  - _Requirements: 2.1, 3.1, 4.5, 7.1, 7.4_

- [ ] 16. Create Docker configuration
  - Write Dockerfile with multi-stage build for Go application
  - Add proper security configurations and non-root user
  - Optimize image size and build time
  - _Requirements: 7.1_

- [ ]* 17. Write integration tests
  - Create integration tests using test Kubernetes cluster (kind/minikube)
  - Test complete migration workflow with mock spot interruption scenarios
  - Verify zero-downtime behavior and proper error handling

- [ ]* 18. Write unit tests for core components
  - Create unit tests for metadata client with mock HTTP responses
  - Add unit tests for deployment patcher with mock Kubernetes API
  - Write unit tests for rollout monitor and health checker
  - Test error conditions and edge cases for all components

- [ ] 19. Final checkpoint - Ensure all tests pass
  - Ensure all tests pass, ask the user if questions arise.