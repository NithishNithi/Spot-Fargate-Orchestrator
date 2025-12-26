# 🚀 Quick Start Guide

Get the Spot Fargate Orchestrator running in 5 minutes! This guide covers everything you need to deploy and configure the orchestrator.

## 📋 Prerequisites

Before starting, ensure you have:

1. **Kubernetes Cluster** with RBAC permissions
2. **AWS SQS Queue** configured to receive EventBridge spot interruption events
3. **Docker** (for containerized deployment) OR **Go 1.23+** (for source build)
4. **kubectl** configured to access your cluster

## 🎯 Step 1: Choose Your Deployment Method

### Option A: Docker Compose (Recommended for Local Development)

**1. Clone and Setup**
```bash
git clone <your-repo-url>
cd spot-fargate-orchestrator/deployments
cp .env.example .env
```

**2. Configure Environment**
Edit `.env` file with your settings:
```bash
# Required: Your AWS SQS queue URL
SQS_QUEUE_URL=https://sqs.us-east-1.amazonaws.com/xxx/spot-interruption-queue

# Required: AWS region
AWS_REGION=us-east-1

# Recommended: Use namespace mode
MODE=namespace
NAMESPACE=default

# Optional: Slack alerts
SLACK_WEBHOOK_URL=https://hooks.slack.com/services/YOUR/SLACK/WEBHOOK
```

**3. Start the Orchestrator**
```bash
docker-compose up -d
```

**4. Verify it's Running**
```bash
docker-compose logs -f spot-orchestrator
```

### Option B: Kubernetes Deployment (Recommended for Production)

**1. Download the Deployment File**
```bash
wget https://raw.githubusercontent.com/your-repo/spot-fargate-orchestrator/main/deployments/kubernetes/spot-orchestrator.yaml
```

**2. Configure Secrets**
Edit the Secret section in `spot-orchestrator.yaml`:
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: spot-orchestrator-secrets
  namespace: spot-orchestrator
stringData:
  SQS_QUEUE_URL: "https://sqs.us-east-1.amazonaws.com/xxx/spot-interruption-queue"
  SLACK_WEBHOOK_URL: "https://hooks.slack.com/services/YOUR/SLACK/WEBHOOK"  # Optional
```

**3. Deploy to Kubernetes**
```bash
kubectl apply -f spot-orchestrator.yaml
```

**4. Verify Deployment**
```bash
kubectl get pods -n spot-orchestrator
kubectl logs -n spot-orchestrator deployment/spot-orchestrator -f
```

### Option C: Build from Source

**1. Clone and Build**
```bash
git clone <your-repo-url>
cd spot-fargate-orchestrator
go build -o orchestrator .
```

**2. Set Environment Variables**
```bash
# Required configuration
export SQS_QUEUE_URL=https://sqs.us-east-1.amazonaws.com/xxx/spot-interruption-queue
export AWS_REGION=us-east-1
export MODE=namespace
export NAMESPACE=default

# Optional: Slack alerts
export SLACK_WEBHOOK_URL=https://hooks.slack.com/services/YOUR/SLACK/WEBHOOK
```

**3. Run the Orchestrator**
```bash
./orchestrator
```

## 🎯 Step 2: Configure Your Deployments

To enable orchestrator management for your deployments, add this annotation:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-application
  annotations:
    spot-orchestrator/enabled: "true"  # Required: Opt-in to management
spec:
  # Your deployment spec...
```

### Per-Deployment Configuration

Each deployment can override global settings via annotations:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: critical-api
  annotations:
    spot-orchestrator/enabled: "true"                    # Required: Opt-in to management
    spot-orchestrator/recovery-cooldown: "15m"           # Custom recovery cooldown
    spot-orchestrator/rollout-timeout: "60s"             # Custom rollout timeout
    spot-orchestrator/verification-delay: "10s"          # Custom verification delay
    spot-orchestrator/allow-fargate: "true"              # Allow Fargate migration
    spot-orchestrator/recovery-enabled: "true"           # Enable automatic recovery
spec:
  # Your deployment spec...
