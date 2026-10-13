// Package netplan manages host networking for microVMs: the shared bridge,
// per-VM tap devices, DNAT port-forwards for SSH, and the address/port
// allocation logic.
package netplan

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// Manager owns host-side network plumbing. All methods are idempotent so the
// daemon can re-run them safely at every startup.
type Manager struct {
	Bridge     string
	HostCIDR   string
	Subnet     *net.IPNet
	ExternalIP string
}

// New builds a Manager.
func New(bridge, hostCIDR, externalIP string) (*Manager, error) {
	ip, ipnet, err := net.ParseCIDR(hostCIDR)
	if err != nil {
		return nil, fmt.Errorf("host cidr: %w", err)
	}
	if !ipnet.Contains(ip) {
		return nil, fmt.Errorf("host cidr %q does not contain its own address", hostCIDR)
	}
	return &Manager{Bridge: bridge, HostCIDR: hostCIDR, Subnet: ipnet, ExternalIP: externalIP}, nil
}

func run(cmd string, args ...string) error {
	out, err := exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", cmd, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ruleTagged reports whether chain in table already has a rule carrying the
// comment tag. Matching on `iptables -S` output instead of `iptables -C`:
// the nftables backend normalizes rules, which breaks -C's exact matching
// and caused duplicate rules on every daemon restart.
func ruleTagged(table, chain, tag string) bool {
	out, err := exec.Command("iptables", "-t", table, "-S", chain).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), `"microvms:`+tag+`"`)
}

// EnsureHostNetworking brings up the bridge, enables forwarding and installs
// the shared NAT rules. Safe to call repeatedly.
func (m *Manager) EnsureHostNetworking() error {
	// Bridge up with its address.
	if err := run("ip", "link", "show", m.Bridge); err != nil {
		if err := run("ip", "link", "add", m.Bridge, "type", "bridge"); err != nil {
			return fmt.Errorf("create bridge: %w", err)
		}
	}
	_ = run("ip", "addr", "replace", m.HostCIDR, "dev", m.Bridge)
	if err := run("ip", "link", "set", m.Bridge, "up"); err != nil {
		return err
	}

	if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}

	// NAT for VM outbound traffic.
	if !ruleTagged("nat", "POSTROUTING", "nat") {
		masq := []string{
			"-t", "nat", "-A", "POSTROUTING",
			"-s", m.Subnet.String(), "!", "-o", m.Bridge,
			"-m", "comment", "--comment", "microvms:nat", "-j", "MASQUERADE",
		}
		if err := run("iptables", masq...); err != nil {
			return err
		}
	}

	// Accept forwarded traffic in both directions.
	if !ruleTagged("filter", "FORWARD", "fwd") {
		fwd := []string{"-A", "FORWARD", "-i", m.Bridge, "-m", "comment", "--comment", "microvms:fwd", "-j", "ACCEPT"}
		if err := run("iptables", fwd...); err != nil {
			return err
		}
	}
	if !ruleTagged("filter", "FORWARD", "fwd-back") {
		fwdBack := []string{
			"-A", "FORWARD",
			"-o", m.Bridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED",
			"-m", "comment", "--comment", "microvms:fwd-back", "-j", "ACCEPT",
		}
		if err := run("iptables", fwdBack...); err != nil {
			return err
		}
	}
	return nil
}

// AddTap creates a tap device enslaved to the bridge.
func (m *Manager) AddTap(name string) error {
	if err := run("ip", "tuntap", "add", "mode", "tap", name); err != nil {
		// tolerate "already exists" so retries after a crash work
		if !strings.Contains(err.Error(), "exists") {
			return err
		}
	}
	if err := run("ip", "link", "set", name, "master", m.Bridge); err != nil {
		return err
	}
	return run("ip", "link", "set", name, "up")
}

// DeleteTap removes a tap device.
func (m *Manager) DeleteTap(name string) error {
	err := run("ip", "link", "delete", name)
	if err != nil && strings.Contains(err.Error(), "Cannot find") {
		return nil
	}
	return err
}

