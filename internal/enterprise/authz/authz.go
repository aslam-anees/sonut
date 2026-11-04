// Portions derived from DEEIX-Chat, Copyright 2026 DEEIX-AI, Apache-2.0.
// Modified for @code; see WIRING.md and third_party/licenses/DEEIX-Chat-LICENSE.txt.
// Package authz provides role-based permission (RBAC) checks. Generalized
// from the role-gating logic in DEEIX-Chat/backend's
// internal/transport/http/middleware AdminOnly middleware and
// internal/domain/user.IsAdminRole, which hard-coded a single admin/non-admin
// split. This package makes the role -> allowed-actions mapping data-driven
// so embedders can define their own roles and permissions.
package authz

import "sort"

// Role is an identifier for a set of granted permissions, e.g. "admin",
// "member", "viewer".
type Role string

// Permission is an identifier for a guarded action, e.g. "audit:read",
// "user:delete".
type Permission string

// Policy maps roles to the permissions they hold.
type Policy struct {
	grants map[Role]map[Permission]struct{}
}

// NewPolicy creates an empty policy. Use Grant to populate it.
func NewPolicy() *Policy {
	return &Policy{grants: make(map[Role]map[Permission]struct{})}
}

// Grant gives role the listed permissions, in addition to any previously
// granted. Returns the Policy for chaining.
func (p *Policy) Grant(role Role, permissions ...Permission) *Policy {
	set, ok := p.grants[role]
	if !ok {
		set = make(map[Permission]struct{})
		p.grants[role] = set
	}
	for _, perm := range permissions {
		set[perm] = struct{}{}
	}
	return p
}

// Allow reports whether role holds permission.
func (p *Policy) Allow(role Role, permission Permission) bool {
	set, ok := p.grants[role]
	if !ok {
		return false
	}
	_, ok = set[permission]
	return ok
}

// Permissions returns the sorted list of permissions granted to role.
func (p *Policy) Permissions(role Role) []Permission {
	set, ok := p.grants[role]
	if !ok {
		return nil
	}
	out := make([]Permission, 0, len(set))
	for perm := range set {
		out = append(out, perm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ErrForbidden is returned by Check when the role lacks the permission.
type ErrForbidden struct {
	Role       Role
	Permission Permission
}

func (e *ErrForbidden) Error() string {
	return "authz: role " + string(e.Role) + " lacks permission " + string(e.Permission)
}

// Check returns an *ErrForbidden if role does not hold permission, else nil.
// It is the guard callers place at the top of a protected operation.
func (p *Policy) Check(role Role, permission Permission) error {
	if p.Allow(role, permission) {
		return nil
	}
	return &ErrForbidden{Role: role, Permission: permission}
}
