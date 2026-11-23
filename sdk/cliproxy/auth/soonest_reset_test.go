package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func claudeAuthWithWeeklyReset(id string, reset time.Time) *Auth {
	auth := &Auth{ID: id, Provider: "claude"}
	if !reset.IsZero() {
		auth.Quota.Signals = map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(reset.Unix(), 10),
			"Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		}
		auth.Quota.ObservedAt = time.Now()
	}
	return auth
}

func TestWeeklyQuotaResetAt(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name      string
		signals   map[string]string
		observed  time.Time
		wantReset time.Time
		wantKnown bool
	}{
		{
			name:      "claude 7d reset",
			signals:   map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(now.Unix()+3600, 10)},
			wantReset: now.Add(time.Hour),
			wantKnown: true,
		},
		{
			name:    "claude 5h reset alone is not weekly",
			signals: map[string]string{"Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(now.Unix()+3600, 10)},
		},
		{
			name:      "claude reset in the past is still reported",
			signals:   map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(now.Unix()-1, 10)},
			wantReset: now.Add(-time.Second),
			wantKnown: true,
		},
		{
			name: "codex uses the longer window",
			signals: map[string]string{
				"X-Codex-Primary-Window-Minutes":   "300",
				"X-Codex-Primary-Reset-At":         strconv.FormatInt(now.Unix()+600, 10),
				"X-Codex-Secondary-Window-Minutes": "10080",
				"X-Codex-Secondary-Reset-At":       strconv.FormatInt(now.Unix()+7200, 10),
			},
			wantReset: now.Add(2 * time.Hour),
			wantKnown: true,
		},
		{
			name: "codex reset-after-seconds counts from observation",
			signals: map[string]string{
				"X-Codex-Secondary-Window-Minutes":      "10080",
				"X-Codex-Secondary-Reset-After-Seconds": "7200",
			},
			observed:  now.Add(-time.Hour),
			wantReset: now.Add(time.Hour),
			wantKnown: true,
		},
		{
			name:    "no signals",
			signals: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			auth := &Auth{ID: "a", Quota: QuotaState{Signals: tt.signals, ObservedAt: tt.observed}}
			gotReset, gotKnown := weeklyQuotaResetAt(auth)
			if gotKnown != tt.wantKnown {
				t.Fatalf("known = %v, want %v", gotKnown, tt.wantKnown)
			}
			if tt.wantKnown && !gotReset.Equal(tt.wantReset) {
				t.Fatalf("reset = %v, want %v", gotReset, tt.wantReset)
			}
		})
	}
}

func TestSchedulerPick_SoonestResetPrefersEarliestWeeklyReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	scheduler := newSchedulerForTest(
		&SoonestResetSelector{},
		claudeAuthWithWeeklyReset("a-unknown", time.Time{}),
		claudeAuthWithWeeklyReset("b-later", now.Add(72*time.Hour)),
		claudeAuthWithWeeklyReset("c-sooner", now.Add(24*time.Hour)),
	)

	want := []string{"a-unknown", "c-sooner", "b-later"}
	tried := map[string]struct{}{}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, tried)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil || got.ID != wantID {
			t.Fatalf("pickSingle() #%d = %v, want %q", index, got, wantID)
		}
		tried[got.ID] = struct{}{}
	}
}

func TestSchedulerPick_SoonestResetFollowsNewObservations(t *testing.T) {
	t.Parallel()

	now := time.Now()
	a := claudeAuthWithWeeklyReset("a", now.Add(24*time.Hour))
	b := claudeAuthWithWeeklyReset("b", now.Add(48*time.Hour))
	scheduler := newSchedulerForTest(&SoonestResetSelector{}, a, b)

	got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil || got == nil || got.ID != "a" {
		t.Fatalf("first pick = %v, %v; want a", got, errPick)
	}

	// A fresh response from b reports an earlier weekly reset than a.
	updated := claudeAuthWithWeeklyReset("b", now.Add(time.Hour))
	updated.Generation = 1
	updated.UpdatedAt = now
	scheduler.upsertAuth(updated)

	got, errPick = scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil || got == nil || got.ID != "b" {
		t.Fatalf("pick after update = %v, %v; want b", got, errPick)
	}
}

func TestSoonestResetSelectorPick(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auths := []*Auth{
		claudeAuthWithWeeklyReset("a-unknown", time.Time{}),
		claudeAuthWithWeeklyReset("b-later", now.Add(72*time.Hour)),
		claudeAuthWithWeeklyReset("c-sooner", now.Add(24*time.Hour)),
	}
	got, errPick := (&SoonestResetSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if got.ID != "a-unknown" {
		t.Fatalf("Pick() = %q, want a-unknown (probe first)", got.ID)
	}

	got, errPick = (&SoonestResetSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths[1:])
	if errPick != nil || got.ID != "c-sooner" {
		t.Fatalf("Pick() with known resets = %v, %v; want c-sooner", got, errPick)
	}
}

func TestSoonestResetIsBuiltInSchedulerStrategy(t *testing.T) {
	t.Parallel()

	if got := selectorStrategy(&SoonestResetSelector{}); got != schedulerStrategySoonestReset {
		t.Fatalf("selectorStrategy() = %v, want soonest-reset", got)
	}
	if !isBuiltInSelector(&SoonestResetSelector{}) {
		t.Fatal("isBuiltInSelector() = false, want true")
	}
}

func TestSoonestResetRankOf(t *testing.T) {
	t.Parallel()

	now := time.Now()
	expired := claudeAuthWithWeeklyReset("expired", now.Add(-time.Minute))
	noHeaders := &Auth{ID: "no-headers", Provider: "claude", Quota: QuotaState{
		ObservedAt: now,
		Signals:    map[string]string{"Retry-After": "5"},
	}}
	apiKey := &Auth{ID: "api-key", Provider: "openai-compatibility"}

	tests := []struct {
		name string
		auth *Auth
		want int
	}{
		{"never observed", claudeAuthWithWeeklyReset("new", time.Time{}), soonestResetClassProbe},
		{"reset already passed", expired, soonestResetClassProbe},
		{"known future reset", claudeAuthWithWeeklyReset("known", now.Add(time.Hour)), soonestResetClassKnown},
		{"observed without weekly header", noHeaders, soonestResetClassNoInfo},
		{"provider without quota headers", apiKey, soonestResetClassNoInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := soonestResetRankOf(tt.auth, now).class; got != tt.want {
				t.Fatalf("class = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSchedulerPickMixed_SoonestResetAcrossProviders(t *testing.T) {
	t.Parallel()

	now := time.Now()
	codex := &Auth{ID: "codex-sooner", Provider: "codex", Quota: QuotaState{
		ObservedAt: now,
		Signals: map[string]string{
			"X-Codex-Secondary-Window-Minutes": "10080",
			"X-Codex-Secondary-Reset-At":       strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10),
		},
	}}
	scheduler := newSchedulerForTest(
		&SoonestResetSelector{},
		claudeAuthWithWeeklyReset("claude-later", now.Add(48*time.Hour)),
		codex,
	)

	got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"claude", "codex"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickMixed() error = %v", errPick)
	}
	if got == nil || got.ID != "codex-sooner" || provider != "codex" {
		t.Fatalf("pickMixed() = %v via %q, want codex-sooner via codex", got, provider)
	}
}
