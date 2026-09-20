package structs

import "testing"

// HasRole is what every edge authorization decision reads, so an empty or missing role list
// must answer false rather than panic or pass.
func TestHasRole(t *testing.T) {
	cases := []struct {
		name      string
		principal Principal
		role      string
		want      bool
	}{
		{"holds the role", Principal{Roles: []string{"app-admin", "reader"}}, "app-admin", true},
		{"does not hold the role", Principal{Roles: []string{"reader"}}, "app-admin", false},
		{"no roles at all", Principal{}, "app-admin", false},
		{"empty role is not a wildcard", Principal{Roles: []string{"reader"}}, "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.principal.HasRole(c.role); got != c.want {
				t.Fatalf("HasRole(%q) = %v, want %v", c.role, got, c.want)
			}
		})
	}
}
