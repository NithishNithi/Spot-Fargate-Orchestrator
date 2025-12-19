# Spot Fargate Orchestrator

A Kubernetes-native application that monitors AWS EC2 Spot instances for interruption notices and automatically migrates workloads to AWS Fargate to ensure zero downtime.

## Overview

The Spot Fargate Orchestrator monitors AWS EventBridge for EC2 Spot Instance Interruption Warnings, maps interrupted instances to Kubernetes nodes and affected deployments, then orchestrates seamless workload migration by patching deployments to switch from spot instances to Fargate.

## Features

- **EventBridge-Based Detection**: Monitors AWS EventBridge for EC2 Spot Instance Interruption Warnings via SQS
- **Instance-to-Node Mapping**: Maps EC2 instance IDs to Kubernetes nodes using providerID
- **Deployment-Aware Migration**: Identifies affected deployments and migrates only impacted workloads
- **Zero-Downtime Migration**: Seamlessly migrates workloads from spot to Fargate without service interruption
- **Kubernetes Native**: Uses Kubernetes APIs for deployment patching and rollout monitoring
- **State Tracking**: Uses deployment annotations as single source of truth for migration state and history
- **Health Verification**: Verifies application health after migration completion
- **Slack Alerting**: Real-time notifications for spot interruptions, migration status, and failures
- **Comprehensive Logging**: Structured logging with colored console output for development
- **Automatic Recovery**: Intelligent recovery system that migrates workloads back to spot instances when available
- **Configurable**: TOML and environment-based configuration for different deployment scenarios

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│ EventBridge     │───▶│   Spot Watcher   │───▶│   Orchestrator  │
│ Spot Events     │    │                  │    │                 │
└─────────────────┘    └──────────────────┘    └─────────────────┘
         │                       │                       │
         ▼                       ▼                       ▼
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│ SQS Queue       │    │ Instance→Node    │    │ Deployment      │
│                 │    │ Mapping          │    │ Patcher         │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                                │                       │
                                ▼                       ▼
                     ┌──────────────────┐    ┌─────────────────┐
                     │ Kubernetes API   │    │ Rollout Monitor │
                     │                  │    │                 │
                     └──────────────────┘    └─────────────────┘
                                │                       │
                                ▼                       ▼
                     ┌──────────────────┐    ┌─────────────────┐
                     │ State Manager    │    │ Health Checker  │
                     │                  │    │                 │
                     └──────────────────┘    └─────────────────┘
```

### Flow
1. **EventBridge** emits EC2 Spot Instance Interruption Warning
2. **SQS Queue** receives and buffers the event
3. **Spot Watcher** polls SQS and maps instance ID → Kubernetes node → affected pods
4. **Orchestrator** receives event and patches affected deployments
5. **Deployment Patcher** switches compute type from spot to Fargate
6. **Rollout Monitor** waits for successful deployment
7. **Health Checker** verifies migration success

## Configuration

The orchestrator supports two configuration methods:

1. **TOML Configuration File** (`config.toml`) - Recommended for development and static deployments
2. **Environment Variables** - Required for containerized deployments and CI/CD

Environment variables always take precedence over TOML configuration. If `config.toml` doesn't exist, the orchestrator falls back to environment variables and defaults.

### TOML Configuration (`config.toml`)

Create a `config.toml` file in the project root:

```toml
[kubernetes]
namespace = "default"
deployment_name = "my-app"
service_name = "my-app-service"

[monitoring]
check_interval = "5s"
health_check_retries = 3
health_check_interval = "2s"

[migration]
rollout_timeout = "120s"
verification_delay = "5s"

[recovery]
enabled = true
interval = "5m"
cooldown = "45m"
timeout = "3m"
backoff_base = "15m"
max_backoff = "4h"

[compute]
spot_label = "spot"
fargate_label = "fargate"
compute_label_key = "compute-type"

[aws]
region = "us-east-1"
sqs_queue_url = ""  # Required: Set this or use SQS_QUEUE_URL env var

[alerts]
enabled = true
slack_webhook_url = ""  # Optional: Set this or use SLACK_WEBHOOK_URL env var

