package auth

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

func codexUsageLimitBody(resetsAt, resetsIn int64) string {
	return `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":` +
		strconv.FormatInt(resetsAt, 10) + `,"eligible_promo":null,"limit_window_minutes":300,"resets_in_seconds":` +
		strconv.FormatInt(resetsIn, 10) + `}}`
}

func TestCapCodexUsageLimitResetUsesPoolDeadline(t *testing.T) {
	t.Parallel()

	// The last account tried resets at 19:42:40; another pool account at 19:08:45.
	now := time.Unix(1_795_460_680, 0)
	lastAccountReset := now.Add(3480 * time.Second)
	deadline := now.Add(24*time.Minute + 5*time.Second)
	body := codexUsageLimitBody(lastAccountReset.Unix(), 3480)

	got := capCodexUsageLimitReset(body, deadline, now)
	if v := ojson.Get(got, "error.resets_at").Int(); v != deadline.Unix() {
		t.Fatalf("resets_at = %d, want %d", v, deadline.Unix())
	}
	if v := ojson.Get(got, "error.resets_in_seconds").Int(); v != int64(24*60+5) {
		t.Fatalf("resets_in_seconds = %d, want %d", v, 24*60+5)
	}
	if ojson.Get(got, "error.type").String() != "usage_limit_reached" || ojson.Get(got, "error.plan_type").String() != "plus" {
		t.Fatalf("other fields changed: %s", got)
	}
}

func TestCapCodexUsageLimitResetKeepsEarlierReset(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_795_460_680, 0)
	body := codexUsageLimitBody(now.Add(10*time.Minute).Unix(), 600)
	if got := capCodexUsageLimitReset(body, now.Add(time.Hour), now); got != body {
		t.Fatalf("earlier reset was changed: %s", got)
	}
}

func TestCapCodexUsageLimitResetIgnoresOtherBodies(t *testing.T) {
	t.Parallel()

	now := time.Now()
	for _, body := range []string{
		`{"type":"error","error":{"type":"rate_limit_error","message":"slow down","resets_at":9999999999}}`,
		`usage_limit_reached but not json`,
		``,
	} {
		if got := capCodexUsageLimitReset(body, now.Add(time.Minute), now); got != body {
			t.Fatalf("body changed: %q -> %q", body, got)
		}
	}
}

func TestPoolCooldownTerminalErrorCapsCodexBody(t *testing.T) {
	t.Parallel()

	now := time.Now()
	deadline := now.Add(20 * time.Minute)
	cause := errors.New(codexUsageLimitBody(now.Add(3*time.Hour).Unix(), 10800))
	err := &poolCooldownTerminalError{cause: cause, deadline: deadline}

	limit := deadline.Unix()
	if deadline.Nanosecond() > 0 {
		limit++
	}
	if v := ojson.Get(err.Error(), "error.resets_at").Int(); v != limit {
		t.Fatalf("Error() resets_at = %d, want %d", v, limit)
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapper no longer unwraps to the upstream error")
	}
}
