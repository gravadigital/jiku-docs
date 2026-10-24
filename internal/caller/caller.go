// Package caller resolves who is asking. jiku-docs does not validate tokens: the user id of the
// subject is the identity, guaranteed by the auth-callout. On top of it, the class decides what
// the caller may read and write.
package caller

import (
	"github.com/gravadigital/jiku-docs/internal/acl"
)

// Class of a caller.
type Class string

const (
	// PersonInternal: `admin` or `user`. Reads and writes according to the folder ACL.
	PersonInternal Class = "person-internal"
	// PersonExternal: `external-user`. Reads only public documents related to its projects; never writes.
	PersonExternal Class = "person-external"
	// Service: `internal-app` (any connector, the api without actor, jiku-docs itself). Reads and
	// writes everything; what it writes is attributed to it.
	Service Class = "service"
)

// Roles of Zitadel and their class. Any other role has no access to jiku-docs.
var (
	internalRoles = []string{"admin", "user"}
	externalRoles = []string{"external-user"}
	serviceRoles  = []string{"internal-app"}
)

// ClassFor maps a user's roles and identity type to a class. ok=false means the roles grant no
// access (`caller_not_authorized`). The most permissive matching class wins for persons with
// both internal and external roles? No: like core, the most restrictive wins, so a person that
// carries external-user is external.
func ClassFor(roles []string, identityType string) (Class, bool) {
	has := func(set []string) bool {
		for _, r := range roles {
			for _, s := range set {
				if r == s {
					return true
				}
			}
		}
		return false
	}
	if identityType == "service" {
		if has(serviceRoles) {
			return Service, true
		}
		return "", false
	}
	if has(externalRoles) {
		return PersonExternal, true
	}
	if has(internalRoles) {
		return PersonInternal, true
	}
	return "", false
}

// Caller is the resolved identity of a request.
type Caller struct {
	// SubjectID is the user id of the subject (the one who published).
	SubjectID string
	// ViaAPI is true when the subject is Jiku's api.
	ViaAPI bool
	// ID is the effective identity: the actor when the api declares one, else the subject.
	ID string
	// Name of the effective identity, for event envelopes. May be empty.
	Name  string
	Class Class
	Roles []string
}

// IsService reports the service class.
func (c *Caller) IsService() bool { return c.Class == Service }

// IsExternal reports the external class.
func (c *Caller) IsExternal() bool { return c.Class == PersonExternal }

// Principal is the ACL view of the caller.
func (c *Caller) Principal() acl.Principal {
	return acl.Principal{UserID: c.ID, Roles: c.Roles, All: c.Class == Service}
}

// DefaultVia is the origin recorded for a change when the payload does not say: services are
// always `service`, persons default to `human`.
func (c *Caller) DefaultVia(requested string) string {
	if c.Class == Service {
		return "service"
	}
	if requested == "" || requested == "service" {
		return "human"
	}
	return requested
}
