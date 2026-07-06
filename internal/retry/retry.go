package retry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"google.golang.org/api/googleapi"
)

const (
	defaultMaxRetries = 5
	defaultBaseDelay  = 5 * time.Second
)

type config struct {
	maxRetries int
	baseDelay  time.Duration
}

type Option func(*config)

func WithMaxRetries(n int) Option {
	return func(c *config) { c.maxRetries = n }
}

func WithBaseDelay(d time.Duration) Option {
	return func(c *config) { c.baseDelay = d }
}

func IsRetryable(err error) bool {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code == 429 || apiErr.Code >= 500
	}
	return false
}

func DoWithResult[T any](ctx context.Context, operation string, fn func() (T, error), opts ...Option) (T, error) {
	cfg := config{maxRetries: defaultMaxRetries, baseDelay: defaultBaseDelay}
	for _, o := range opts {
		o(&cfg)
	}

	var zero T
	var lastErr error

	for attempt := range cfg.maxRetries {
		result, err := fn()
		if err == nil {
			return result, nil
		}

		if !IsRetryable(err) {
			return zero, err
		}

		lastErr = err
		if attempt == cfg.maxRetries-1 {
			break
		}

		delay := cfg.baseDelay * (1 << attempt)
		jitter := time.Duration(rand.Int64N(int64(delay)/2)) - delay/4
		delay += jitter

		slog.Warn("[retry] retryable error, backing off",
			"operation", operation,
			"attempt", attempt+1,
			"delay", delay,
			"error", err,
		)

		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(delay):
		}
	}

	return zero, fmt.Errorf("max retries (%d) exceeded for %s: %w", cfg.maxRetries, operation, lastErr)
}

func Do(ctx context.Context, operation string, fn func() error, opts ...Option) error {
	_, err := DoWithResult(ctx, operation, func() (struct{}, error) {
		return struct{}{}, fn()
	}, opts...)
	return err
}
