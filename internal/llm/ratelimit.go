package llm

import (
	"sync"
	"time"

	"codeberg.org/mutker/bumpa/internal/errors"
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

// WaitInfo contains information about why we're waiting
type WaitInfo struct {
	NeedsToWait     bool
	RemainingTokens int
	WaitTime        time.Duration
	ResetAt         time.Time
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
}

// WaitForCapacity waits until there's capacity to make a request
func (rl *RateLimiter) WaitForCapacity() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.remainingTokens >= 0 {
		logger.Debug().
			Int("remaining_tokens", rl.remainingTokens).
			Int("remaining_requests", rl.remainingRequests).
			Time("tokens_reset_at", rl.tokensResetAt).
			Time("requests_reset_at", rl.requestsResetAt).
			Msg("Current rate limit status")
	}
}

// HandleRetryAfter handles 429 responses by waiting for the specified duration
func HandleRetryAfter(retryAfter time.Duration) {
	logger.Debug().
		Float64("retry_after_seconds", retryAfter.Seconds()).
		Msg(errors.ContextLLMRateLimit)
	time.Sleep(retryAfter)
}
