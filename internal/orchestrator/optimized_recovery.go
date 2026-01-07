package orchestrator

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/time/rate"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"spot-fargate-orchestrator/internal/kubernetes/annotations"
	"spot-fargate-orchestrator/internal/kubernetes/cache"
	"spot-fargate-orchestrator/internal/kubernetes/discovery"
	"spot-fargate-orchestrator/internal/logger"
)

// RateLimiter provides application-level rate limiting
type RateLimiter struct {
	limiter *rate.Limiter
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(qps float64, burst int) *RateLimiter {
	return &RateLimiter{
		limiter: rate.NewLimiter(rate.Limit(qps), burst),
	}
}

// Wait blocks until the rate limiter allows the operation
func (rl *RateLimiter) Wait(ctx context.Context) error {
	return rl.limiter.Wait(ctx)
}

// Allow checks if an operation is allowed without blocking
func (rl *RateLimiter) Allow() bool {
	return rl.limiter.Allow()
}

// OptimizedRecoveryManager implements API-efficient recovery
type OptimizedRecoveryManager struct {
	orchestrator    *Orchestrator
	logger          *logger.Logger
	deploymentCache *cache.DeploymentCache

	// Rate limiting
	rateLimiter *RateLimiter

	// Batch processing
	batchSize    int
	batchTimeout time.Duration

	// Control channels
	ticker *time.Ticker
	stopCh chan struct{}
}

// NewOptimizedRecoveryManager creates an API-efficient recovery manager
func NewOptimizedRecoveryManager(orchestrator *Orchestrator) *OptimizedRecoveryManager {
	var deploymentCache *cache.DeploymentCache
	var rateLimiter *RateLimiter
	batchSize := 1 // Default to no batching

	// Only create cache if caching is enabled
	if orchestrator.config.CachingEnabled {
		deploymentCache = cache.NewDeploymentCache(
			orchestrator.k8sClient.GetClientset(),
			orchestrator.config.Namespace,
			orchestrator.config.CachingDeploymentTTL,
		)
	}

	// Only create rate limiter if rate limiting is enabled
	if orchestrator.config.RateLimitingEnabled {
		rateLimiter = NewRateLimiter(orchestrator.config.RateLimitingQPS, orchestrator.config.RateLimitingBurst)
	}

	// Only use batch processing if enabled
	if orchestrator.config.BatchProcessingEnabled {
		batchSize = orchestrator.config.BatchSize
	}

	return &OptimizedRecoveryManager{
		orchestrator:    orchestrator,
		logger:          logger.NewDefault("optimized-recovery"),
		deploymentCache: deploymentCache,
		rateLimiter:     rateLimiter,
		batchSize:       batchSize,
		batchTimeout:    orchestrator.config.BatchTimeout,
		stopCh:          make(chan struct{}),
	}
}

// Start begins optimized recovery with API efficiency
func (orm *OptimizedRecoveryManager) Start(ctx context.Context) {
	if !orm.orchestrator.config.RecoveryEnabled {
		orm.logger.Info("Optimized recovery manager is disabled")
		return
	}

	orm.logger.Info("Starting optimized recovery manager",
		"recovery_interval", orm.orchestrator.config.RecoveryInterval,
		"batch_size", orm.batchSize,
		"rate_limit_qps", 10.0)

	orm.ticker = time.NewTicker(orm.orchestrator.config.RecoveryInterval)
	defer orm.ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			orm.logger.Info("Optimized recovery manager stopping")
			orm.deploymentCache.Stop()
			return
		case <-orm.stopCh:
			orm.logger.Info("Optimized recovery manager stopped")
			orm.deploymentCache.Stop()
			return
		case <-orm.ticker.C:
			if err := orm.attemptOptimizedRecovery(ctx); err != nil {
				orm.logger.Error("Optimized recovery failed", "error", err)
			}
		}
	}
}