[logging]
level = "info"
format = "auto"  # auto, console, json
```

### Environment Variables

Environment variables override TOML configuration:

#### Required
- `DEPLOYMENT_NAME`: Name of the Kubernetes deployment to monitor and migrate
- `SERVICE_NAME`: Name of the Kubernetes service for health verification  
- `SQS_QUEUE_URL`: SQS queue URL that receives EventBridge spot interruption events

#### Optional
- `NAMESPACE`: Kubernetes namespace (default: "default")
- `AWS_REGION`: AWS region for EventBridge and SQS (default: "us-east-1")
- `CHECK_INTERVAL`: How often to check for events (default: "5s")
- `HEALTH_CHECK_RETRIES`: Number of health check retries (default: 3)
- `HEALTH_CHECK_INTERVAL`: Time between health checks (default: "2s")
- `ROLLOUT_TIMEOUT`: Maximum time to wait for rollout completion (default: "120s")
- `VERIFICATION_DELAY`: Delay before verifying migration (default: "5s")
- `LOG_LEVEL`: Logging level - debug, info, warn, error (default: "info")
- `SLACK_WEBHOOK_URL`: Slack webhook URL for alerts (optional)
- `ALERTS_ENABLED`: Enable/disable Slack alerts (default: "true")
- `SPOT_LABEL`: Label value for spot instances (default: "spot")
- `FARGATE_LABEL`: Label value for Fargate (default: "fargate")
- `COMPUTE_LABEL_KEY`: Label key for compute type (default: "compute-type")

#### Recovery System (Optional)
- `RECOVERY_ENABLED`: Enable automatic recovery to spot instances (default: "true")
- `RECOVERY_INTERVAL`: How often to check for recovery opportunities (default: "5m")
- `RECOVERY_COOLDOWN`: Wait time after migration before attempting recovery (default: "45m")
- `RECOVERY_TIMEOUT`: Maximum time to wait for recovery rollout (default: "3m")
- `RECOVERY_BACKOFF_BASE`: Base backoff time for failed recovery attempts (default: "15m")
- `RECOVERY_MAX_BACKOFF`: Maximum backoff time for failed recovery attempts (default: "4h")

## Building

```bash
# Build the application
go build -o orchestrator ./cmd/orchestrator

# Build Docker image
docker build -t spot-fargate-orchestrator .
```

## Running

### Local Development

#### Option 1: Using TOML Configuration (Recommended)
```bash
# 1. Copy and customize the config file
cp config.toml config.toml.local
# Edit config.toml.local with your values

# 2. Set only required environment variables that aren't in TOML
export SQS_QUEUE_URL=https://sqs.us-east-1.amazonaws.com/123456789012/spot-interruption-queue

# 3. Run the orchestrator
./orchestrator
```

#### Option 2: Using Environment Variables
```bash
# Set required environment variables
export DEPLOYMENT_NAME=my-app
export SERVICE_NAME=my-app-service
export SQS_QUEUE_URL=https://sqs.us-east-1.amazonaws.com/123456789012/spot-interruption-queue

# Optional: Enable Slack alerts
export SLACK_WEBHOOK_URL=https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX

# Run the orchestrator
./orchestrator
```

### Kubernetes Deployment
```bash
# Apply Kubernetes manifests
kubectl apply -f deployments/kubernetes/
```

## Requirements

- Go 1.21+
- Kubernetes cluster with appropriate RBAC permissions
- AWS EventBridge rule configured to send EC2 Spot Instance Interruption Warnings to SQS
- SQS queue to receive EventBridge events
- AWS IAM permissions for SQS access
- Target deployment must support compute type switching via labels and node selectors

## AWS Setup

### 1. Create SQS Queue
```bash
aws sqs create-queue --queue-name spot-interruption-queue
```

### 2. Create EventBridge Rule
```bash
aws events put-rule \
  --name spot-interruption-rule \
  --event-pattern '{
    "source": ["aws.ec2"],
    "detail-type": ["EC2 Spot Instance Interruption Warning"]
  }'
```

### 3. Add SQS Target to EventBridge Rule
```bash
aws events put-targets \
  --rule spot-interruption-rule \
  --targets "Id"="1","Arn"="arn:aws:sqs:us-east-1:123456789012:spot-interruption-queue"
