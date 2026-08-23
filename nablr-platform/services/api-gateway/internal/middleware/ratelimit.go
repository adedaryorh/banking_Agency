package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

type RateLimiter struct {
	client *redis.Client
	rate   int64 // requests per minute
}

func NewRateLimiter(redisURL string, rate int64) (*RateLimiter, error) {
	client := redis.NewClient(&redis.Options{
		Addr: redisURL,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &RateLimiter{
		client: client,
		rate:   rate,
	}, nil
}

func (rl *RateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get identifier (user_id or IP)
		identifier := c.GetString("user_id")
		if identifier == "" {
			identifier = c.ClientIP()
		}

		key := fmt.Sprintf("rate_limit:%s", identifier)
		ctx := c.Request.Context()
		count, err := rl.client.Incr(ctx, key).Result()
		if err != nil {
			// If Redis fails, log but allow request (fail open)
			c.Next()
			return
		}

		// Set expiry on first request
		if count == 1 {
			rl.client.Expire(ctx, key, time.Minute)
		}

		// Get TTL for rate limit reset time
		ttl, _ := rl.client.TTL(ctx, key).Result()

		// Set rate limit headers
		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", rl.rate))
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", max(0, rl.rate-count)))
		c.Header("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(ttl).Unix()))

		// Check if rate limit exceeded
		if count > rl.rate {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "Rate limit exceeded",
				"message":     fmt.Sprintf("Maximum %d requests per minute allowed", rl.rate),
				"retry_after": ttl.Seconds(),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

func IPRateLimiter(requestsPerMinute int) gin.HandlerFunc {
	type client struct {
		count     int
		resetTime time.Time
	}

	clients := make(map[string]*client)

	return func(c *gin.Context) {
		ip := c.ClientIP()
		now := time.Now()

		// Clean up old entries
		if len(clients) > 10000 {
			for k, v := range clients {
				if now.After(v.resetTime) {
					delete(clients, k)
				}
			}
		}

		// Get or create client
		cl, exists := clients[ip]
		if !exists || now.After(cl.resetTime) {
			clients[ip] = &client{
				count:     1,
				resetTime: now.Add(time.Minute),
			}
			c.Next()
			return
		}

		// Increment count
		cl.count++

		// Check limit
		if cl.count > requestsPerMinute {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":   "Rate limit exceeded",
				"message": fmt.Sprintf("Maximum %d requests per minute allowed", requestsPerMinute),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