// attemptOptimizedRecovery performs batch recovery with conditional optimizations
func (orm *OptimizedRecoveryManager) attemptOptimizedRecovery(ctx context.Context) error {
	startTime := time.Now()

	// Rate limit the entire recovery attempt (only if rate limiting is enabled)
	if orm.rateLimiter != nil {
		if err := orm.rateLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter error at start: %w", err)
		}
	}

	// Get deployments from cache if available, otherwise use discovery service
	var deploymentList *appsv1.DeploymentList
	var err error

	if orm.deploymentCache != nil {
		// Use cache (reduces API calls)
		orm.logger.Debug("Using deployment cache for discovery")
		deploymentList, err = orm.deploymentCache.ListDeployments(ctx)
		if err != nil {
			orm.logger.Warn("Cache failed, falling back to direct API", "error", err)
			// Fall through to direct API call
		}
	}

	if deploymentList == nil {
		// Fall back to direct API calls (same as regular recovery)
		orm.logger.Debug("Using direct API for deployment discovery")
		deployments, discErr := orm.orchestrator.discoveryService.DiscoverManagedDeployments(ctx)
		if discErr != nil {
			return fmt.Errorf("failed to discover managed deployments: %w", discErr)
		}

		// Convert to DeploymentList format
		deploymentList = &appsv1.DeploymentList{
			Items: make([]appsv1.Deployment, 0, len(deployments)),
		}

		// Get each deployment with rate limiting
		for i, depInfo := range deployments {
			// Rate limit between deployment fetches
			if orm.rateLimiter != nil && i > 0 {
				if err := orm.rateLimiter.Wait(ctx); err != nil {
					orm.logger.Warn("Rate limiter error during discovery", "error", err)
					continue
				}
			}

			deployment, getErr := orm.orchestrator.k8sClient.GetClientset().AppsV1().Deployments(depInfo.Namespace).Get(
				ctx, depInfo.Name, metav1.GetOptions{})
			if getErr != nil {
				orm.logger.Warn("Failed to get deployment during discovery",
					"deployment", depInfo.Name, "error", getErr)
				continue
			}
			deploymentList.Items = append(deploymentList.Items, *deployment)
		}
	}

	// Filter deployments that need recovery
	var candidateDeployments []string
	for _, deployment := range deploymentList.Items {
		// Quick check using cached data or direct deployment data
		if orm.isRecoveryCandidate(&deployment) {
			candidateDeployments = append(candidateDeployments, deployment.Name)
		}
	}

	if len(candidateDeployments) == 0 {
		orm.logger.Debug("No recovery candidates found")
		return nil
	}

	orm.logger.Info("Found recovery candidates",
		"count", len(candidateDeployments),
		"batch_size", orm.batchSize,
		"caching_enabled", orm.deploymentCache != nil,
		"rate_limiting_enabled", orm.rateLimiter != nil)

	// Process in controlled batches
	return orm.processBatchRecovery(ctx, candidateDeployments, startTime)
}

// processBatchRecovery processes deployments in controlled batches with proper concurrency
func (orm *OptimizedRecoveryManager) processBatchRecovery(ctx context.Context, deployments []string, startTime time.Time) error {
	totalDeployments := len(deployments)

	orm.logger.Info("Starting batch recovery processing",
		"total_deployments", totalDeployments,
		"batch_size", orm.batchSize,
		"rate_limiting_enabled", orm.rateLimiter != nil)

	// Process deployments in controlled batches
	for i := 0; i < totalDeployments; i += orm.batchSize {
		// Calculate batch boundaries
		end := i + orm.batchSize
		if end > totalDeployments {
			end = totalDeployments
		}

		batch := deployments[i:end]
		batchNumber := (i / orm.batchSize) + 1

		orm.logger.Debug("Processing batch",
			"batch_number", batchNumber,
			"batch_size", len(batch),
			"deployments", batch)

		// Process each deployment in the batch SEQUENTIALLY (no goroutines)
		batchStartTime := time.Now()
		successCount := 0

		for j, deploymentName := range batch {
			// Rate limit between deployments within batch
			if orm.rateLimiter != nil && j > 0 {
				if err := orm.rateLimiter.Wait(ctx); err != nil {
					orm.logger.Warn("Rate limiter error between deployments",
						"deployment", deploymentName, "error", err)
					continue
				}
			}

			// Process single deployment
			deploymentStartTime := time.Now()
			if err := orm.attemptSingleDeploymentRecovery(ctx, deploymentName); err != nil {
				orm.logger.Error("Recovery failed for deployment",
					"deployment", deploymentName,
					"batch", batchNumber,
					"error", err)
			} else {
				successCount++
				orm.logger.Debug("Deployment recovery completed",
					"deployment", deploymentName,
					"duration", time.Since(deploymentStartTime))
			}

			// Check for context cancellation
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				// Continue processing
			}
		}

		batchDuration := time.Since(batchStartTime)
		orm.logger.Info("Batch processing completed",
			"batch_number", batchNumber,
			"total_batches", (totalDeployments+orm.batchSize-1)/orm.batchSize,
			"batch_size", len(batch),
			"successful_recoveries", successCount,
			"failed_recoveries", len(batch)-successCount,
			"batch_duration", batchDuration)

		// Add delay between batches (except for the last batch)
		if end < totalDeployments {
			batchDelay := time.Second // 1 second delay between batches
			if orm.batchTimeout > 0 && batchDelay > orm.batchTimeout/10 {
				batchDelay = orm.batchTimeout / 10 // Use 10% of batch timeout as delay
			}

			orm.logger.Debug("Waiting between batches",
				"delay", batchDelay,
				"next_batch", batchNumber+1)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(batchDelay):
				// Continue to next batch
			}
		}
	}

	duration := time.Since(startTime)
	avgPerDeployment := duration
	if totalDeployments > 0 {
		avgPerDeployment = duration / time.Duration(totalDeployments)
	}

	orm.logger.Info("Recovery processing completed",
		"total_deployments", totalDeployments,
		"duration", duration,
		"avg_per_deployment", avgPerDeployment,
		"optimizations_used", map[string]bool{
			"caching":          orm.deploymentCache != nil,
			"rate_limiting":    orm.rateLimiter != nil,
			"batch_processing": orm.batchSize > 1,
		})

	return nil
}

