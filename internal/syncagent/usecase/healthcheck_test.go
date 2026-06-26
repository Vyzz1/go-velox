package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// scriptedProbe returns results in order; the final result repeats forever.
type scriptedProbe struct {
	mu      sync.Mutex
	results []error
	i       int
}

func (p *scriptedProbe) Probe(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.results[p.i]
	if p.i < len(p.results)-1 {
		p.i++
	}
	return r
}

// recordAdvertiser captures every health transition advertised.
type recordAdvertiser struct {
	ch chan bool
}

func (a *recordAdvertiser) SetEngineHealthy(healthy bool) error {
	a.ch <- healthy
	return nil
}

func fastConfig() HealthCheckConfig {
	return HealthCheckConfig{
		Interval:           2 * time.Millisecond,
		Timeout:            time.Millisecond,
		UnhealthyThreshold: 3,
		HealthyThreshold:   1,
	}
}

func recv(t *testing.T, ch chan bool) bool {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for advertise")
		return false
	}
}

func TestHealthCheck_DebouncesHealthyThenUnhealthy(t *testing.T) {
	// One success flips healthy=true; three consecutive failures flip it back.
	probe := &scriptedProbe{results: []error{
		nil,
		errors.New("x"), errors.New("x"), errors.New("x"),
	}}
	adv := &recordAdvertiser{ch: make(chan bool, 8)}
	uc := NewHealthCheck(probe, adv, fastConfig(), zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go uc.Run(ctx)

	if got := recv(t, adv.ch); got != true {
		t.Fatalf("first advertise = %v, want true", got)
	}
	if got := recv(t, adv.ch); got != false {
		t.Fatalf("second advertise = %v, want false", got)
	}
}

func TestHealthCheck_AdvertisesHealthyOnlyOnChange(t *testing.T) {
	// Always-healthy engine must advertise exactly once, not on every probe.
	probe := &scriptedProbe{results: []error{nil}}
	adv := &recordAdvertiser{ch: make(chan bool, 8)}
	uc := NewHealthCheck(probe, adv, fastConfig(), zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go uc.Run(ctx)

	if got := recv(t, adv.ch); got != true {
		t.Fatalf("advertise = %v, want true", got)
	}
	select {
	case v := <-adv.ch:
		t.Fatalf("unexpected second advertise: %v", v)
	case <-time.After(30 * time.Millisecond):
		// good: stayed quiet across several probe intervals
	}
}
