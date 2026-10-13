// Package vm implements microVM lifecycle on top of firecracker-go-sdk.
package vm

import (
	"errors"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"golang.org/x/crypto/ssh"

	"microvms/internal/config"
	"microvms/internal/netplan"
	"microvms/internal/store"
)

// CreateParams are the caller-supplied settings for a new VM.
type CreateParams struct {
	Name   string `json:"name"`
	VCPUs  int    `json:"cpus"`
	MemMib int    `json:"mem_mib"`
	SSHKey string `json:"ssh_key"` // public key in authorized_keys format
}

// ErrNotFound is returned when a VM id does not exist.
var ErrNotFound = errors.New("vm not found")

// Capacity reports what the daemon can still hand out.
type Capacity struct {
	MaxVMs          int `json:"max_vms"`
	ExistingVMs     int `json:"existing_vms"`
	RunningVMs      int `json:"running_vms"`
	MaxVCPUsPerVM   int `json:"max_vcpus_per_vm"`
	MaxMemMibPerVM  int `json:"max_mem_mib_per_vm"`
	FreeHostMemMib  int `json:"free_host_mem_mib"`
	FreeHostCPUs    int `json:"free_host_cpus"`
}

// Manager owns all running firecracker machines.
type Manager struct {
	cfg   *config.Config
	store *store.Store
	net   *netplan.Manager

	mu      sync.Mutex
	running map[string]*managedVM
}

type managedVM struct {
	id      string
	machine *firecracker.Machine
	cancel  context.CancelFunc
	// netOnce guarantees tap/DNAT cleanup runs exactly once whether the VM
	// exits on its own, is stopped, or fails mid-boot.
	netOnce sync.Once
	cleanup func()
	exited  chan struct{}
}

// NewManager builds a Manager, brings up host networking, and reconciles
// leftover state from a previous daemon run.
func NewManager(cfg *config.Config, st *store.Store, np *netplan.Manager) (*Manager, error) {
	m := &Manager{cfg: cfg, store: st, net: np, running: map[string]*managedVM{}}
	if err := np.EnsureHostNetworking(); err != nil {
		if !cfg.DevUnprivileged {
			return nil, fmt.Errorf("host networking: %w", err)
		}
		log.Printf("WARNING: dev_unprivileged mode - host networking setup failed: %v", err)
	}
	m.reconcile()
	return m, nil
}

func (m *Manager) vmdir(id string) string {
	return filepath.Join(m.cfg.DataDir, "vms", id)
}

// Record returns a copy of one VM record.
func (m *Manager) Record(id string) (store.VMRecord, bool) {
	return m.store.Get(id)
}

// AllRecords returns every VM record.
func (m *Manager) AllRecords() []store.VMRecord {
	return m.store.All()
}

// Capacity reports current headroom.
func (m *Manager) Capacity() Capacity {
	c := Capacity{
		MaxVMs:         m.cfg.MaxVMs,
		ExistingVMs:    m.store.Count(),
		MaxVCPUsPerVM:  m.cfg.MaxVCPUs,
		MaxMemMibPerVM: m.cfg.MaxMemMib,
		FreeHostMemMib: memAvailableMib(),
		FreeHostCPUs:   runtime.NumCPU(),
	}
	m.mu.Lock()
	c.RunningVMs = len(m.running)
	m.mu.Unlock()
	return c
}

