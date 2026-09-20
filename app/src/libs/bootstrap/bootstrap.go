// Package bootstrap groups the modules every entrypoint shares. It is what makes "the worker
// runs the same source code" true in the wiring and not only in prose.
package bootstrap

import (
	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/libs/awsclients"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/db"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/repositories"
	"github.com/estrategiahq/pedro-test/app/src/services"
)

// Core is the shared graph. Module order is irrelevant: fx resolves by type.
var Core = fx.Options(
	config.Module,
	observability.Module,
	db.Module,
	awsclients.Module,
	repositories.Module,
	services.Module,
)