```

### Available Configuration Annotations

| Annotation | Type | Description | Example |
|------------|------|-------------|---------|
| `spot-orchestrator/enabled` | `bool` | **Required**: Opt-in to orchestrator management | `"true"` |
| `spot-orchestrator/recovery-cooldown` | `duration` | Override global recovery cooldown | `"15m"`, `"2h"` |
| `spot-orchestrator/allow-fargate` | `bool` | Allow migration to Fargate (false = spot-only) | `"false"` |
| `spot-orchestrator/recovery-enabled` | `bool` | Enable automatic recovery to spot | `"false"` |
| `spot-orchestrator/rollout-timeout` | `duration` | Override rollout timeout | `"60s"`, `"3m"` |
| `spot-orchestrator/verification-delay` | `duration` | Override verification delay | `"10s"`, `"30s"` |

### Configuration Examples

#### Critical API (Fast Recovery)
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: critical-api
  annotations:
    spot-orchestrator/enabled: "true"
    spot-orchestrator/recovery-cooldown: "15m"      # Fast recovery
    spot-orchestrator/rollout-timeout: "60s"        # Fast rollout
    spot-orchestrator/verification-delay: "10s"     # Extra verification
```

#### Batch Processing (Spot-Only)
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: batch-processor
  annotations:
    spot-orchestrator/enabled: "true"
    spot-orchestrator/allow-fargate: "false"        # NEVER migrate to Fargate
    spot-orchestrator/recovery-enabled: "false"     # No recovery needed
```

#### Standard Web App (Global Config)
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-frontend
  annotations:
    spot-orchestrator/enabled: "true"               # Use global config defaults
```

## 🎯 Step 3: Set Up AWS EventBridge (Required)

The orchestrator needs AWS EventBridge to detect spot interruptions. Run this setup:

**1. Create SQS Queue**
```bash
aws sqs create-queue \
  --queue-name spot-interruption-queue \
  --region us-east-1
```

**2. Create EventBridge Rule**
```bash
aws events put-rule \
  --name spot-interruption-rule \
  --event-pattern '{
    "source": ["aws.ec2"],
    "detail-type": ["EC2 Spot Instance Interruption Warning"]
  }' \
  --region us-east-1
```

**3. Connect EventBridge to SQS**
```bash
# Get your queue ARN
QUEUE_ARN=$(aws sqs get-queue-attributes \
  --queue-url https://sqs.us-east-1.amazonaws.com/xxx/spot-interruption-queue \
  --attribute-names QueueArn \
  --query 'Attributes.QueueArn' \
  --output text)

# Add SQS as target
aws events put-targets \
  --rule spot-interruption-rule \
  --targets "Id"="1","Arn"="$QUEUE_ARN" \
  --region us-east-1
```

**4. Set SQS Permissions**
```bash
aws sqs set-queue-attributes \
  --queue-url https://sqs.us-east-1.amazonaws.com/xxx/spot-interruption-queue \
  --attributes '{
    "Policy": "{
      \"Version\": \"2012-10-17\",
      \"Statement\": [{
        \"Effect\": \"Allow\",
        \"Principal\": {\"Service\": \"events.amazonaws.com\"},
        \"Action\": \"sqs:SendMessage\",
        \"Resource\": \"'$QUEUE_ARN'\"
      }]
    }"
  }'
```

## ⚙️ Configuration

The orchestrator supports a **three-layer configuration system** with clear precedence:

1. **Environment Variables** (Highest Priority) - Override everything
2. **TOML Configuration File** (`config.toml`) - Recommended for development
3. **Built-in Defaults** (Lowest Priority) - Safe fallbacks

### Environment Variables

#### Required
- `SQS_QUEUE_URL`: SQS queue URL that receives EventBridge spot interruption events
- `AWS_REGION`: AWS region for EventBridge and SQS (default: "us-east-1")

#### Mode Configuration
**Namespace Mode (Recommended):**
- `MODE`: Set to "namespace"
- `NAMESPACE`: Kubernetes namespace to monitor (default: "default")
- `OPT_IN_ANNOTATION`: Annotation for deployment opt-in (default: "spot-orchestrator/enabled")
- `SERVICE_LABEL_SELECTOR`: Label to match deployments to services (default: "app")

**Single Mode (Alternative):**
- `MODE`: Set to "single"
- `DEPLOYMENT_NAME`: Name of the Kubernetes deployment to monitor
- `SERVICE_NAME`: Name of the Kubernetes service for health verification

#### Optional Configuration
- `LOG_LEVEL`: Logging level - debug, info, warn, error (default: "info")
- `LOG_FORMAT`: Logging format - auto, console, json (default: "auto")
- `SLACK_WEBHOOK_URL`: Slack webhook URL for alerts (optional)
- `ALERTS_ENABLED`: Enable/disable Slack alerts (default: "true")
- `RECOVERY_ENABLED`: Enable automatic recovery to spot instances (default: "true")
- `RECOVERY_INTERVAL`: How often to check for recovery opportunities (default: "5m")
- `RECOVERY_COOLDOWN`: Wait time after migration before attempting recovery (default: "45m")
- `RECOVERY_TIMEOUT`: Maximum time to wait for recovery rollout (default: "3m")