// Create provisions and boots a new VM. The second return value is the
// OpenSSH PEM private key, set only when the caller did not supply one.
func (m *Manager) Create(ctx context.Context, p CreateParams) (store.VMRecord, string, error) {
	c := m.Capacity()
	if c.ExistingVMs >= m.cfg.MaxVMs {
		return store.VMRecord{}, "", fmt.Errorf("max_vms (%d) reached; delete VMs first", m.cfg.MaxVMs)
	}
	if p.VCPUs == 0 {
		p.VCPUs = m.cfg.DefaultVCPUs
	}
	if p.MemMib == 0 {
		p.MemMib = m.cfg.DefaultMem
	}
	if p.VCPUs < 1 || p.VCPUs > m.cfg.MaxVCPUs {
		return store.VMRecord{}, "", fmt.Errorf("cpus must be between 1 and %d", m.cfg.MaxVCPUs)
	}
	if p.MemMib < 64 || p.MemMib > m.cfg.MaxMemMib {
		return store.VMRecord{}, "", fmt.Errorf("mem_mib must be between 64 and %d", m.cfg.MaxMemMib)
	}
	if p.MemMib+128 > c.FreeHostMemMib {
		return store.VMRecord{}, "", fmt.Errorf("insufficient host memory: %d MiB free, need %d", c.FreeHostMemMib, p.MemMib+128)
	}

	// Resolve the SSH key: use the caller's, or generate a throwaway pair.
	pubLine := strings.TrimSpace(p.SSHKey)
	privPEM := ""
	if pubLine == "" {
		var err error
		pubLine, privPEM, err = generateKeypair()
		if err != nil {
			return store.VMRecord{}, "", fmt.Errorf("generate ssh key: %w", err)
		}
	} else if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubLine)); err != nil {
		return store.VMRecord{}, "", fmt.Errorf("ssh_key is not a valid public key: %v", err)
	}

	rec, err := m.reserve(p, pubLine, privPEM != "")
	if err != nil {
		return store.VMRecord{}, "", err
	}

	if err := m.provisionDisk(rec, pubLine, privPEM); err != nil {
		m.store.Delete(rec.ID)
		return store.VMRecord{}, "", fmt.Errorf("provision: %w", err)
	}
	if err := m.boot(rec); err != nil {
		m.store.UpdateState(rec.ID, store.StateError)
		m.releaseNetwork(rec)
		return store.VMRecord{}, "", fmt.Errorf("boot: %w", err)
	}
	final, _ := m.store.Get(rec.ID)
	return final, privPEM, nil
}

// reserve allocates identity and resources, persisting a "creating" record.
func (m *Manager) reserve(p CreateParams, pubKey string, generated bool) (store.VMRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing := m.store.All()
	if len(existing) >= m.cfg.MaxVMs {
		return store.VMRecord{}, fmt.Errorf("max_vms (%d) reached", m.cfg.MaxVMs)
	}
	usedIPs := map[string]bool{m.cfg.GatewayIP(): true}
	usedPorts := map[int]bool{}
	for _, r := range existing {
		usedIPs[r.IP] = true
		usedPorts[r.SSHPort] = true
	}
	ip, err := netplan.AllocateIP(m.cfg.IPStart, m.cfg.IPEnd, usedIPs)
	if err != nil {
		return store.VMRecord{}, err
	}
	port := netplan.AllocatePort(m.cfg.SSHPortBase, usedPorts)
	if port == 0 {
		return store.VMRecord{}, fmt.Errorf("ssh port pool exhausted")
	}

	id := randomID()
	if p.Name == "" {
		p.Name = "vm-" + id[:8]
	}
	rec := store.VMRecord{
		ID:           id,
		Name:         p.Name,
		MAC:          netplan.MACFor(id),
		IP:           ip,
		SSHPort:      port,
		VCPUs:        p.VCPUs,
		MemMib:       p.MemMib,
		State:        store.StateCreating,
		CreatedAt:    time.Now().UTC(),
		SSHPubKey:    pubKey,
		KeyGenerated: generated,
	}
	if err := m.store.Put(rec); err != nil {
		return store.VMRecord{}, err
	}
	return rec, nil
}

