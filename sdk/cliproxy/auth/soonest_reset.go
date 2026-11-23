package auth

import (
	"context"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// SoonestResetSelector prefers the credential whose weekly quota window resets
// soonest, so quota that would otherwise expire unused is spent first.
//
// Reset times come from the passive quota snapshot (QuotaState.Signals) taken
// from each credential's latest upstream response. Candidates are ranked:
//  1. Credentials whose provider reports quota but whose weekly reset is not
//     known yet (never observed, or the observed reset has passed). One
//     response teaches the scheduler when they reset next.
//  2. Credentials with a known future weekly reset, soonest first.
//  3. Credentials with no quota information (for example API keys).
//
// Ties keep the deterministic order fill-first uses. Credentials that hit a
// short-window limit are already excluded by the normal cooldown handling.
type SoonestResetSelector struct{}

// Pick selects the available auth with the soonest weekly quota reset.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	best := 0
	bestRank := soonestResetRankOf(available[0], now)
	for i := 1; i < len(available); i++ {
		if rank := soonestResetRankOf(available[i], now); rank.before(bestRank) {
			best, bestRank = i, rank
		}
	}
	return available[best], nil
}

// pickSoonestReset returns the matching entry that ranks first under the
// soonest-reset strategy. Ties keep the view's order, matching pickFirst.
func (v *readyView) pickSoonestReset(predicate func(*scheduledAuth) bool, now time.Time) *scheduledAuth {
	var best *scheduledAuth
	var bestRank soonestResetRank
	for _, entry := range v.flat {
		if predicate != nil && !predicate(entry) {
			continue
		}
		rank := soonestResetRankOf(entry.auth, now)
		if best == nil || rank.before(bestRank) {
			best, bestRank = entry, rank
		}
	}
	return best
}

const (
	soonestResetClassProbe = iota
	soonestResetClassKnown
	soonestResetClassNoInfo
)

// soonestResetRank orders credentials for the soonest-reset strategy.
type soonestResetRank struct {
	class int
	reset time.Time
}

// before reports whether r should be picked ahead of other.
func (r soonestResetRank) before(other soonestResetRank) bool {
	if r.class != other.class {
		return r.class < other.class
	}
	return r.class == soonestResetClassKnown && r.reset.Before(other.reset)
}

func soonestResetRankOf(auth *Auth, now time.Time) soonestResetRank {
	if auth == nil {
		return soonestResetRank{class: soonestResetClassNoInfo}
	}
	reset, found := weeklyQuotaResetAt(auth)
	if found && reset.After(now) {
		return soonestResetRank{class: soonestResetClassKnown, reset: reset}
	}
	if ProviderSupportsQuotaObservation(auth.Provider) && (auth.Quota.ObservedAt.IsZero() || found) {
		return soonestResetRank{class: soonestResetClassProbe}
	}
	return soonestResetRank{class: soonestResetClassNoInfo}
}

const (
	claudeWeeklyResetSignal = "anthropic-ratelimit-unified-7d-reset"
)

// weeklyQuotaResetAt returns when the credential's weekly (longest) quota window
// resets, based on the latest observed upstream quota headers. The returned time
// may already be in the past. It reports false when no weekly reset was observed.
func weeklyQuotaResetAt(auth *Auth) (time.Time, bool) {
	if auth == nil || len(auth.Quota.Signals) == 0 {
		return time.Time{}, false
	}
	signals := auth.Quota.Signals
	if raw, ok := quotaSignal(signals, claudeWeeklyResetSignal); ok {
		return parseQuotaResetTime(raw)
	}

	// Codex reports up to two windows (primary and secondary). Use the longer
	// one, which is the weekly window on subscription plans.
	var best time.Time
	bestMinutes := int64(-1)
	for _, window := range []string{"primary", "secondary"} {
		prefix := "x-codex-" + window + "-"
		minutesRaw, ok := quotaSignal(signals, prefix+"window-minutes")
		if !ok {
			continue
		}
		minutes, errParse := strconv.ParseInt(strings.TrimSpace(minutesRaw), 10, 64)
		if errParse != nil || minutes <= 0 || minutes <= bestMinutes {
			continue
		}
		reset, okReset := codexWindowReset(signals, prefix, auth.Quota.ObservedAt)
		if !okReset {
			continue
		}
		best, bestMinutes = reset, minutes
	}
	if bestMinutes < 0 {
		return time.Time{}, false
	}
	return best, true
}

func codexWindowReset(signals map[string]string, prefix string, observedAt time.Time) (time.Time, bool) {
	if raw, ok := quotaSignal(signals, prefix+"reset-at"); ok {
		if reset, okParse := parseQuotaResetTime(raw); okParse {
			return reset, true
		}
	}
	if raw, ok := quotaSignal(signals, prefix+"reset-after-seconds"); ok && !observedAt.IsZero() {
		seconds, errParse := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if errParse == nil && seconds >= 0 {
			return observedAt.Add(time.Duration(seconds * float64(time.Second))), true
		}
	}
	return time.Time{}, false
}

// quotaSignal looks up a signal by header name, ignoring case.
func quotaSignal(signals map[string]string, name string) (string, bool) {
	for key, value := range signals {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}

// parseQuotaResetTime accepts Unix seconds (optionally fractional) or RFC 3339.
func parseQuotaResetTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if seconds, errParse := strconv.ParseFloat(raw, 64); errParse == nil {
		if seconds <= 0 {
			return time.Time{}, false
		}
		whole := int64(seconds)
		return time.Unix(whole, int64((seconds-float64(whole))*float64(time.Second))), true
	}
	if parsed, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
		return parsed, true
	}
	return time.Time{}, false
}
