// Package outbox is the adapter of the outbox contract: Postgres for the records and SQS for the
// publication.
package outbox

import "go.uber.org/fx"

// Module provides the repository and the publisher behind the contracts in interfaces/outbox.
var Module = fx.Module("repositories.outbox",
	fx.Provide(
		NewPostgresRepository,
		NewSQSPublisher,
	),
)
