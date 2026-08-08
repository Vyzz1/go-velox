package client

import (
	"testing"

	"github.com/Vyzz1/go-velox.git/internal/gateway/domain"
)

func TestRoutingKey(t *testing.T) {
	in := domain.CheckInput{TenantID: "acme", Subject: "u1"}
	cases := []struct {
		name string
		mode RouteMode
		in   domain.CheckInput
		want string
	}{
		{"tenant mode ignores subject", RouteByTenant, in, "acme"},
		{"tenant_subject composes", RouteByTenantSubject, in, "acme|u1"},
		{"tenant_subject falls back when subject empty", RouteByTenantSubject, domain.CheckInput{TenantID: "acme"}, "acme"},
	}
	for _, c := range cases {
		if got := routingKey(c.mode, c.in); got != c.want {
			t.Errorf("%s: routingKey = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParseRouteMode(t *testing.T) {
	cases := map[string]RouteMode{
		"tenant_subject": RouteByTenantSubject,
		"TENANT_SUBJECT": RouteByTenantSubject,
		"tenant":         RouteByTenant,
		"":               RouteByTenant,
		"weird":          RouteByTenant,
	}
	for in, want := range cases {
		if got := ParseRouteMode(in); got != want {
			t.Errorf("ParseRouteMode(%q) = %v, want %v", in, got, want)
		}
	}
}
