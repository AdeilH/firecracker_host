// Package api exposes the VM manager over a REST + JSON HTTP API.
package api

import (
	"errors"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"microvms/internal/config"
	"microvms/internal/store"
	"microvms/internal/vm"
)

// Server wires HTTP handlers to the VM manager.
type Server struct {
	cfg   *config.Config
	mgr   *vm.Manager
	openapi []byte
}

// New builds the HTTP server.
func New(cfg *config.Config, mgr *vm.Manager, openapiSpec []byte) http.Handler {
	s := &Server{cfg: cfg, mgr: mgr, openapi: openapiSpec}
	mux := http.NewServeMux()

	// Unauthenticated endpoints.
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/openapi", s.handleOpenAPI)

	// Authenticated endpoints.
	auth := s.auth()
	mux.Handle("POST /v1/vms", auth(http.HandlerFunc(s.handleCreate)))
	mux.Handle("GET /v1/vms", auth(http.HandlerFunc(s.handleList)))
	mux.Handle("GET /v1/vms/{id}", auth(http.HandlerFunc(s.handleGet)))
	mux.Handle("POST /v1/vms/{id}/stop", auth(http.HandlerFunc(s.handleStop)))
	mux.Handle("POST /v1/vms/{id}/start", auth(http.HandlerFunc(s.handleStart)))
	mux.Handle("DELETE /v1/vms/{id}", auth(http.HandlerFunc(s.handleDelete)))
	mux.Handle("GET /v1/capacity", auth(http.HandlerFunc(s.handleCapacity)))

	return mux
}

// auth wraps a handler with bearer-token validation.
func (s *Server) auth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				token = strings.TrimPrefix(h, "Bearer ")
			}
			ok := false
			for _, want := range s.cfg.APITokens {
				if subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1 {
					ok = true
					break
				}
			}
			if !ok {
				writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.openapi)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var p vm.CreateParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	rec, privPEM, err := s.mgr.Create(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	resp := s.vmResponse(rec)
	if privPEM != "" {
		resp.SSHPrivateKey = privPEM
		resp.SSHCommand = fmt.Sprintf("ssh -i <key-file> -o StrictHostKeyChecking=no -p %d root@%s", rec.SSHPort, s.cfg.ExternalIP)
	} else {
		resp.SSHCommand = fmt.Sprintf("ssh -o StrictHostKeyChecking=no -p %d root@%s", rec.SSHPort, s.cfg.ExternalIP)
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	recs := s.mgr.AllRecords()
	out := make([]VMResponse, 0, len(recs))
	for _, rec := range recs {
		out = append(out, s.vmResponse(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"vms": out})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.mgr.Record(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "vm not found")
		return
	}
	writeJSON(w, http.StatusOK, s.vmResponse(rec))
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Stop(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	rec, _ := s.mgr.Record(r.PathValue("id"))
	writeJSON(w, http.StatusOK, s.vmResponse(rec))
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Start(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	rec, _ := s.mgr.Record(r.PathValue("id"))
	writeJSON(w, http.StatusOK, s.vmResponse(rec))
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Delete(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeErr maps manager errors to HTTP codes.
func writeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, vm.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func (s *Server) handleCapacity(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Capacity())
}

// VMResponse is the API view of a VM record.
type VMResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	State         string `json:"state"`
	IP            string `json:"ip"`
	SSHPort       int    `json:"ssh_port"`
	SSHUser       string `json:"ssh_user"`
	SSHCommand    string `json:"ssh_command,omitempty"`
	SSHPrivateKey string `json:"ssh_private_key,omitempty"` // only in create response when generated
	VCPUs         int    `json:"vcpus"`
	MemMib        int    `json:"mem_mib"`
	CreatedAt     string `json:"created_at"`
}

func (s *Server) vmResponse(rec store.VMRecord) VMResponse {
	return VMResponse{
		ID:         rec.ID,
		Name:       rec.Name,
		State:      rec.State,
		IP:         rec.IP,
		SSHPort:    rec.SSHPort,
		SSHUser:    "root",
		SSHCommand: fmt.Sprintf("ssh -o StrictHostKeyChecking=no -p %d root@%s", rec.SSHPort, s.cfg.ExternalIP),
		VCPUs:      rec.VCPUs,
		MemMib:     rec.MemMib,
		CreatedAt:  rec.CreatedAt.Format(time.RFC3339),
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
