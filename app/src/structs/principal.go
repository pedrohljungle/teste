package structs

// Roles the realm grants. They are what a route asks for at the border, and the two are
// exclusive in practice: an internal service is not a provider, and a provider is not a service.
const (
	// RoleInternalService is held by the services that operate wallets: opening them, reading
	// them and their ledger, and reconciling them.
	RoleInternalService = "internal_service"
	// RoleProvider is held by a game provider, which sends operations and reads its own.
	RoleProvider = "provider"
)

// Principal is the authenticated caller, as vouched for by the IDP.
type Principal struct {
	Subject  string   `json:"sub"`
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	// ProviderID is the provider this identity acts as, from the provider_id claim of its
	// token. It is empty for anyone that is not a provider. The provider a request may act for
	// comes from here and never from what the request says about itself.
	ProviderID string `json:"provider_id,omitempty"`
}

// HasRole reports whether the role is present. Deciding which role a route requires belongs
// to the caller, not here.
func (p Principal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}
