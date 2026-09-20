// Package appinfo holds the identity of the running application. It is the only value that
// differs between entrypoints besides the modules each one wires, and it is what segregates
// telemetry per application.
package appinfo

// Role tells which job the process does.
type Role string

const (
	RoleServer Role = "server"
	RoleWorker Role = "worker"
)

// App is the identity supplied by the entrypoint. Name becomes service.name in OpenTelemetry
// and the app field on every log line.
type App struct {
	Name    string
	Role    Role
	Version string
}
