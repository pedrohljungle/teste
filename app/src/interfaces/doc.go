// Package interfaces holds the contracts of each domain, one subpackage per domain: what the
// domain offers, what it needs, and the errors both sides match on.
//
// The contracts live apart from the implementations on purpose. A port declared inside the
// service would make the adapter import the service to satisfy it; declared inside the
// adapter, it would make the service import the adapter. Either way one layer ends up knowing
// the other. With a leaf package in the middle, all three layers point at the same place:
//
//	handlers/<domain> ─┐
//	services/<domain> ─┼─> interfaces/<domain> ─> entities, structs
//	repositories/<domain> ─┘
//
// It is empty because this repository carries no domain yet.
package interfaces