// provisionDisk copies the base rootfs and writes the guest config disk. When
// the daemon generated the guest keypair it keeps the private half (0600,
// root-only) so Stop can power the guest off cleanly over SSH.
func (m *Manager) provisionDisk(rec store.VMRecord, pubKey, privPEM string) error {
	dir := m.vmdir(rec.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := copyFile(m.cfg.RootFSPath, rootfs); err != nil {
		return fmt.Errorf("copy rootfs: %w", err)
	}
	if privPEM != "" {
		if err := os.WriteFile(filepath.Join(dir, "guest.key"), []byte(privPEM), 0o600); err != nil {
			return fmt.Errorf("store guest key: %w", err)
		}
	}
	return os.WriteFile(filepath.Join(dir, "config.img"), configDisk(m.cfg, rec, pubKey), 0o644)
}

// configDisk renders the raw key=value block the guest init reads from
// /dev/vdb, padded to 4 KiB.
func configDisk(cfg *config.Config, rec store.VMRecord, pubKey string) []byte {
	ones, _ := cfg.Subnet().Mask.Size()
	txt := fmt.Sprintf(
		"MICROVMS_IP=%s\nMICROVMS_PREFIX=%d\nMICROVMS_GW=%s\nMICROVMS_DNS=1.1.1.1,8.8.8.8\nMICROVMS_HOSTNAME=%s\nMICROVMS_SSH_KEY=%s\n",
		rec.IP, ones, cfg.GatewayIP(), rec.Name, pubKey)
	b := make([]byte, 4096)
	copy(b, txt)
	for i := len(txt); i < len(b); i++ {
		b[i] = '\n'
	}
	return b
}

// releaseNetwork removes a VM's tap and DNAT rules (idempotent).
func (m *Manager) releaseNetwork(rec store.VMRecord) {
	m.net.RemovePortDNAT(rec.SSHPort, rec.IP, rec.ID)
	m.net.DeleteTap(netplan.TapName(rec.ID))
}

// boot starts the firecracker process for a provisioned record and registers
// a watcher that cleans up when the VM exits for any reason.
func (m *Manager) boot(rec store.VMRecord) error {
	m.mu.Lock()
	if _, exists := m.running[rec.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("vm %s is already running", rec.ID)
	}
	m.mu.Unlock()

	dir := m.vmdir(rec.ID)
	socket := filepath.Join(dir, "fc.sock")
	_ = os.Remove(socket)

	tap := netplan.TapName(rec.ID)
	if err := m.net.AddTap(tap); err != nil {
		return fmt.Errorf("create tap: %w", err)
	}
	if err := m.net.PortDNAT(rec.SSHPort, rec.IP, rec.ID); err != nil {
		m.net.DeleteTap(tap)
		return fmt.Errorf("port forward: %w", err)
	}

	fcCfg := firecracker.Config{
		SocketPath:      socket,
		KernelImagePath: m.cfg.KernelPath,
		// No reboot=k: it pins the reset method to the keyboard controller,
		// which firecracker's i8042 model ignores, so guest reboots would
		// hang instead of ending the firecracker process.
		KernelArgs: "console=ttyS0 panic=1 pci=off random.trust_cpu=on root=/dev/vda rw",
		LogPath:         filepath.Join(dir, "fc.log"),
		LogLevel:        "Info",
		Drives: []models.Drive{
			{
				DriveID:      firecracker.String("root"),
				PathOnHost:   firecracker.String(filepath.Join(dir, "rootfs.ext4")),
				IsReadOnly:   firecracker.Bool(false),
				IsRootDevice: firecracker.Bool(true),
			},
			{
				DriveID:      firecracker.String("config"),
				PathOnHost:   firecracker.String(filepath.Join(dir, "config.img")),
				IsReadOnly:   firecracker.Bool(true),
				IsRootDevice: firecracker.Bool(false),
			},
		},
		NetworkInterfaces: firecracker.NetworkInterfaces{
			{
				StaticConfiguration: &firecracker.StaticNetworkConfiguration{
					MacAddress: rec.MAC,
					HostDevName: tap,
				},
			},
		},
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(int64(rec.VCPUs)),
			MemSizeMib: firecracker.Int64(int64(rec.MemMib)),
			Smt:        firecracker.Bool(true),
		},
		VMID: rec.ID,
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Capture the guest serial console to a per-VM file; it is the single
	// most useful artifact when a VM misbehaves.
	consoleLog, _ := os.OpenFile(filepath.Join(dir, "console.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	builder := firecracker.VMCommandBuilder{}.
		WithBin(m.cfg.FirecrackerBin).
		WithSocketPath(socket)
	if consoleLog != nil {
		defer consoleLog.Close()
		builder = builder.WithStdout(consoleLog).WithStderr(consoleLog)
	}
	cmd := builder.Build(ctx)

	machine, err := firecracker.NewMachine(ctx, fcCfg, firecracker.WithProcessRunner(cmd))
	if err != nil {
		cancel()
		m.releaseNetwork(rec)
		return fmt.Errorf("machine config: %w", err)
	}
	if err := machine.Start(ctx); err != nil {
		cancel()
		m.releaseNetwork(rec)
		return fmt.Errorf("start firecracker: %w", err)
	}

	mv := &managedVM{
		id:      rec.ID,
		machine: machine,
		cancel:  cancel,
		exited:  make(chan struct{}),
	}
	mv.cleanup = func() {
		mv.netOnce.Do(func() {
			m.releaseNetwork(rec)
			close(mv.exited)
		})
	}

	m.mu.Lock()
	m.running[rec.ID] = mv
	m.mu.Unlock()

	if err := m.store.UpdateState(rec.ID, store.StateRunning); err != nil {
		return err
	}

	// Watcher: when the firecracker process exits (guest poweroff, crash,
	// kill), clean up host networking and mark the record stopped.
	go func() {
		_ = machine.Wait(ctx)
		mv.cleanup()
		m.mu.Lock()
		delete(m.running, rec.ID)
		m.mu.Unlock()
		_ = m.store.UpdateState(rec.ID, store.StateStopped)
	}()
	return nil
}

// Stop gracefully shuts a VM down but keeps its disk so it can be started
// again.
//
// Firecracker's ACPI tables only advertise S0, so a guest `poweroff` can only
// halt the kernel — it never ends the firecracker process. A guest `reboot`,
// by contrast, runs the full openrc shutdown (services stop, filesystems
// remount read-only) and then resets the CPU, which firecracker treats as
// end-of-VM and exits. So the stop ladder is:
//
//  1. SSH `reboot` in the guest (clean; needs the daemon-held guest key)
//  2. CtrlAltDel (for guest kernels with a PS/2 driver; ours has none)
//  3. terminate the firecracker process
func (m *Manager) Stop(id string) error {
	rec, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	m.mu.Lock()
	mv, live := m.running[id]
	m.mu.Unlock()
	if !live {
		if rec.State == store.StateRunning || rec.State == store.StateCreating {
			// Stale record: no live process, just clean up leftovers.
			m.releaseNetwork(rec)
			return m.store.UpdateState(id, store.StateStopped)
		}
		if rec.State == store.StateStopped {
			return nil
		}
	}

	if live {
		grace := time.Duration(m.cfg.GracefulTimeoutSec) * time.Second
		if keyPath := filepath.Join(m.vmdir(id), "guest.key"); fileExists(keyPath) {
			log.Printf("vm %s: stop via ssh reboot", id)
			t0 := time.Now()
			sshGuestReboot(keyPath, rec.IP)
			log.Printf("vm %s: ssh reboot command finished in %s", id, time.Since(t0))
			if mv.waitForExit(grace) {
				<-mv.exited
				return m.store.UpdateState(id, store.StateStopped)
			}
			log.Printf("vm %s: guest did not exit within %s after ssh reboot", id, grace)
		}
		cadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = mv.machine.Shutdown(cadCtx) // CtrlAltDel
		cancel()
		if !mv.waitForExit(3 * time.Second) {
			log.Printf("vm %s: terminating firecracker process", id)
			_ = mv.machine.StopVMM() // terminate the firecracker process
			if !mv.waitForExit(5 * time.Second) {
				mv.cleanup()
			}
		}
		<-mv.exited
	}
	return m.store.UpdateState(id, store.StateStopped)
}

// waitForExit reports whether the firecracker process exited within d.
func (mv *managedVM) waitForExit(d time.Duration) bool {
	select {
	case <-mv.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// sshGuestReboot asks the guest to shut down cleanly and reboot; the reboot
// reset ends the firecracker process (see Stop). Failure is fine: the caller
// falls back to more forceful methods.
func sshGuestReboot(keyPath, vmIP string) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "ssh",
		"-i", keyPath,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
		"root@"+vmIP,
		"reboot",
	).Run()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Start boots a previously stopped VM with its existing disk.
func (m *Manager) Start(id string) error {
	rec, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if rec.State == store.StateRunning {
		return fmt.Errorf("vm %s is already running", id)
	}
	if _, err := os.Stat(filepath.Join(m.vmdir(id), "rootfs.ext4")); err != nil {
		return fmt.Errorf("vm disk missing; recreate the VM: %v", err)
	}
	c := m.Capacity()
	if c.RunningVMs >= m.cfg.MaxVMs {
		return fmt.Errorf("max_vms (%d) reached", m.cfg.MaxVMs)
	}
	if rec.MemMib+128 > c.FreeHostMemMib {
		return fmt.Errorf("insufficient host memory: %d MiB free", c.FreeHostMemMib)
	}
	if err := m.boot(rec); err != nil {
		return err
	}
	return nil
}

// Delete stops the VM (if running) and removes its disk and record.
func (m *Manager) Delete(id string) error {
	if _, ok := m.store.Get(id); !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err := m.Stop(id); err != nil {
		return err
	}
	if err := os.RemoveAll(m.vmdir(id)); err != nil {
		return fmt.Errorf("remove vm dir: %w", err)
	}
	return m.store.Delete(id)
}

// StopAll is called on daemon shutdown: graceful-stop every running VM.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = m.Stop(id)
		}(id)
	}
	wg.Wait()
}

// reconcile cleans up after an unclean daemon exit: firecracker processes
// still holding our sockets are killed, network leftovers removed, and all
// records marked stopped.
func (m *Manager) reconcile() {
	killOrphanFirecrackers(m.cfg.DataDir)
	for _, rec := range m.store.All() {
		if rec.State == store.StateCreating || rec.State == store.StateRunning {
			m.releaseNetwork(rec)
			_ = m.store.UpdateState(rec.ID, store.StateStopped)
		}
	}
}

// killOrphanFirecrackers SIGKILLs any firecracker process whose socket path
// lives under our data dir.
func killOrphanFirecrackers(dataDir string) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	marker := filepath.Join(dataDir, "vms")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		fields := strings.Split(string(raw), "\x00")
		if len(fields) == 0 || !strings.Contains(filepath.Base(fields[0]), "firecracker") {
			continue
		}
		for _, f := range fields {
			if strings.HasPrefix(f, "--api-sock") || strings.Contains(f, marker) {
				for _, f2 := range fields {
					if strings.HasPrefix(f2, marker) {
						_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
						break
					}
				}
				break
			}
		}
	}
}

// generateKeypair returns an authorized_keys line and the matching OpenSSH
// PEM private key.
func generateKeypair() (pub string, privPEM string, err error) {
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return "", "", err
	}
	pub = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	block, err := ssh.MarshalPrivateKey(privKey, "microvms")
	if err != nil {
		return "", "", err
	}
	return pub, string(pem.EncodeToMemory(block)), nil
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func memAvailableMib() int {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kib, _ := strconv.Atoi(fields[1])
				return kib / 1024
			}
		}
	}
	return 0
}
