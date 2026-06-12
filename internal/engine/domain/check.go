package domain

// CheckInput carries the parameters for a single rate-limit decision.
type CheckInput struct {
	TenantID   string
	RuleID     string
	Subject    string            // who is making the request (user ID, IP, etc.)
	Resource   string            // resource being accessed (e.g. "api", "upload")
	Action     string            // action being performed (e.g. "POST", "read")
	Cost       uint32            // tokens consumed; 0 treated as 1 by the store
	Attributes map[string]string // reserved for future rule conditions
}

// CheckResult is the enriched outcome returned to the delivery layer.
type CheckResult struct {
	Allowed      bool
	Limit        uint64
	Remaining    uint64
	ResetAtMs    int64
	RetryAfterMs int64
	Reason       string // "allowed" | "rate_limit_exceeded"
}
