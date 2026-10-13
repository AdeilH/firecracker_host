package netplan

import (
	"fmt"
	"testing"
)

func TestAllocateIP(t *testing.T) {
	used := map[string]bool{"172.16.100.10": true, "172.16.100.11": true}
	got, err := AllocateIP("172.16.100.10", "172.16.100.12", used)
	if err != nil || got != "172.16.100.12" {
		t.Fatalf("expected .12, got %q err %v", got, err)
	}

	// exhausted range
	if _, err := AllocateIP("172.16.100.10", "172.16.100.11", used); err == nil {
		t.Fatal("expected exhaustion error")
	}

	// range crossing an octet boundary
	free := map[string]bool{}
	got, err = AllocateIP("172.16.0.254", "172.16.1.2", free)
	if err != nil || got != "172.16.0.254" {
		t.Fatalf("expected 172.16.0.254, got %q err %v", got, err)
	}
	free[got] = true
	free["172.16.0.255"] = true
	free["172.16.1.0"] = true
	got, err = AllocateIP("172.16.0.254", "172.16.1.2", free)
	if err != nil || got != "172.16.1.1" {
		t.Fatalf("expected 172.16.1.1, got %q err %v", got, err)
	}
}

func TestAllocatePort(t *testing.T) {
	used := map[int]bool{2200: true, 2201: true}
	if p := AllocatePort(2200, used); p != 2202 {
		t.Fatalf("expected 2202, got %d", p)
	}
}

func TestMACFor(t *testing.T) {
	m1 := MACFor("abc123")
	m2 := MACFor("abc123")
	m3 := MACFor("xyz789")
	if m1 != m2 {
		t.Fatal("MAC not deterministic for same id")
	}
	if m1 == m3 {
		t.Fatal("different ids produced same MAC")
	}
	// locally administered unicast prefix
	if m1[:5] != "02:fc" {
		t.Fatalf("unexpected MAC prefix in %q", m1)
	}
	if len(m1) != 17 {
		t.Fatalf("bad MAC length: %q", m1)
	}
}

func TestTapName(t *testing.T) {
	n := TapName("0123456789abcdef")
	if len(n) > 15 { // Linux interface name limit
		t.Fatalf("tap name too long: %q", n)
	}
	if n != "t-0123456789a" {
		t.Fatalf("unexpected tap name %q", n)
	}
}

func ExampleAllocateIP() {
	ip, _ := AllocateIP("10.0.0.10", "10.0.0.20", map[string]bool{"10.0.0.10": true})
	fmt.Println(ip)
	// Output: 10.0.0.11
}
