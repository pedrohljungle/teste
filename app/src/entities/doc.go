// Package entities holds the domain entities, which mirror the database tables.
//
// It is a leaf: it imports no layer and no framework. Struct tags are strings, so the `db` tag
// that maps a column and the `json` tag that shapes a payload can live here without dragging a
// driver or a web framework into the domain.
//
// It is empty because this repository carries no domain yet. The first entity goes here.
package entities
