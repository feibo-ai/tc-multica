package middleware

import (
	"net/http"
)

// Control plane capability names. The same strings are reused as
// audit-log event prefixes (see internal/audit/audit.go).
const (
	CapIntegrationsRead  = "integrations:read"
	CapIntegrationsWrite = "integrations:write"
	CapSecretsRead       = "secrets:read"
	CapSecretsWrite      = "secrets:write"
)

// roleCapabilities is the single source of truth for what each workspace role
// can do in the control plane. Member roles are checked against the existing
// CHECK constraint on the member table (owner / admin / member).
//
// To add a new capability, extend this map AND add a RequireCapability call
// in the relevant route. Do not gate capabilities behind a separate flag
// system — capabilities are role-derived in this codebase.
var roleCapabilities = map[string]map[string]bool{
	"owner": {
		CapIntegrationsRead:  true,
		CapIntegrationsWrite: true,
		CapSecretsRead:       true,
		CapSecretsWrite:      true,
	},
	"admin": {
		CapIntegrationsRead:  true,
		CapIntegrationsWrite: true,
		CapSecretsRead:       true,
		CapSecretsWrite:      true,
	},
	"member": {
		CapIntegrationsRead: true,
		// Members see integration configs (sans secrets) but cannot edit.
	},
}

// HasCapability returns true if the given role grants the given capability.
// Unknown roles get nothing.
func HasCapability(role, capability string) bool {
	return roleCapabilities[role][capability]
}

// RequireCapability returns middleware that 403s if the current workspace
// member's role does not include the given capability. It assumes
// RequireWorkspaceMember (or a sibling middleware that injects Member into
// context) has already run.
func RequireCapability(capability string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m, ok := MemberFromContext(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "no workspace member in context")
				return
			}
			if !HasCapability(m.Role, capability) {
				writeError(w, http.StatusForbidden, "missing capability: "+capability)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
