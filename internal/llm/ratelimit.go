package llm

import (
	"sync"
	"time"

	"codeberg.org/mutker/bumpa/internal/logger"
)

// HTTP header names for rate limiting
const (
	headerRemainingTokens   = "x-ratelimit-remaining-tokens" //nolint:gosec // HTTP header names, not credentials
	headerRemainingRequests = "x-ratelimit-remaining-requests"
	headerResetTokens       = "x-ratelimit-reset-tokens" //nolint:gosec // HTTP header names, not credentials
	headerResetRequests     = "x-ratelimit-reset-requests"
	headerRetryAfter        = "retry-after"
)

const (
	// Default values
	defaultRetryDuration = 5 * time.Second
	tokenSizeMultiplier  = 4 // Approximate bytes-to-tokens ratio
)

// RateLimiter handles rate limiting for LLM API calls
type RateLimiter struct {
	mu sync.Mutex

	// Token-based rate limiting
	remainingTokens int
	tokensResetAt   time.Time

	// Request-based rate limiting
	remainingRequests int
	requestsResetAt   time.Time

	// Last update time for rate limit info
	lastUpdate time.Time
}

// RateLimitInfo contains rate limit information from API headers
type RateLimitInfo struct {
	RemainingTokens   int
	TokensResetIn     time.Duration
	RemainingRequests int
	RequestsResetIn   time.Duration
	RetryAfter        time.Duration // Only set when receiving 429
}

// NewRateLimiter creates a new rate limiter instance
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		remainingTokens:   -1, // -1 indicates not yet initialized
		remainingRequests: -1,
		lastUpdate:        time.Now(),
	}
}

// UpdateLimits updates the rate limiter with new information from API headers
func (rl *RateLimiter) UpdateLimits(info RateLimitInfo) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	rl.lastUpdate = now

	rl.remainingTokens = info.RemainingTokens
	rl.tokensResetAt = now.Add(info.TokensResetIn)

	rl.remainingRequests = info.RemainingRequests
	rl.requestsResetAt = now.Add(info.RequestsResetIn)

	logger.Debug().
		Int("remaining_tokens", info.RemainingTokens).
		Dur("tokens_reset_in", info.TokensResetIn).
		Int("remaining_requests", info.RemainingRequests).
		Dur("requests_reset_in", info.RequestsResetIn).
		Msg("Updated rate limits")
}

// WaitForCapacity waits until there's capacity to make a request
func (rl *RateLimiter) WaitForCapacity(estimatedTokens int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// If limits aren't initialized yet, don't wait
	if rl.remainingTokens < 0 || rl.remainingRequests < 0 {
		return
	}

	now := time.Now()

	// Check and wait for token capacity
	if now.Before(rl.tokensResetAt) && rl.remainingTokens < estimatedTokens {
		waitTime := rl.tokensResetAt.Sub(now)
		logger.Debug().
			Dur("wait_time", waitTime).
			Int("remaining_tokens", rl.remainingTokens).
			Int("estimated_tokens", estimatedTokens).
			Msg("Waiting for token capacity")

		rl.mu.Unlock()
		time.Sleep(waitTime)
		rl.mu.Lock()
	}

	// Check and wait for request capacity
	if now.Before(rl.requestsResetAt) && rl.remainingRequests < 1 {
		waitTime := rl.requestsResetAt.Sub(now)
		logger.Debug().
			Dur("wait_time", waitTime).
			Int("remaining_requests", rl.remainingRequests).
			Msg("Waiting for request capacity")

		rl.mu.Unlock()
		time.Sleep(waitTime)
		rl.mu.Lock()
	}
}

// HandleRetryAfter handles 429 responses by waiting for the specified duration
func HandleRetryAfter(retryAfter time.Duration) {
	logger.Debug().
		Dur("retry_after", retryAfter).
		Msg("Rate limit exceeded, waiting before retry")
	time.Sleep(retryAfter)
}
