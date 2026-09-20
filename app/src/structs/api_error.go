package structs

// APIError is the body every failed request answers with. It exists as a named type because
// the documentation has to describe the error shape, and "whatever the framework renders" is
// not a description. Echo renders an HTTPError carrying it exactly like this.
type APIError struct {
	Message string `json:"message" example:"invalid token"`
	// FailureCode is the stable, documented reason of a business rejection or of an invalid
	// operation. It is what a provider reads to tell an input it can correct from a definitive
	// outcome, and it is absent on errors that are not about the operation itself.
	FailureCode string `json:"failureCode,omitempty" example:"INSUFFICIENT_FUNDS"`
}
