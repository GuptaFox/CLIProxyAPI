package auth

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestPoolCooldownTerminalErrorCapsClaudeResetToPoolDeadline(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lastAccountWeekly := now.Add(99 * time.Hour).Unix()
	lastAccountFiveHour := now.Add(3 * time.Hour).Unix()
	allowedWindowReset := now.Add(120 * time.Hour).Unix()
	upstream := http.Header{}
	upstream.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
	upstream.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(lastAccountWeekly, 10))
	upstream.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day")
	upstream.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
	upstream.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(lastAccountWeekly, 10))
	upstream.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
	upstream.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(lastAccountFiveHour, 10))
	upstream.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "allowed")
	upstream.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", strconv.FormatInt(allowedWindowReset, 10))
	upstream.Set("Request-Id", "req_1")

	// Another credential in the pool recovers in 29 minutes.
	deadline := now.Add(29 * time.Minute)
	err := &poolCooldownTerminalError{cause: headerCarryingError{headers: upstream}, deadline: deadline}

	got := err.Headers()
	want := strconv.FormatInt(deadline.Unix()+1, 10)
	if deadline.Nanosecond() == 0 {
		want = strconv.FormatInt(deadline.Unix(), 10)
	}
	for _, key := range []string{
		"Anthropic-Ratelimit-Unified-Reset",
		"Anthropic-Ratelimit-Unified-7d-Reset",
		"Anthropic-Ratelimit-Unified-5h-Reset",
	} {
		if got.Get(key) != want {
			t.Errorf("%s = %q, want pool deadline %q", key, got.Get(key), want)
		}
	}
	if got.Get("Anthropic-Ratelimit-Unified-7d_oi-Reset") != strconv.FormatInt(allowedWindowReset, 10) {
		t.Errorf("allowed window reset changed to %q", got.Get("Anthropic-Ratelimit-Unified-7d_oi-Reset"))
	}
	if got.Get("Request-Id") != "req_1" || got.Get("Anthropic-Ratelimit-Unified-Representative-Claim") != "seven_day" {
		t.Errorf("unrelated headers changed: %v", got)
	}
	if upstream.Get("Anthropic-Ratelimit-Unified-Reset") != strconv.FormatInt(lastAccountWeekly, 10) {
		t.Error("cause headers were mutated")
	}
}

func TestPoolCooldownTerminalErrorKeepsEarlierClaudeReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	earlier := strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)
	upstream := http.Header{}
	upstream.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
	upstream.Set("Anthropic-Ratelimit-Unified-Reset", earlier)
	upstream.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
	upstream.Set("Anthropic-Ratelimit-Unified-5h-Reset", earlier)

	err := &poolCooldownTerminalError{cause: headerCarryingError{headers: upstream}, deadline: now.Add(time.Hour)}
	got := err.Headers()
	if got.Get("Anthropic-Ratelimit-Unified-Reset") != earlier || got.Get("Anthropic-Ratelimit-Unified-5h-Reset") != earlier {
		t.Fatalf("earlier reset was moved later: %v", got)
	}
}

func TestPoolCooldownTerminalErrorLeavesNonClaudeHeaders(t *testing.T) {
	t.Parallel()

	upstream := http.Header{"X-Codex-Primary-Reset-At": []string{"9999999999"}}
	err := &poolCooldownTerminalError{cause: headerCarryingError{headers: upstream}, deadline: time.Now().Add(time.Hour)}
	if got := err.Headers().Get("X-Codex-Primary-Reset-At"); got != "9999999999" {
		t.Fatalf("non-Claude header changed to %q", got)
	}
}
