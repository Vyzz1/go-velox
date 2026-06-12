package middleware

import "context"

type contextKey string

const requestIDKey contextKey = "request_id"

// RequestIDFromContext returns the request ID stored in ctx, or empty string.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}
