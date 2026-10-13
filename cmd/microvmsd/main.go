// microvmsd is the microVM control daemon: it wraps Firecracker and exposes
// a REST API for creating, stopping and inspecting microVMs.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"microvms/internal/api"
	"microvms/internal/config"
	"microvms/internal/netplan"
	"microvms/internal/store"
	"microvms/internal/vm"
)

func main() {
	configPath := flag.String("config", "/etc/microvms/config.toml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.ExternalIP == "" {
		cfg.ExternalIP = config.DetectExternalIP()
		log.Printf("external_ip not set; detected %s", cfg.ExternalIP)
	}
	cfgPtr := &cfg

	if os.Geteuid() != 0 && !cfg.DevUnprivileged {
		log.Fatalf("microvmsd must run as root (needs KVM, taps and iptables)")
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	np, err := netplan.New(cfg.Bridge, cfg.HostCIDR, cfg.ExternalIP)
	if err != nil {
		log.Fatalf("netplan: %v", err)
	}
	mgr, err := vm.NewManager(cfgPtr, st, np)
	if err != nil {
		log.Fatalf("vm manager: %v", err)
	}

	openapiSpec, err := os.ReadFile("/usr/local/share/microvms/openapi.yaml")
	if err != nil {
		openapiSpec = []byte("# OpenAPI spec not installed; see docs/api.yaml in the repo\n")
	}

	handler := api.New(cfgPtr, mgr, openapiSpec)
	srv := &http.Server{Addr: cfg.Listen, Handler: handler}

	go func() {
		log.Printf("microvmsd listening on %s (external ip %s, bridge %s)", cfg.Listen, cfg.ExternalIP, cfg.Bridge)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down: stopping all VMs")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	mgr.StopAll()
	log.Printf("bye")
}
