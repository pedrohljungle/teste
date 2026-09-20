package structs

// APIError is the body every failed request answers with. It exists as a named type because
// the documentation has to describe the error shape, and "whatever the framework renders" is
// not a description. Echo renders an HTTPError exactly like this.
type APIError struct {
	Message string `json:"message" example:"invalid token"`
}
