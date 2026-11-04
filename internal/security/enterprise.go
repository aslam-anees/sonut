package security

import (
	"context"
	"time"

	"github.com/aistatine/sonut/internal/enterprise/audit"
	"github.com/aistatine/sonut/internal/enterprise/authz"
	entsecurity "github.com/aistatine/sonut/internal/enterprise/security"
)

// EnterpriseLayer bundles the optional enterprise security capabilities
// (audit trail, RBAC policy checks, sensitive-header redaction, SSRF-hardened
// outbound URL validation) on top of the base Guard. It is entirely optional
// and off by default: a Guard works exactly as before if no EnterpriseLayer
// is attached.
type EnterpriseLayer struct {
	Audit  *audit.Service
	Policy *authz.Policy
}

// EnterpriseOptions configures an EnterpriseLayer. All fields have safe
// zero-value defaults: a nil Store falls back to an in-memory audit store,
// and a nil Policy denies every permission check until roles are granted.
type EnterpriseOptions struct {
	AuditStore audit.Store
	AuditSink  audit.Sink
	Policy     *authz.Policy
}

// NewEnterpriseLayer constructs the enterprise layer. Pass zero-value
// EnterpriseOptions{} to get an in-memory audit trail and an empty (deny-all)
// policy.
func NewEnterpriseLayer(opts EnterpriseOptions) *EnterpriseLayer {
	policy := opts.Policy
	if policy == nil {
		policy = authz.NewPolicy()
	}
	return &EnterpriseLayer{
		Audit:  audit.NewService(opts.AuditStore, opts.AuditSink),
		Policy: policy,
	}
}

// AttachEnterprise wires an EnterpriseLayer onto an existing Guard so that
// every recorded Event is also written to the enterprise audit trail. Guards
// with no attached layer are unaffected: this call is additive.
func (g *Guard) AttachEnterprise(layer *EnterpriseLayer) {
	g.mu.Lock()
	g.enterprise = layer
	g.mu.Unlock()
}

// recordEnterprise forwards event to the attached enterprise audit trail, if
// any. Best-effort: failures do not affect the outbound decision already
// made by CheckOutbound.
func (g *Guard) recordEnterprise(event Event) {
	g.mu.Lock()
	layer := g.enterprise
	g.mu.Unlock()
	if layer == nil || layer.Audit == nil {
		return
	}
	action := "egress." + string(event.Verdict)
	_ = layer.Audit.Write(
		context.Background(),
		"",
		0,
		action,
		"network",
		event.Host,
		event.Host,
		event.Tool,
		map[string]any{
			"port":           event.Port,
			"protocol":       event.Protocol,
			"bytes":          event.Bytes,
			"classification": event.Classification,
			"reason":         event.Reason,
			"timestamp":      event.Timestamp.Format(time.RFC3339),
		},
	)
}

// RedactHeadersJSON exposes the enterprise sensitive-header redaction helper
// for callers that build outbound HTTP requests through this package.
func RedactHeadersJSON(raw string) string {
	return entsecurity.RedactHeadersJSON(raw)
}

// ValidateOutboundHTTPURL exposes the enterprise SSRF-hardened URL validator.
func ValidateOutboundHTTPURL(raw, env string, ssrfProtectionEnabled bool) error {
	return entsecurity.ValidateOutboundHTTPURL(raw, env, ssrfProtectionEnabled)
}