### TOML Configuration File

Create a `config.toml` file for development:

```toml
[kubernetes]
namespace = "default"
mode = "namespace"  # single | namespace

# Namespace mode configuration (recommended)
opt_in_annotation = "spot-orchestrator/enabled"
service_label_selector = "app"

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
# sqs_queue_url will be set via environment variable

[alerts]
enabled = true
# slack_webhook_url will be set via environment variable

[logging]
level = "info"
format = "json"
```

## 🎯 Step 4: Verify Everything Works

**1. Check Orchestrator Logs**
```bash
# Docker Compose
docker-compose logs -f spot-orchestrator

# Kubernetes
kubectl logs -n spot-orchestrator deployment/spot-orchestrator -f

# Source build
# Check console output
```

**2. Look for Success Messages**
```
{"level":"info","message":"Orchestrator initialized successfully, starting monitoring loop"}
{"level":"info","message":"Configuration loaded successfully","namespace":"default","mode":"namespace"}
```

**3. Test Deployment Discovery**
The orchestrator should find deployments with the `spot-orchestrator/enabled: "true"` annotation:
```
{"level":"info","message":"Found managed deployment","deployment":"my-app","namespace":"default"}
```

## 🎯 Step 5: Test Spot Interruption (Optional)

To test without waiting for a real spot interruption:

**1. Send Test Event to SQS**
```bash
aws sqs send-message \
  --queue-url https://sqs.us-east-1.amazonaws.com/xxx/spot-interruption-queue \
  --message-body '{
    "version": "0",
    "id": "test-event",
    "detail-type": "EC2 Spot Instance Interruption Warning",
    "source": "aws.ec2",
    "account": "xxx",
    "time": "2024-01-15T10:30:00Z",
    "region": "us-east-1",
    "detail": {
      "instance-id": "i-1234567890abcdef0",
      "instance-action": "terminate"
    }
  }'
```

**2. Watch the Logs**
You should see the orchestrator process the event and attempt migration.

## 🚨 Troubleshooting

### Common Issues

1. **"No deployments found"**
   - Ensure deployments have `spot-orchestrator/enabled: "true"` annotation
   - Check the namespace configuration matches your deployments

2. **"Failed to initialize Kubernetes client"**
   - Verify kubectl can access your cluster: `kubectl get nodes`
   - For Docker: Ensure kubeconfig is properly mounted
   - For EKS: Ensure AWS CLI is available and configured

3. **"SQS queue not found"**
   - Verify the SQS queue URL is correct
   - Check AWS credentials have SQS permissions

4. **"Migration failed"**
   - Ensure Fargate profile is configured for your namespace
   - Check pod resource requests are within Fargate limits
   - Verify deployments have proper readiness probes

5. **"Config profile not found" (EKS)**
   - Ensure AWS credentials are mounted: `~/.aws` directory
   - Check your kubeconfig uses the correct AWS profile
   - Verify AWS CLI is installed in the container

### Debug Mode

```bash
# Enable debug logging
export LOG_LEVEL=debug
export LOG_FORMAT=console

# For Docker Compose, add to .env:
LOG_LEVEL=debug
LOG_FORMAT=console
```

### Health Checks

```bash
# Test Kubernetes connectivity
kubectl get nodes

# Test AWS SQS access
aws sqs get-queue-attributes --queue-url YOUR_QUEUE_URL

# Check deployment annotations
kubectl get deployment <deployment-name> -o yaml | grep annotations -A 10
```

## 🎉 Success!

If you see logs like this, you're ready:
```
{"level":"info","message":"Spot Fargate Orchestrator starting..."}
{"level":"info","message":"Configuration loaded successfully"}
{"level":"info","message":"Orchestrator initialized successfully, starting monitoring loop"}
{"level":"info","message":"Found managed deployment","deployment":"my-app"}
```

Your orchestrator is now monitoring for spot interruptions and ready to migrate workloads to Fargate when needed!

## 📚 Next Steps

- Review the main [README.md](README.md) for architecture details
- Set up monitoring and alerts
- Learn about the recovery system for cost optimization
- Explore advanced configuration options
- Test with real spot interruptions