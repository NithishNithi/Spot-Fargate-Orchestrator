package client

import (
	"context"
	"spot-fargate-orchestrator/internal/logger"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// RateLimitedConfig adds rate limiting to Kubernetes client
type RateLimitedConfig struct {
	// QPS limits the number of queries per second
	QPS float32
	// Burst allows burst of requests
	Burst int
	// Timeout for rate limiting
	Timeout time.Duration
}

// NewRateLimitedK8sClient creates a rate-limited Kubernetes client
func NewRateLimitedK8sClient(namespace string, rateLimitConfig *RateLimitedConfig) (*K8sClient, error) {
	// Get base config
	config, err := rest.InClusterConfig()
	if err != nil {
		config, err = buildOutOfClusterConfig()
		if err != nil {
			return nil, err
		}
	}

	// Apply rate limiting to config
	if rateLimitConfig != nil {
		config.QPS = rateLimitConfig.QPS
		config.Burst = rateLimitConfig.Burst
		config.Timeout = rateLimitConfig.Timeout
	} else {
		// Conservative defaults
		config.QPS = 20   // 20 queries per second
		config.Burst = 50 // Allow burst of 50
		config.Timeout = 30 * time.Second
	}

	// Create clientset with rate limiting
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &K8sClient{
		clientset: clientset,
		namespace: namespace,
		logger:    logger.NewDefault("rate-limited-k8s-client"),
	}, nil
}

// RateLimiter provides additional application-level rate limiting
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
