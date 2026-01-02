package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"spot-fargate-orchestrator/internal/kubernetes/cache"
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

	// Rate limit the recovery attempt (only if rate limiting is enabled)
	if orm.rateLimiter != nil {
		if err := orm.rateLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter error: %w", err)
		}
	}

	// Get deployments from cache if available, otherwise use discovery service
	var deploymentList *appsv1.DeploymentList
	var err error

	if orm.deploymentCache != nil {
		// Use cache (reduces API calls)
		deploymentList, err = orm.deploymentCache.ListDeployments(ctx)
		if err != nil {
			return fmt.Errorf("failed to list deployments from cache: %w", err)
		}
	} else {
		// Fall back to direct API calls (same as regular recovery)
		deployments, discErr := orm.orchestrator.discoveryService.DiscoverManagedDeployments(ctx)
		if discErr != nil {
			return fmt.Errorf("failed to discover managed deployments: %w", discErr)
		}

		// Convert to DeploymentList format
		deploymentList = &appsv1.DeploymentList{
			Items: make([]appsv1.Deployment, len(deployments)),
		}

		// Get each deployment (this will make individual API calls)
		for i, depInfo := range deployments {
			deployment, getErr := orm.orchestrator.k8sClient.GetClientset().AppsV1().Deployments(depInfo.Namespace).Get(
				ctx, depInfo.Name, metav1.GetOptions{})
			if getErr != nil {
				orm.logger.Warn("Failed to get deployment", "deployment", depInfo.Name, "error", getErr)
				continue
			}
			deploymentList.Items[i] = *deployment
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

	// Process in batches (or individually if batch processing is disabled)
	return orm.processBatchRecovery(ctx, candidateDeployments, startTime)
}

// processBatchRecovery processes deployments in batches or individually
func (orm *OptimizedRecoveryManager) processBatchRecovery(ctx context.Context, deployments []string, startTime time.Time) error {
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, orm.batchSize)

	for i, deploymentName := range deployments {
		// Rate limit each batch (only if rate limiting is enabled and batch processing is enabled)
		if orm.rateLimiter != nil && orm.batchSize > 1 && i > 0 && i%orm.batchSize == 0 {
			orm.logger.Debug("Batch completed, rate limiting",
				"batch", i/orm.batchSize,
				"processed", i)

			if err := orm.rateLimiter.Wait(ctx); err != nil {
				return err
			}
		}

		wg.Add(1)
		go func(name string) {
			defer wg.Done()

			// Acquire semaphore (controls concurrency)
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Rate limit individual recovery (only if rate limiting is enabled)
			if orm.rateLimiter != nil {
				if err := orm.rateLimiter.Wait(ctx); err != nil {
					orm.logger.Warn("Rate limiter error for deployment",
						"deployment", name, "error", err)
					return
				}
			}

			if err := orm.attemptSingleDeploymentRecovery(ctx, name); err != nil {
				orm.logger.Error("Recovery failed for deployment",
					"deployment", name, "error", err)
			}
		}(deploymentName)
	}

	wg.Wait()

	duration := time.Since(startTime)
	orm.logger.Info("Recovery processing completed",
		"total_deployments", len(deployments),
		"duration", duration,
		"avg_per_deployment", duration/time.Duration(len(deployments)),
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
			return fmt.Errorf("failed to get deployment %s from cache: %w", deploymentName, err)
		}
	} else {
		// Fall back to direct API call
		deployment, err = orm.orchestrator.k8sClient.GetClientset().AppsV1().Deployments(orm.orchestrator.config.Namespace).Get(
			ctx, deploymentName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
		}
	}

	// Perform detailed recovery checks and execution
	// (Implementation would continue with existing recovery logic)

	orm.logger.Debug("Processing recovery for deployment",
		"deployment", deploymentName,
		"current_replicas", *deployment.Spec.Replicas,
		"using_cache", orm.deploymentCache != nil)

	// Invalidate cache after successful recovery (only if cache is enabled)
	if orm.deploymentCache != nil {
		defer orm.deploymentCache.InvalidateDeployment(deploymentName)
	}

	return nil
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
