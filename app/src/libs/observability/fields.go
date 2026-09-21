package observability

import "context"

type fieldsKey struct{}

// WithFields adds identifiers to the context, and every line logged from it or from anything below
// it carries them: the wallet, the provider, the message, the transaction. It is how the code that
// knows a fact states it once, instead of every layer below having to be handed it to log.
//
// Only identifiers belong here. Nothing that is a credential and nothing that is a financial payload
// is ever put in, because everything put in is written to every log line that follows.
func WithFields(ctx context.Context, fields ...Field) context.Context {
	if len(fields) == 0 {
		return ctx
	}
	existing := contextFields(ctx)
	merged := make([]Field, 0, len(existing)+len(fields))
	merged = append(merged, existing...)
	merged = append(merged, fields...)
	return context.WithValue(ctx, fieldsKey{}, merged)
}

func contextFields(ctx context.Context) []Field {
	fields, _ := ctx.Value(fieldsKey{}).([]Field)
	return fields
}