// isRecoveryCandidate quickly checks if deployment needs recovery using cached data
func (orm *OptimizedRecoveryManager) isRecoveryCandidate(deployment *appsv1.Deployment) bool {
	// Quick checks using cached deployment data

	// 1. Check if on Fargate
	currentType := orm.getCurrentComputeTypeFromSpec(deployment)
	if currentType != orm.orchestrator.config.FargateLabel {
		return false
	}

	// 2. Check if recovery is enabled via annotations
	if deployment.Annotations != nil {
		if enabled, exists := deployment.Annotations["spot-orchestrator/recovery-enabled"]; exists {
			return enabled == "true"
		}
	}

	// 3. Check opt-in annotation
	if deployment.Annotations != nil {
		if enabled, exists := deployment.Annotations[orm.orchestrator.config.OptInAnnotation]; exists {
			return enabled == "true"
		}
	}

	return false
}

// getCurrentComputeTypeFromSpec determines compute type from deployment spec
func (orm *OptimizedRecoveryManager) getCurrentComputeTypeFromSpec(deployment *appsv1.Deployment) string {
	if deployment.Spec.Template.Labels != nil {
		if computeType, exists := deployment.Spec.Template.Labels[orm.orchestrator.config.ComputeLabelKey]; exists {
			return computeType
		}
	}

	// Check nodeSelector
	if deployment.Spec.Template.Spec.NodeSelector != nil {
		if computeType, exists := deployment.Spec.Template.Spec.NodeSelector["eks.amazonaws.com/compute-type"]; exists && computeType == "fargate" {
			return orm.orchestrator.config.FargateLabel
		}
	}

	return orm.orchestrator.config.SpotLabel
}

// attemptSingleDeploymentRecovery performs recovery for a single deployment
func (orm *OptimizedRecoveryManager) attemptSingleDeploymentRecovery(ctx context.Context, deploymentName string) error {
	var deployment *appsv1.Deployment
	var err error

	// Get fresh deployment data for recovery decision
	if orm.deploymentCache != nil {
		// Use cache if available
		deployment, err = orm.deploymentCache.GetDeployment(ctx, deploymentName)
		if err != nil {
			orm.logger.Warn("Cache miss, using direct API call",
				"deployment", deploymentName, "error", err)
			// Fall through to direct API call
		}
	}

	if deployment == nil {
		// Fall back to direct API call
		deployment, err = orm.orchestrator.k8sClient.GetClientset().AppsV1().Deployments(orm.orchestrator.config.Namespace).Get(
			ctx, deploymentName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
		}
	}

	// Check if deployment is still a recovery candidate with fresh data
	if !orm.isRecoveryCandidate(deployment) {
		orm.logger.Debug("Deployment no longer needs recovery",
			"deployment", deploymentName)
		return nil
	}

	// Check spot node availability (only once per recovery cycle, not per deployment)
	if !orm.spotNodesExist(ctx) {
		orm.logger.Debug("No spot nodes available, skipping recovery",
			"deployment", deploymentName)
		return nil
	}

	// Create deployment-specific configuration
	deploymentConfig := annotations.NewDeploymentConfig(orm.orchestrator.config, deployment)

	// Check if recovery is enabled for this deployment
	if !deploymentConfig.GetRecoveryEnabled() {
		orm.logger.Debug("Recovery disabled for deployment",
			"deployment", deploymentName)
		return nil
	}

	// Check cooldown period using deployment-specific cooldown
	if !orm.shouldAttemptRecoveryWithConfig(deployment.Annotations, deploymentConfig) {
		orm.logger.Debug("Deployment still in cooldown period",
			"deployment", deploymentName,
			"cooldown", deploymentConfig.GetRecoveryCooldown())
		return nil
	}

	orm.logger.Info("Attempting recovery from Fargate to Spot",
		"deployment", deploymentName,
		"cooldown", deploymentConfig.GetRecoveryCooldown())

	// Use the regular recovery manager's logic by calling attemptDeploymentRecovery
	// Create a temporary recovery manager to access the recovery logic
	regularRecoveryManager := NewRecoveryManager(orm.orchestrator)

	// Create a deployment info structure for the recovery
	deploymentInfo := discovery.DeploymentInfo{
		Name:      deploymentName,
		Namespace: orm.orchestrator.config.Namespace,
	}

	// Call the regular recovery manager's attemptDeploymentRecovery method
	err = regularRecoveryManager.attemptDeploymentRecovery(ctx, deploymentInfo)

	// Invalidate cache after recovery attempt (success or failure)
	if orm.deploymentCache != nil {
		orm.deploymentCache.InvalidateDeployment(deploymentName)
	}

	return err
}

