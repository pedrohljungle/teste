package observability

import "context"

type correlationKey struct{}

// WithCorrelationID stores the identifier that ties everything one request or one message does.
// The HTTP middleware sets it from the request id and the queue consumer from the message id,
// and it travels to the events the operation produces.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID returns the identifier stored by WithCorrelationID, empty when none was set.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}
