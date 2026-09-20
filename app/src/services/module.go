// Package services is the business rule layer. It holds no code of its own: every rule lives
// in a domain module below it, and this package only aggregates them.
//
// It aggregates nothing yet, because this repository carries no domain. A new domain adds
// services/<domain>/ with its own module.go and one line here.
package services

import "go.uber.org/fx"

// Module wires every domain module of this layer.
var Module = fx.Module("services")
