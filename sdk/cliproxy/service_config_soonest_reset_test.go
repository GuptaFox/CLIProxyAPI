package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestSoonestResetRoutingSelector(t *testing.T) {
	for _, value := range []string{"soonest-reset", "SoonestReset", "sr"} {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{
			Routing: internalconfig.RoutingConfig{Strategy: value},
		})
		if state.strategy != "soonest-reset" {
			t.Fatalf("%q: strategy = %q, want soonest-reset", value, state.strategy)
		}
		if _, ok := newRoutingSelector(state).(*coreauth.SoonestResetSelector); !ok {
			t.Fatalf("%q: selector type = %T, want *auth.SoonestResetSelector", value, newRoutingSelector(state))
		}
	}
}
