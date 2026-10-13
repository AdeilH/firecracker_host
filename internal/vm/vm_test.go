package vm

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"microvms/internal/config"
	"microvms/internal/store"
)

func TestGenerateKeypairProducesValidOpenSSHKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	pub, priv, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) == 0 || len(priv) == 0 {
		t.Fatal("empty key material")
	}

	// round-trip: the PEM private key must derive the same public key
	keyFile := t.TempDir() + "/id_test"
	if err := os.WriteFile(keyFile, []byte(priv), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ssh-keygen", "-y", "-f", keyFile).Output()
	if err != nil {
		t.Fatalf("ssh-keygen rejected generated key: %v: %s", err, out)
	}
	// compare key type + material; the comment field may differ
	derived := strings.Fields(string(out))
	generated := strings.Fields(pub)
	if len(derived) < 2 || len(generated) < 2 || derived[0] != generated[0] || derived[1] != generated[1] {
		t.Fatalf("derived public key mismatch:\n got %q\nwant %q", out, pub)
	}
}

func TestConfigDiskFormat(t *testing.T) {
	cfg := config.Default()
	rec := store.VMRecord{ID: "ab12", Name: "k8s-build", IP: "172.16.100.42"}
	disk := configDisk(&cfg, rec, "ssh-ed25519 AAAA test")

	if len(disk) != 4096 {
		t.Fatalf("config disk size = %d", len(disk))
	}
	txt := string(disk)
	for _, want := range []string{
		"MICROVMS_IP=172.16.100.42",
		"MICROVMS_PREFIX=24",
		"MICROVMS_GW=172.16.100.1",
		"MICROVMS_HOSTNAME=k8s-build",
		"MICROVMS_SSH_KEY=ssh-ed25519 AAAA test",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("config disk missing %q", want)
		}
	}
}
