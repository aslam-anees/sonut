package security

import (
	"net"
	"sort"
	"strings"
)

// NetworkInterface is a safe, agentless snapshot of one local interface
// address. It intentionally excludes hardware addresses and does not probe the
// network. Active discovery belongs behind an explicit user action.
type NetworkInterface struct {
	Name     string
	Label    string
	Address  string
	CIDR     string
	Kind     string
	Tunnel   bool
	Wireless bool
}

// LocalNetworkInterfaces inventories operational local interfaces using only
// the Go standard library. The shape is inspired by LAN-Orangutan's useful
// multi-interface inventory, without its nmap/arp-scan side effects or elevated
// privilege requirements.
func LocalNetworkInterfaces() []NetworkInterface {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	result := make([]NetworkInterface, 0, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		kind, label, tunnel, wireless := classifyNetworkInterface(iface.Name)
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, network, err := net.ParseCIDR(address.String())
			if err != nil || ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}
			result = append(result, NetworkInterface{
				Name:     iface.Name,
				Label:    label,
				Address:  ip.String(),
				CIDR:     network.String(),
				Kind:     kind,
				Tunnel:   tunnel,
				Wireless: wireless,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].Address < result[j].Address
	})
	return result
}

func classifyNetworkInterface(name string) (kind, label string, tunnel, wireless bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasPrefix(lower, "tailscale"):
		return "overlay", "Tailscale", true, false
	case strings.HasPrefix(lower, "utun"), strings.HasPrefix(lower, "tun"), strings.HasPrefix(lower, "tap"), strings.HasPrefix(lower, "wg"):
		return "tunnel", "Private tunnel", true, false
	case strings.HasPrefix(lower, "wlan"), strings.HasPrefix(lower, "wifi"), strings.HasPrefix(lower, "wl"):
		return "wireless", "Wi-Fi", false, true
	case strings.HasPrefix(lower, "docker"), strings.HasPrefix(lower, "br-"), strings.HasPrefix(lower, "veth"), strings.HasPrefix(lower, "podman"), strings.HasPrefix(lower, "cni"):
		return "virtual", "Container bridge", false, false
	case strings.HasPrefix(lower, "en"):
		// Darwin commonly names both Wi-Fi and Ethernet interfaces enN. Avoid
		// pretending we can distinguish them without platform-specific APIs.
		return "physical", "Local network", false, false
	case strings.HasPrefix(lower, "eth"):
		return "ethernet", "Ethernet", false, false
	default:
		return "network", "Local network", false, false
	}
}
