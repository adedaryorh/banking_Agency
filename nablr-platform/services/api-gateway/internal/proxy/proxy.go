package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sony/gobreaker"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
)

type ServiceProxy struct {
	client         *http.Client
	circuitBreaker *gobreaker.CircuitBreaker
	logger         *zap.Logger
}

func NewServiceProxy(timeout time.Duration, logger *zap.Logger) *ServiceProxy {
	// Circuit breaker settings
	cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "service-proxy",
		MaxRequests: 3,
		Interval:    10 * time.Second,
		Timeout:     60 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= 5 && failureRatio >= 0.6
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			logger.Warn("Circuit breaker state changed",
				zap.String("name", name),
				zap.String("from", from.String()),
				zap.String("to", to.String()))
		},
	})

	return &ServiceProxy{
		client: &http.Client{
			Timeout: timeout,
			Transport: otelhttp.NewTransport(&http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			}),
		},
		circuitBreaker: cb,
		logger:         logger,
	}
}

func (sp *ServiceProxy) ProxyRequest(c *gin.Context, targetURL string) {
	sp.ProxyRequestWithPath(c, targetURL, c.Request.URL.Path)
}

func (sp *ServiceProxy) ProxyRequestWithPath(c *gin.Context, targetURL, targetPath string) {
	// Execute with circuit breaker
	_, err := sp.circuitBreaker.Execute(func() (interface{}, error) {
		return nil, sp.doProxy(c, targetURL, targetPath)
	})

	if err != nil {
		if err == gobreaker.ErrOpenState {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":   "Service temporarily unavailable",
				"message": "Circuit breaker is open",
			})
			return
		}

		sp.logger.Error("Proxy request failed",
			zap.Error(err),
			zap.String("target", targetURL))
		_ = c.Error(err)

		c.JSON(http.StatusBadGateway, gin.H{
			"error":   "Failed to reach backend service",
			"message": err.Error(),
		})
	}
}

func (sp *ServiceProxy) doProxy(c *gin.Context, targetURL, targetPath string) error {
	// Build target URL with path and query
	fullURL := targetURL + targetPath
	if c.Request.URL.RawQuery != "" {
		fullURL += "?" + c.Request.URL.RawQuery
	}

	var bodyBytes []byte
	if c.Request.Body != nil {
		bodyBytes, _ = io.ReadAll(c.Request.Body)
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}

	req, err := http.NewRequestWithContext(
		context.Background(),
		c.Request.Method,
		fullURL,
		bytes.NewBuffer(bodyBytes),
	)
	if err != nil {
		return err
	}

	// Copy headers
	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// Forward request
	resp, err := sp.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			c.Header(key, value)
		}
	}

	c.Status(resp.StatusCode)
	_, err = io.Copy(c.Writer, resp.Body)
	return err
}

// LoadBalancer - Simple round-robin load balancer
type LoadBalancer struct {
	proxies []*ServiceProxy
	current int
	logger  *zap.Logger
	mu      sync.Mutex
}

func NewLoadBalancer(endpoints []string, timeout time.Duration, logger *zap.Logger) *LoadBalancer {
	proxies := make([]*ServiceProxy, len(endpoints))
	for i := range endpoints {
		proxies[i] = NewServiceProxy(timeout, logger)
	}

	return &LoadBalancer{
		proxies: proxies,
		current: 0,
		logger:  logger,
	}
}

func (lb *LoadBalancer) Next() *ServiceProxy {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	proxy := lb.proxies[lb.current]
	lb.current = (lb.current + 1) % len(lb.proxies)
	return proxy
}
