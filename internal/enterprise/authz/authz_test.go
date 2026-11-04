package authz

import "testing"

func TestPolicyAllowDeny(t *testing.T) {
	policy := NewPolicy().
		Grant("admin", "audit:read", "user:delete").
		Grant("member", "audit:read")

	if !policy.Allow("admin", "user:delete") {
		t.Fatalf("expected admin to be allowed user:delete")
	}
	if policy.Allow("member", "user:delete") {
		t.Fatalf("expected member to be denied user:delete")
	}
	if !policy.Allow("member", "audit:read") {
		t.Fatalf("expected member to be allowed audit:read")
	}
	if policy.Allow("guest", "audit:read") {
		t.Fatalf("expected unknown role to be denied everything")
	}
}

func TestCheckReturnsForbiddenError(t *testing.T) {
	policy := NewPolicy().Grant("admin", "user:delete")

	if err := policy.Check("admin", "user:delete"); err != nil {
		t.Fatalf("expected nil error for allowed permission, got %v", err)
	}

	err := policy.Check("member", "user:delete")
	if err == nil {
		t.Fatalf("expected forbidden error")
	}
	forbidden, ok := err.(*ErrForbidden)
	if !ok {
		t.Fatalf("expected *ErrForbidden, got %T", err)
	}
	if forbidden.Role != "member" || forbidden.Permission != "user:delete" {
		t.Fatalf("unexpected forbidden error fields: %+v", forbidden)
	}
}
