package structs

// Principal is the authenticated caller, as vouched for by the IDP.
type Principal struct {
	Subject  string   `json:"sub"`
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
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
