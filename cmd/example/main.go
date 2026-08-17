// Command example walks through govelox's use cases against a running stack
// (api-gateway + config-service over REST). It configures rules, drives traffic,
// and prints what each request returned — a quick end-to-end demo and smoke test.
//
// Start the stack first (docker compose --profile stack up, or the Helm chart),
// then:
//
//	go run ./cmd/example
//	go run ./cmd/example -gateway http://localhost:8080 -config http://localhost:8081
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

var (
	gatewayURL = flag.String("gateway", envOr("GATEWAY_URL", "http://localhost:8080"), "api-gateway base URL")
	configURL  = flag.String("config", envOr("CONFIG_URL", "http://localhost:8081"), "config-service base URL")
	client     = &http.Client{Timeout: 5 * time.Second}
)

func main() {
	flag.Parse()
	preflight()

	// A per-run suffix keeps each run isolated from leftover Redis counters.
	run := time.Now().Format("150405")

	scenarioBurst(run)
	scenarioHotReload(run)
	scenarioPerSubject(run)
	scenarioCost(run)
	scenarioSlidingWindow(run)
	scenarioMultiTenant(run)

	fmt.Println("\nAll scenarios completed.")
}

// ── Scenarios ─────────────────────────────────────────────────────────────────

// 1. A GCRA rule (limit 5, burst 5) allows a short burst then throttles.
func scenarioBurst(run string) {
	header("1. Basic rate limit (GCRA burst)")
	tenant := "demo-burst-" + run
	putRule(tenant, "gcra", 5, 5)
	allowed, denied := hammer(tenant, "user-1", 8)
	fmt.Printf("   8 requests → %d allowed (200), %d denied (429)\n", allowed, denied)
	note(allowed == 5, "burst of 5 allowed, the rest throttled")
}

// 2. Changing a rule propagates to the engine with no restart (Postgres→etcd).
func scenarioHotReload(run string) {
	header("2. Hot reload (change a rule at runtime)")
	tenant := "demo-reload-" + run
	putRule(tenant, "gcra", 5, 5)
	a1, _ := hammer(tenant, "before", 8)
	fmt.Printf("   limit=5 → %d allowed\n", a1)

	fmt.Println("   PUT limit=2 …")
	putRule(tenant, "gcra", 2, 2)
	time.Sleep(1500 * time.Millisecond) // let the etcd watch reach the engine
	a2, _ := hammer(tenant, "after", 8)
	fmt.Printf("   limit=2 → %d allowed (no engine restart)\n", a2)
	note(a1 == 5 && a2 == 2, "the new limit took effect live")
}

// 3. Limits are per (tenant, subject): throttling one subject does not affect another.
func scenarioPerSubject(run string) {
	header("3. Per-subject isolation")
	tenant := "demo-subject-" + run
	putRule(tenant, "gcra", 3, 3)
	aA, dA := hammer(tenant, "alice", 6)
	fmt.Printf("   alice: %d allowed, %d denied\n", aA, dA)
	okB, _ := check(tenant, "bob", 1)
	fmt.Printf("   bob (fresh subject): allowed=%v\n", okB)
	note(dA > 0 && okB, "alice is throttled but bob still has budget")
}

// 4. A request can cost more than one token.
func scenarioCost(run string) {
	header("4. Weighted requests (cost > 1)")
	tenant := "demo-cost-" + run
	putRule(tenant, "gcra", 9, 9)
	allowed := 0
	for range 5 {
		if ok, _ := check(tenant, "user-1", 3); ok { // cost 3 each
			allowed++
		}
	}
	fmt.Printf("   5 requests of cost 3 (budget 9) → %d allowed\n", allowed)
	note(allowed == 3, "3 × cost-3 = 9 fits, the 4th is denied")
}

// 5. The sliding-window algorithm is selectable per rule.
func scenarioSlidingWindow(run string) {
	header("5. Sliding-window algorithm")
	tenant := "demo-sw-" + run
	putRule(tenant, "sliding_window", 5, 0)
	allowed, denied := hammer(tenant, "user-1", 8)
	fmt.Printf("   8 requests → %d allowed, %d denied\n", allowed, denied)
	note(allowed == 5, "sliding window enforced the limit of 5")
}

// 6. Tenants are independent.
func scenarioMultiTenant(run string) {
	header("6. Multi-tenant independence")
	a := "demo-tA-" + run
	b := "demo-tB-" + run
	putRule(a, "gcra", 2, 2)
	putRule(b, "gcra", 2, 2)
	aa, _ := hammer(a, "u", 5) // exhaust tenant A
	okB, _ := check(b, "u", 1) // tenant B untouched
	fmt.Printf("   tenant A: %d allowed then throttled | tenant B: allowed=%v\n", aa, okB)
	note(okB, "exhausting tenant A does not affect tenant B")
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

type rule struct {
	Algorithm  string `json:"algorithm"`
	Limit      uint64 `json:"limit"`
	PeriodSecs uint64 `json:"period_secs"`
	Burst      uint64 `json:"burst"`
}

// putRule upserts a rule (60s period) for a tenant via config-service.
func putRule(tenant, algorithm string, limit, burst uint64) {
	body, _ := json.Marshal(rule{Algorithm: algorithm, Limit: limit, PeriodSecs: 60, Burst: burst})
	url := fmt.Sprintf("%s/v1/tenants/%s/rules/default", *configURL, tenant)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fatalf("PUT rule for %s: %v", tenant, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		fatalf("PUT rule for %s: status %d", tenant, resp.StatusCode)
	}
	// Give the config → etcd → engine watch a moment to propagate.
	time.Sleep(400 * time.Millisecond)
}

// check performs one POST /v1/check and reports whether it was allowed.
func check(tenant, subject string, cost uint32) (allowed bool, status int) {
	body, _ := json.Marshal(map[string]any{"tenant_id": tenant, "subject": subject, "cost": cost})
	resp, err := client.Post(*gatewayURL+"/v1/check", "application/json", bytes.NewReader(body))
	if err != nil {
		fatalf("POST /v1/check: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK, resp.StatusCode
}

// hammer sends n cost-1 requests for one (tenant, subject) and counts the outcomes.
func hammer(tenant, subject string, n int) (allowed, denied int) {
	for range n {
		ok, code := check(tenant, subject, 1)
		switch {
		case ok:
			allowed++
		case code == http.StatusTooManyRequests:
			denied++
		default:
			fatalf("unexpected status %d from gateway", code)
		}
	}
	return allowed, denied
}

// ── misc ──────────────────────────────────────────────────────────────────────

func preflight() {
	resp, err := client.Get(*gatewayURL + "/healthz")
	if err != nil {
		fatalf("api-gateway not reachable at %s (%v)\nStart the stack first: docker compose --profile stack up -d", *gatewayURL, err)
	}
	_ = resp.Body.Close()
	fmt.Printf("gateway=%s  config=%s\n", *gatewayURL, *configURL)
}

func header(s string) { fmt.Printf("\n=== %s ===\n", s) }

func note(ok bool, msg string) {
	mark := "✓"
	if !ok {
		mark = "✗"
	}
	fmt.Printf("   %s %s\n", mark, msg)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
