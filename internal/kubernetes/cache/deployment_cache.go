package cache

import (
	"context"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"spot-fargate-orchestrator/internal/logger"
)

// DeploymentCache provides cached access to deployment information
type DeploymentCache struct {
	client    kubernetes.Interface
	namespace string
	logger    *logger.Logger

	// Cache storage
	mu          sync.RWMutex
	deployments map[string]*CachedDeployment
	lastRefresh time.Time

	// Cache configuration
	ttl           time.Duration
	refreshTicker *time.Ticker
	stopCh        chan struct{}
}

// CachedDeployment represents a cached deployment with metadata
type CachedDeployment struct {
	Deployment *appsv1.Deployment
	CachedAt   time.Time
	Hits       int64
}

// NewDeploymentCache creates a new deployment cache
func NewDeploymentCache(client kubernetes.Interface, namespace string, ttl time.Duration) *DeploymentCache {
	cache := &DeploymentCache{
		client:      client,
		namespace:   namespace,
		logger:      logger.NewDefault("deployment-cache"),
		deployments: make(map[string]*CachedDeployment),
		ttl:         ttl,
		stopCh:      make(chan struct{}),
	}

	// Start background refresh
	cache.refreshTicker = time.NewTicker(ttl / 2) // Refresh at half TTL
	go cache.backgroundRefresh()

	return cache
}

// GetDeployment returns a deployment from cache or fetches it
func (dc *DeploymentCache) GetDeployment(ctx context.Context, name string) (*appsv1.Deployment, error) {
	dc.mu.RLock()
	cached, exists := dc.deployments[name]
	dc.mu.RUnlock()

	// Check if cached version is still valid
	if exists && time.Since(cached.CachedAt) < dc.ttl {
		cached.Hits++
		dc.logger.Debug("Cache hit for deployment",
			"deployment", name,
			"hits", cached.Hits,
			"age", time.Since(cached.CachedAt))
		return cached.Deployment, nil
	}

	// Cache miss or expired - fetch from API
	dc.logger.Debug("Cache miss for deployment, fetching from API",
		"deployment", name,
		"exists", exists)

	deployment, err := dc.client.AppsV1().Deployments(dc.namespace).Get(
		ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	// Update cache
	dc.mu.Lock()
	dc.deployments[name] = &CachedDeployment{
		Deployment: deployment,
		CachedAt:   time.Now(),
		Hits:       1,
	}
	dc.mu.Unlock()

	return deployment, nil
}

// ListDeployments returns all deployments from cache or fetches them
func (dc *DeploymentCache) ListDeployments(ctx context.Context) (*appsv1.DeploymentList, error) {
	// Check if we need to refresh the full list
	dc.mu.RLock()
	needsRefresh := time.Since(dc.lastRefresh) > dc.ttl
	dc.mu.RUnlock()

	if needsRefresh {
		return dc.refreshDeploymentList(ctx)
	}

	// Build list from cache
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	deploymentList := &appsv1.DeploymentList{
		Items: make([]appsv1.Deployment, 0, len(dc.deployments)),
	}

	for _, cached := range dc.deployments {
		if time.Since(cached.CachedAt) < dc.ttl {
			deploymentList.Items = append(deploymentList.Items, *cached.Deployment)
		}
	}

	dc.logger.Debug("Returning cached deployment list",
		"count", len(deploymentList.Items))

	return deploymentList, nil
}

// refreshDeploymentList fetches fresh deployment list from API
func (dc *DeploymentCache) refreshDeploymentList(ctx context.Context) (*appsv1.DeploymentList, error) {
	dc.logger.Debug("Refreshing deployment list from API")

	deploymentList, err := dc.client.AppsV1().Deployments(dc.namespace).List(
		ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// Update cache with fresh data
	dc.mu.Lock()
	now := time.Now()
	for _, deployment := range deploymentList.Items {
		dc.deployments[deployment.Name] = &CachedDeployment{
			Deployment: &deployment,
			CachedAt:   now,
			Hits:       0,
		}
	}
	dc.lastRefresh = now
	dc.mu.Unlock()

	dc.logger.Info("Deployment cache refreshed",
		"count", len(deploymentList.Items))

	return deploymentList, nil
}

// backgroundRefresh periodically refreshes the cache
func (dc *DeploymentCache) backgroundRefresh() {
	for {
		select {
		case <-dc.refreshTicker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if _, err := dc.refreshDeploymentList(ctx); err != nil {
				dc.logger.Warn("Background cache refresh failed", "error", err)
			}
			cancel()
		case <-dc.stopCh:
			dc.refreshTicker.Stop()
			return
		}
	}
}

// InvalidateDeployment removes a deployment from cache
func (dc *DeploymentCache) InvalidateDeployment(name string) {
	dc.mu.Lock()
	delete(dc.deployments, name)
	dc.mu.Unlock()

	dc.logger.Debug("Invalidated deployment cache", "deployment", name)
}

// GetStats returns cache statistics
func (dc *DeploymentCache) GetStats() map[string]interface{} {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	totalHits := int64(0)
	for _, cached := range dc.deployments {
		totalHits += cached.Hits
	}

	return map[string]interface{}{
		"cached_deployments": len(dc.deployments),
		"total_hits":         totalHits,
		"last_refresh":       dc.lastRefresh,
		"ttl_seconds":        dc.ttl.Seconds(),
	}
}

// Stop stops the background refresh
func (dc *DeploymentCache) Stop() {
	close(dc.stopCh)
}