// spotNodesExist checks if there are any spot nodes available in the cluster
// This is a cached check to avoid repeated API calls
func (orm *OptimizedRecoveryManager) spotNodesExist(ctx context.Context) bool {
	// TODO: This could be cached for better performance
	nodes, err := orm.orchestrator.k8sClient.GetClientset().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		orm.logger.Warn("Failed to list nodes for spot check", "error", err)
		return false
	}

	for _, node := range nodes.Items {
		// Check for spot node labels (same logic as in recovery.go)
		if orm.isSpotNode(&node) {
			return true
		}
	}

	return false
}

// isSpotNode checks if a node is a spot instance (same logic as in recovery.go)
func (orm *OptimizedRecoveryManager) isSpotNode(node *corev1.Node) bool {
	// Check for common spot node labels
	if capacityType, exists := node.Labels["eks.amazonaws.com/capacityType"]; exists && capacityType == "SPOT" {
		return true
	}
	if capacityType, exists := node.Labels["capacity-type"]; exists && capacityType == "spot" {
		return true
	}
	if capacityType, exists := node.Labels["karpenter.sh/capacity-type"]; exists && capacityType == "spot" {
		return true
	}
	if capacityType, exists := node.Labels["node.kubernetes.io/capacity-type"]; exists && capacityType == "spot" {
		return true
	}

	// Check for spot instance taints
	for _, taint := range node.Spec.Taints {
		if taint.Key == "capacity-type" && taint.Value == "spot" {
			return true
		}
		if taint.Key == "karpenter.sh/capacity-type" && taint.Value == "spot" {
			return true
		}
		if taint.Key == "eks.amazonaws.com/capacityType" && taint.Value == "SPOT" {
			return true
		}
	}

	return false
}

// shouldAttemptRecoveryWithConfig checks if we should attempt recovery using deployment-specific config
func (orm *OptimizedRecoveryManager) shouldAttemptRecoveryWithConfig(annotations map[string]string, deploymentConfig *annotations.DeploymentConfig) bool {
	if annotations == nil {
		return true
	}

	// Get the cooldown duration (deployment-specific or global)
	cooldownDuration := deploymentConfig.GetRecoveryCooldown()

	// Check migration cooldown
	if migratedAtStr, exists := annotations["spot-orchestrator/migrated-at"]; exists {
		if migratedAt, err := time.Parse(time.RFC3339, migratedAtStr); err == nil {
			if time.Since(migratedAt) < cooldownDuration {
				return false
			}
		}
	}

	// Check recovery backoff based on failed attempts
	if failedAttemptsStr, exists := annotations["spot-orchestrator.io/failed-recovery-attempts"]; exists {
		if lastRecoveredStr, exists := annotations["spot-orchestrator.io/last-recovered-at"]; exists {
			if lastRecovered, err := time.Parse(time.RFC3339, lastRecoveredStr); err == nil {
				failedAttempts := 0
				if attempts, parseErr := time.ParseDuration(failedAttemptsStr + "m"); parseErr == nil {
					failedAttempts = int(attempts.Minutes())
				}

				// Calculate exponential backoff
				backoff := time.Duration(failedAttempts) * orm.orchestrator.config.RecoveryBackoffBase
				if backoff > orm.orchestrator.config.RecoveryMaxBackoff {
					backoff = orm.orchestrator.config.RecoveryMaxBackoff
				}

				if time.Since(lastRecovered) < backoff {
					return false
				}
			}
		}
	}

	return true
}

// GetCacheStats returns cache performance statistics
func (orm *OptimizedRecoveryManager) GetCacheStats() map[string]interface{} {
	if orm.deploymentCache != nil {
		return orm.deploymentCache.GetStats()
	}
	return map[string]interface{}{
		"caching_enabled": false,
		"message":         "Caching is disabled",
	}
}

// Stop stops the optimized recovery manager
func (orm *OptimizedRecoveryManager) Stop() {
	close(orm.stopCh)

	// Stop cache if it exists
	if orm.deploymentCache != nil {
		orm.deploymentCache.Stop()
	}
}
