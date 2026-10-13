// Package config loads and validates the microvmsd daemon configuration.
package config

import (
	"fmt"
	"net"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is the daemon configuration, loaded from a TOML file.
type Config struct {
	// Listen is the address the REST API binds to, e.g. "0.0.0.0:8420".
	Listen string `toml:"listen"`

	// ExternalIP is the host IP agents use to reach the API and the VM SSH
	// ports. If empty it is auto-detected from the default route.
	ExternalIP string `toml:"external_ip"`

	// DataDir holds per-VM disks, sockets, logs and the state file.
	DataDir string `toml:"data_dir"`

	// Bridge is the Linux bridge VM taps attach to.
	Bridge string `toml:"bridge"`

	// HostCIDR is the bridge address in CIDR form, e.g. "172.16.100.1/24".
	// The bridge address doubles as the VM default gateway.
	HostCIDR string `toml:"host_cidr"`

	// IPStart/IPEnd bound the pool of addresses handed out to VMs.
	IPStart string `toml:"ip_start"`
	IPEnd   string `toml:"ip_end"`

	// SSHPortBase is the first host port used for DNAT-forwarded SSH
	// (VM N gets SSHPortBase + N counting up from the first free port).
	SSHPortBase int `toml:"ssh_port_base"`

	// APITokens are the bearer tokens accepted on /v1 endpoints.
	APITokens []string `toml:"api_tokens"`

	// Defaults and limits for created VMs.
	DefaultVCPUs int `toml:"default_vcpus"`
	DefaultMem   int `toml:"default_mem_mib"`
	MaxVCPUs     int `toml:"max_vcpus"`
	MaxMemMib    int `toml:"max_mem_mib"`
	MaxVMs       int `toml:"max_vms"`

	// Paths to shared assets.
	FirecrackerBin string `toml:"firecracker_bin"`
	KernelPath     string `toml:"kernel_path"`
	RootFSPath     string `toml:"rootfs_path"`

	// GracefulTimeoutSec is how long to wait after SendCtrlAltDel before
	// killing the firecracker process.
	GracefulTimeoutSec int `toml:"graceful_shutdown_timeout_sec"`

	// DevUnprivileged skips the root check and tolerates host-networking
	// setup failures, for API-level testing on dev machines. VM creation
	// still fails without CAP_NET_ADMIN.
	DevUnprivileged bool `toml:"dev_unprivileged"`
}

// Default returns a Config populated with sane defaults.
func Default() Config {
	return Config{
		Listen:              "0.0.0.0:8420",
		DataDir:             "/var/lib/microvms",
		Bridge:              "fcbr0",
		HostCIDR:            "172.16.100.1/24",
		IPStart:             "172.16.100.10",
		IPEnd:               "172.16.100.254",
		SSHPortBase:         2200,
		DefaultVCPUs:        1,
		DefaultMem:          256,
		MaxVCPUs:            4,
		MaxMemMib:           2048,
		MaxVMs:              20,
		FirecrackerBin:      "firecracker",
		KernelPath:          "/var/lib/microvms/images/vmlinux",
		RootFSPath:          "/var/lib/microvms/images/rootfs.ext4",
		GracefulTimeoutSec:  10,
	}
}

// Load reads path and overlays it on Default().
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	if _, err := os.Stat(path); err != nil {
		return cfg, fmt.Errorf("config file: %w", err)
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks the assembled configuration for obvious errors.
func (c *Config) Validate() error {
	if _, _, err := net.ParseCIDR(c.HostCIDR); err != nil {
		return fmt.Errorf("host_cidr %q invalid: %v", c.HostCIDR, err)
	}
	if net.ParseIP(c.IPStart) == nil || net.ParseIP(c.IPEnd) == nil {
		return fmt.Errorf("ip_start/ip_end invalid: %q-%q", c.IPStart, c.IPEnd)
	}
	if len(c.APITokens) == 0 {
		return fmt.Errorf("no api_tokens configured - refusing to run an open VM-control API")
	}
	if c.SSHPortBase < 1024 {
		return fmt.Errorf("ssh_port_base must be >= 1024")
	}
	if c.DefaultVCPUs < 1 || c.DefaultMem < 64 {
		return fmt.Errorf("default_vcpus/default_mem_mib too small")
	}
	if c.MaxVCPUs < c.DefaultVCPUs || c.MaxMemMib < c.DefaultMem {
		return fmt.Errorf("max_vcpus/max_mem_mib below defaults")
	}
	return nil
}

// GatewayIP returns the bridge address without the mask.
func (c *Config) GatewayIP() string {
	ip, _, _ := net.ParseCIDR(c.HostCIDR)
	return ip.String()
}

// Subnet returns the bridge subnet as *net.IPNet.
func (c *Config) Subnet() *net.IPNet {
	_, n, _ := net.ParseCIDR(c.HostCIDR)
	return n
}

// DetectExternalIP finds the source IP the host would use to reach the
// internet and is a good default for the address agents connect to.
func DetectExternalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