```

### 4. Configure IAM Permissions
The orchestrator needs SQS permissions:
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "sqs:ReceiveMessage",
        "sqs:DeleteMessage",
        "sqs:DeleteMessageBatch"
      ],
      "Resource": "arn:aws:sqs:us-east-1:123456789012:spot-interruption-queue"
    }
  ]
}
```

## Recovery System

The orchestrator includes an intelligent recovery system that automatically migrates workloads back to spot instances when conditions are favorable, optimizing costs while maintaining reliability.

### Recovery Logic

1. **Automatic Detection**: Monitors deployments currently running on Fargate
2. **Spot Availability Check**: Verifies spot nodes are available in the cluster
3. **Cooldown Enforcement**: Respects configurable cooldown period after initial migration
4. **Exponential Backoff**: Implements backoff strategy for failed recovery attempts
5. **Health Verification**: Ensures successful migration before completing recovery
6. **Rollback on Failure**: Automatically rolls back to Fargate if recovery fails

### Recovery Flow

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│ Recovery Timer  │───▶│ Check Conditions │───▶│ Attempt Recovery│
│ (5min interval) │    │ - On Fargate?    │    │ to Spot         │
└─────────────────┘    │ - Cooldown OK?   │    └─────────────────┘
                       │ - Spot Available?│             │
                       └──────────────────┘             ▼
                                │                ┌─────────────────┐
                                ▼                │ Health Check    │
                       ┌──────────────────┐      │ & Verification  │
                       │ Skip Recovery    │      └─────────────────┘
                       │ (Conditions not  │               │
                       │  met)            │               ▼
                       └──────────────────┘      ┌─────────────────┐
                                                 │ Success: Stay   │
                                                 │ on Spot         │
                                                 └─────────────────┘
                                                          │
                                                          ▼
                                                 ┌─────────────────┐
                                                 │ Failure: Rollback│
                                                 │ to Fargate      │
                                                 └─────────────────┘
```

### Recovery Configuration

- **Interval**: How often to check for recovery opportunities (default: 5 minutes)
- **Cooldown**: Minimum time to wait after migration before attempting recovery (default: 45 minutes)
- **Timeout**: Maximum time to wait for recovery rollout completion (default: 3 minutes)
- **Backoff**: Exponential backoff for failed attempts (base: 15 minutes, max: 4 hours)

### Disabling Recovery

To disable automatic recovery, set `RECOVERY_ENABLED=false` or in `config.toml`:

```toml
[recovery]
enabled = false
```

## State Management

The orchestrator uses deployment annotations as the single source of truth for tracking migration state and history:

### Annotations

- `spot-orchestrator/state`: Current compute state (`spot` | `fargate`)
- `spot-orchestrator/migrated-at`: RFC3339 timestamp of last migration
- `spot-orchestrator/reason`: Migration reason (`spot-interruption` | `manual` | `rollback`)
- `spot-orchestrator.io/last-recovered-at`: RFC3339 timestamp of last recovery attempt
- `spot-orchestrator.io/recovery-reason`: Reason for recovery attempt
- `spot-orchestrator.io/failed-recovery-attempts`: Number of consecutive failed recovery attempts

### State Management CLI

Use the `deployment-state` tool to inspect and manage deployment state:

```bash
# Build the state management tool
go build -o deployment-state ./cmd/deployment-state

# Get current deployment state
./deployment-state get

# Manually set state (for testing or manual migrations)
./deployment-state set fargate manual
./deployment-state set spot rollback
```

### Example Annotations

After a spot interruption migration, your deployment will have:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    spot-orchestrator/state: "fargate"
    spot-orchestrator/migrated-at: "2024-01-15T10:30:45Z"
    spot-orchestrator/reason: "spot-interruption"
```

## Development

The project follows a modular architecture with clear separation of concerns:

- `cmd/`: Application entry points and CLI tools
- `internal/`: Internal packages not intended for external use
  - `kubernetes/annotations/`: Deployment state management via annotations
  - `kubernetes/patcher/`: Deployment patching with state tracking
  - `orchestrator/`: Main orchestration logic with annotation integration
- `pkg/`: Reusable packages (future use)
- `deployments/`: Kubernetes and Docker deployment configurations

## License

[Add your license information here]

## Contributing

[Add contributing guidelines here]