// TapName returns the deterministic tap name for a VM id.
func TapName(vmID string) string {
	if len(vmID) > 11 {
		vmID = vmID[:11]
	}
	return "t-" + vmID
}

// PortDNAT installs NAT/filter rules forwarding hostIP:port -> vmIP:22.
// The OUTPUT-chain copy makes the forward work for clients on the host itself.
func (m *Manager) PortDNAT(port int, vmIP, vmID string) error {
	dnat := func(chain string) []string {
		return []string{
			"-t", "nat", "-A", chain,
			"-d", m.ExternalIP, "-p", "tcp", "--dport", fmt.Sprint(port),
			"-m", "comment", "--comment", "microvms:" + vmID,
			"-j", "DNAT", "--to-destination", vmIP + ":22",
		}
	}
	for _, chain := range []string{"PREROUTING", "OUTPUT"} {
		if !ruleTagged("nat", chain, vmID) {
			if err := run("iptables", dnat(chain)...); err != nil {
				return err
			}
		}
	}
	if !ruleTagged("filter", "FORWARD", vmID) {
		fwdIn := []string{
			"-A", "FORWARD", "-d", vmIP, "-p", "tcp", "--dport", "22",
			"-m", "comment", "--comment", "microvms:" + vmID, "-j", "ACCEPT",
		}
		if err := run("iptables", fwdIn...); err != nil {
			return err
		}
		fwdOut := []string{
			"-A", "FORWARD", "-s", vmIP, "-p", "tcp", "--sport", "22",
			"-m", "comment", "--comment", "microvms:" + vmID, "-j", "ACCEPT",
		}
		if err := run("iptables", fwdOut...); err != nil {
			return err
		}
	}
	return nil
}

// RemovePortDNAT removes every rule tagged for this VM by enumerating
// `iptables -S` and deleting matches. Matching by comment tag instead of a
// reconstructed spec keeps cleanup working when the host's external IP
// changed between create and delete (e.g. a roaming laptop).
func (m *Manager) RemovePortDNAT(vmID string) {
	for _, c := range []struct{ table, chain string }{
		{"nat", "PREROUTING"},
		{"nat", "OUTPUT"},
		{"filter", "FORWARD"},
	} {
		out, err := exec.Command("iptables", "-t", c.table, "-S", c.chain).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(line, "microvms:"+vmID+"\"") {
				continue
			}
			spec := strings.TrimSpace(strings.TrimPrefix(line, "-A "+c.chain+" "))
			spec = strings.ReplaceAll(spec, `"`, "")
			_ = run("iptables", append([]string{"-t", c.table, "-D", c.chain}, strings.Fields(spec)...)...)
		}
	}
}

// AllocateIP picks the first free address in [start, end] given the set of
// addresses already in use.
func AllocateIP(start, end string, used map[string]bool) (string, error) {
	s := net.ParseIP(start).To4()
	e := net.ParseIP(end).To4()
	if s == nil || e == nil {
		return "", fmt.Errorf("invalid ip range %q-%q", start, end)
	}
	for ip := s; cmpIP(ip, e) <= 0; ip = nextIP(ip) {
		if !used[ip.String()] {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("ip pool exhausted (%s-%s)", start, end)
}

// AllocatePort picks the first free port >= base given ports already in use.
func AllocatePort(base int, used map[int]bool) int {
	for p := base; p < base+1000; p++ {
		if !used[p] {
			return p
		}
	}
	return 0
}

// MACFor derives a locally-administered unicast MAC from the VM id.
func MACFor(vmID string) string {
	sum := fnv32(vmID)
	return fmt.Sprintf("02:fc:%02x:%02x:%02x:%02x", byte(sum>>24), byte(sum>>16), byte(sum>>8), byte(sum))
}

func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func cmpIP(a, b net.IP) int {
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

func nextIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			break
		}
	}
	return out
}
