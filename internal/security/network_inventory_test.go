package security

import "testing"

func TestClassifyNetworkInterface(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		tunnel   bool
		wireless bool
	}{
		{name: "tailscale0", kind: "overlay", tunnel: true},
		{name: "utun4", kind: "tunnel", tunnel: true},
		{name: "wlan0", kind: "wireless", wireless: true},
		{name: "docker0", kind: "virtual"},
		{name: "en0", kind: "physical"},
		{name: "eth0", kind: "ethernet"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, _, tunnel, wireless := classifyNetworkInterface(test.name)
			if kind != test.kind || tunnel != test.tunnel || wireless != test.wireless {
				t.Fatalf("classifyNetworkInterface(%q) = %q tunnel=%v wireless=%v", test.name, kind, tunnel, wireless)
			}
		})
	}
}

func TestLocalNetworkInterfacesNeverReturnsLoopbackOrIPv6(t *testing.T) {
	for _, iface := range LocalNetworkInterfaces() {
		if iface.Name == "lo" || iface.Address == "127.0.0.1" {
			t.Fatalf("loopback leaked into inventory: %#v", iface)
		}
		if iface.Address == "" || iface.CIDR == "" || iface.Kind == "" {
			t.Fatalf("incomplete inventory row: %#v", iface)
		}
	}
}
