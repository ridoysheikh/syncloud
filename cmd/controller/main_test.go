package main

import (
	"net/netip"
	"testing"

	"syncloud/internal/config"
)

func TestDevAddress(t *testing.T) {
	if got := devAddress(config.Controller{AgentAdvertise: "172.18.0.2:7443"}); got != "172.18.0.2" {
		t.Fatalf("advertise IP: got %s", got)
	}
	// Not an address (a name, a wildcard listen, loopback): the host's own.
	for _, adv := range []string{"", ":7443", "0.0.0.0:7443", "127.0.0.1:7443", "ctl.example.com:7443"} {
		got := devAddress(config.Controller{AgentAdvertise: adv})
		if a, err := netip.ParseAddr(got); err != nil || !a.Is4() || a.IsUnspecified() {
			t.Errorf("%q: got %q", adv, got)
		}
	}
}
