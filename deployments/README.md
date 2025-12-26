# Spot Fargate Orchestrator Deployment

Simple deployment configurations for the Spot Fargate Orchestrator.

## Directory Structure

```
deployments/
├── docker-compose.yml          # Docker Compose for local development
├── .env.example               # Environment variables template
├── kubernetes/
│   └── spot-orchestrator.yaml # Complete Kubernetes deployment (single file)
└── README.md                  # This file
```

## Quick Start

### Docker Compose (Local Development)

1. **Copy environment template:**
   ```bash
   cp .env.example .env
   ```

2. **Update environment variables:**
   ```bash
   # Edit .env with your AWS SQS queue URL
   vim .env
   ```

3. **Start the orchestrator:**
   ```bash
   docker-compose up -d
   ```

4. **View logs:**
   ```bash
   docker-compose logs -f spot-orchestrator
   ```

### Kubernetes Deployment

1. **Update the secret values:**
   ```bash
   # Edit the Secret section in spot-orchestrator.yaml
   vim kubernetes/spot-orchestrator.yaml
   ```

2. **Deploy everything:**
   ```bash
   kubectl apply -f kubernetes/spot-orchestrator.yaml
   ```

3. **Check status:**
   ```bash
   kubectl get pods -n spot-orchestrator
   kubectl logs -n spot-orchestrator deployment/spot-orchestrator -f
   ```

## Configuration

### Required Configuration

1. **SQS_QUEUE_URL** - Your AWS SQS queue that receives EventBridge spot interruption events
2. **AWS_REGION** - AWS region (default: us-east-1)

### Optional Configuration

1. **SLACK_WEBHOOK_URL** - For real-time alerts
2. **MODE** - "namespace" (recommended) or "single"
3. **NAMESPACE** - Kubernetes namespace to monitor (default: default)

### For Single Mode (Alternative)

If you prefer to manage just one deployment:

```bash
export MODE=single
export DEPLOYMENT_NAME=my-app
export SERVICE_NAME=my-app-service
```

## Adding Deployments to Management

For namespace mode, add this annotation to any deployment you want managed:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    spot-orchestrator/enabled: "true"  # Required for management
```

## Troubleshooting

### Common Issues

1. **No deployments found**
   - Ensure deployments have `spot-orchestrator/enabled: "true"` annotation

2. **Migration failures**
   - Check if Fargate profile is configured
   - Verify pod readiness conditions

3. **No alerts**
   - Check Slack webhook URL is correct

### Debug Commands

```bash
# Check orchestrator logs
kubectl logs -n spot-orchestrator deployment/spot-orchestrator -f

# Check deployment annotations
kubectl get deployment <deployment-name> -o yaml | grep annotations -A 5

# Test health
kubectl exec -n spot-orchestrator deployment/spot-orchestrator -- /app/orchestrator --health-check
```

## What's Included

The single Kubernetes file includes:
- **Namespace** - spot-orchestrator
- **ServiceAccount & RBAC** - Minimal required permissions
- **Secret** - For SQS queue URL and Slack webhook
- **ConfigMap** - Configuration file
- **Deployment** - The orchestrator application
- **Service** - Internal service for the orchestrator

## Security

- Runs as non-root user
- Minimal RBAC permissions
- No external access (internal service only)
- Secrets for sensitive configuration