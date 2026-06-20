package usecase

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/syncagent/domain"
)

// HealthCheckConfig tunes the engine health-check loop. All thresholds are in
// consecutive probe counts; durations are passed as time.Duration by the
// composition root (converted there from env millisecond knobs).
type HealthCheckConfig struct {
	Interval           time.Duration // delay between probes
	Timeout            time.Duration // per-probe deadline
	UnhealthyThreshold int           // consecutive failures before marking unhealthy
	HealthyThreshold   int           // consecutive successes before marking healthy
}

// HealthCheckUseCase periodically probes the co-located limiter-engine and ties
// its observed health to the node's gossip-advertised routability. Consecutive
// failure/success thresholds debounce transient flaps so a single missed probe
// does not pull the engine out of the gateway's hash ring.
type HealthCheckUseCase struct {
	probe      domain.EngineProbe      // port: checks the engine
	advertiser domain.HealthAdvertiser // port: propagates health via gossip
	cfg        HealthCheckConfig
	log        *zap.Logger
}

// NewHealthCheck wires the probe and advertiser ports. Defaults are applied for
// any non-positive config field so the loop is always well-formed.
func NewHealthCheck(probe domain.EngineProbe, adv domain.HealthAdvertiser, cfg HealthCheckConfig, log *zap.Logger) *HealthCheckUseCase {
	if cfg.Interval <= 0 {
		cfg.Interval = 2 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = time.Second
	}
	if cfg.UnhealthyThreshold <= 0 {
		cfg.UnhealthyThreshold = 3
	}
	if cfg.HealthyThreshold <= 0 {
		cfg.HealthyThreshold = 1
	}
	return &HealthCheckUseCase{probe: probe, advertiser: adv, cfg: cfg, log: log}
}

// Run blocks until ctx is canceled, probing the engine on each tick. The node
// starts unhealthy (matching the cluster adapter's initial meta) and is only
// advertised healthy after HealthyThreshold consecutive successes; it flips back
// to unhealthy after UnhealthyThreshold consecutive failures.
func (uc *HealthCheckUseCase) Run(ctx context.Context) {
	ticker := time.NewTicker(uc.cfg.Interval)
	defer ticker.Stop()

	var fails, rises int
	healthy := false

	check := func() {
		pctx, cancel := context.WithTimeout(ctx, uc.cfg.Timeout)
		err := uc.probe.Probe(pctx)
		cancel()

		if err != nil {
			rises = 0
			fails++
			if healthy && fails >= uc.cfg.UnhealthyThreshold {
				healthy = false
				uc.advertise(false)
				uc.log.Warn("engine marked unhealthy",
					zap.Int("consecutive_failures", fails), zap.Error(err))
			} else if !healthy {
				uc.log.Debug("engine probe failed",
					zap.Int("consecutive_failures", fails), zap.Error(err))
			}
			return
		}

		fails = 0
		rises++
		if !healthy && rises >= uc.cfg.HealthyThreshold {
			healthy = true
			uc.advertise(true)
			uc.log.Info("engine marked healthy",
				zap.Int("consecutive_successes", rises))
		}
	}

	check() // probe immediately rather than waiting a full interval
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func (uc *HealthCheckUseCase) advertise(healthy bool) {
	if err := uc.advertiser.SetEngineHealthy(healthy); err != nil {
		uc.log.Warn("failed to advertise engine health",
			zap.Bool("healthy", healthy), zap.Error(err))
	}
}
