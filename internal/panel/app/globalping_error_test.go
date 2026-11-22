package app

import (
	"errors"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/globalping"
)

func TestGlobalpingIPv6ErrorDistinguishesDisabledFromFailed(t *testing.T) {
	if got := globalpingIPv6Error(globalping.ErrDisabled); !errors.Is(got, fleet.ErrGlobalpingOff) {
		t.Fatalf("disabled error = %v, want ErrGlobalpingOff", got)
	}
	refused := errors.New("Globalping refused the request")
	if got := globalpingIPv6Error(refused); got != refused {
		t.Fatalf("request error = %v, want the original failure", got)
	}
}
