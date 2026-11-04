package sensor

import (
	"fmt"
	"net"
	"sort"
)

const (
	maxDeniedCIDRs = 128
	maxDeniedPorts = 128
)

type NetworkPolicy struct {
	DeniedCIDRs []*net.IPNet
	DeniedPorts map[int]bool
}

func ValidatePolicy(cidrs []string, ports []int) (NetworkPolicy, error) {
	if len(cidrs) > maxDeniedCIDRs {
		return NetworkPolicy{}, fmt.Errorf("too many denied CIDRs (max %d)", maxDeniedCIDRs)
	}
	if len(ports) > maxDeniedPorts {
		return NetworkPolicy{}, fmt.Errorf("too many denied ports (max %d)", maxDeniedPorts)
	}
	policy := NetworkPolicy{DeniedPorts: make(map[int]bool, len(ports))}
	for _, raw := range cidrs {
		_, network, err := net.ParseCIDR(raw)
		if err != nil || network.IP.To4() == nil {
			return NetworkPolicy{}, fmt.Errorf("denied CIDR %q is not a valid IPv4 network", raw)
		}
		network.IP = network.IP.To4()
		policy.DeniedCIDRs = append(policy.DeniedCIDRs, network)
	}
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return NetworkPolicy{}, fmt.Errorf("denied port %d is outside 1..65535", port)
		}
		policy.DeniedPorts[port] = true
	}
	sort.Slice(policy.DeniedCIDRs, func(i, j int) bool {
		return policy.DeniedCIDRs[i].String() < policy.DeniedCIDRs[j].String()
	})
	return policy, nil
}

func (p NetworkPolicy) Denies(ip net.IP, port int) bool {
	if p.DeniedPorts[port] {
		return true
	}
	ipv4 := ip.To4()
	if ipv4 == nil {
		return false
	}
	for _, network := range p.DeniedCIDRs {
		if network.Contains(ipv4) {
			return true
		}
	}
	return false
}
