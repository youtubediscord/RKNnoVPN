package root

import (
	"fmt"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
)

type EgressProbeState struct {
	Ms        int64
	Valid     bool
	CheckedAt time.Time
}

type EgressProbeIO interface {
	ClashDelay(apiPort int, outboundTag string, testURL string, timeoutMS int) (int64, int, error)
	TransparentURLProbe(cfg *config.Config, testURL string, timeoutMS int) (URLProbeMetrics, error)
}

type EgressProbeInput struct {
	State     core.State
	Config    *config.Config
	APIPort   int
	TimeoutMS int
	Cache     EgressProbeState
	Now       time.Time
	IO        EgressProbeIO
}

type EgressProbeResult struct {
	Latency *int64
	Check   health.CheckResult
	Cache   EgressProbeState
}

func RefreshOutboundURLProbe(input EgressProbeInput) EgressProbeResult {
	if input.State != core.StateRunning && input.State != core.StateDegraded {
		return EgressProbeResult{
			Cache: input.Cache,
			Check: health.CheckResult{
				Pass:   false,
				Detail: "runtime is not running",
				Code:   "OUTBOUND_NOT_RUNNING",
			},
		}
	}
	if input.TimeoutMS <= 0 {
		input.TimeoutMS = 2500
	}
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	if input.Cache.Valid && input.Now.Sub(input.Cache.CheckedAt) < 30*time.Second {
		value := input.Cache.Ms
		return EgressProbeResult{
			Latency: &value,
			Cache:   input.Cache,
			Check: health.CheckResult{
				Pass:   true,
				Detail: fmt.Sprintf("recent outbound URL probe ok: %d ms", value),
			},
		}
	}
	if !input.Cache.Valid && !input.Cache.CheckedAt.IsZero() && input.Now.Sub(input.Cache.CheckedAt) < 10*time.Second {
		return EgressProbeResult{
			Cache: input.Cache,
			Check: health.CheckResult{
				Pass:   false,
				Detail: "recent outbound URL probe failed",
				Code:   "OUTBOUND_URL_FAILED",
			},
		}
	}
	if input.IO == nil {
		next := input.Cache
		next.Valid = false
		next.CheckedAt = input.Now
		return EgressProbeResult{
			Cache: next,
			Check: health.CheckResult{
				Pass:   false,
				Detail: "outbound URL probe IO is not configured",
				Code:   "OUTBOUND_URL_FAILED",
			},
		}
	}

	var latency int64
	var err error
	var lastURL string
	for _, testURL := range EgressURLs(input.Config) {
		lastURL = testURL
		if input.APIPort > 0 {
			latency, _, err = input.IO.ClashDelay(input.APIPort, "proxy", testURL, input.TimeoutMS)
		} else {
			metrics, probeErr := input.IO.TransparentURLProbe(input.Config, testURL, input.TimeoutMS)
			latency = metrics.LatencyMS
			err = probeErr
		}
		if err == nil {
			break
		}
	}

	next := input.Cache
	next.CheckedAt = input.Now
	if err != nil {
		next.Valid = false
		return EgressProbeResult{
			Cache: next,
			Check: health.CheckResult{
				Pass:   false,
				Detail: fmt.Sprintf("outbound URL probe failed for %s: %v", lastURL, err),
				Code:   "OUTBOUND_URL_FAILED",
			},
		}
	}
	next.Valid = true
	next.Ms = latency
	value := latency
	return EgressProbeResult{
		Latency: &value,
		Cache:   next,
		Check: health.CheckResult{
			Pass:   true,
			Detail: fmt.Sprintf("outbound URL probe ok: %d ms", latency),
		},
	}
}

func HasRecentEgress(cache EgressProbeState, now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	return cache.Valid && now.Sub(cache.CheckedAt) < 30*time.Second
}

func EgressURLs(cfg *config.Config) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, 3)
	add := func(raw string) {
		url := strings.TrimSpace(raw)
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		result = append(result, url)
	}
	if cfg != nil {
		for _, url := range cfg.Health.EgressURLs {
			add(url)
		}
		add(cfg.Health.URL)
	}
	add("https://www.gstatic.com/generate_204")
	add("https://cp.cloudflare.com/generate_204")
	return result
}
