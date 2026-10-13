# microvms

A small Go daemon (`microvmsd`) that runs on a Firecracker host and exposes a
REST API for creating, stopping, and deleting disposable Linux VMs — designed
for AI agents and automation. Each VM boots in ~1s, self-configures, and is
reachable over SSH through a forwarded port.

```
agents ──HTTP + bearer token──► host:8420 ──► microvmsd ──► firecracker VMs
agents ──SSH host:22XX──────────────────────────────────────► VM (root, key auth)
```

## Deploy

Host needs: Linux x86_64 with `/dev/kvm` (bare metal or nested virt), systemd, ~1GB free RAM.

```bash
ssh-copy-id you@192.168.0.118                    # one time, prompt-free deploys
make preflight USER=you HOST=192.168.0.118       # verifies KVM etc. (no sudo)
make deploy USER=you HOST=192.168.0.118          # sudo password prompted once
```

Deploy generates an API token (printed, and stored at `/etc/microvms/config.toml`
on the host — a list, so you can mint one per agent and revoke independently).
Code changes after that: `make redeploy`.

## API

```bash
BASE=http://192.168.0.118:8420
TOKEN=<token>                      # every /v1/vms* call needs:
                                    #   Authorization: Bearer $TOKEN

# create a VM (omit ssh_key and the daemon generates a keypair, returning
# the private key once; or pass your own public key)
curl -X POST -H "Authorization: Bearer $TOKEN" $BASE/v1/vms \
  -d '{"name":"sandbox","cpus":2,"mem_mib":512}'

curl -H "Authorization: Bearer $TOKEN" $BASE/v1/vms            # list
curl -H "Authorization: Bearer $TOKEN" $BASE/v1/vms/<id>       # inspect
curl -X POST   -H "Authorization: Bearer $TOKEN" $BASE/v1/vms/<id>/stop   # keep disk
curl -X POST   -H "Authorization: Bearer $TOKEN" $BASE/v1/vms/<id>/start  # boot again
curl -X DELETE -H "Authorization: Bearer $TOKEN" $BASE/v1/vms/<id>        # free everything
```

Create response (trimmed):

```json
{"id": "9f2c7d1a", "state": "running", "ip": "172.16.100.10",
 "ssh_port": 2200, "ssh_user": "root",
 "ssh_command": "ssh -i <key-file> -o StrictHostKeyChecking=no -p 2200 root@192.168.0.118",
 "ssh_private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\n...",
 "vcpus": 2, "mem_mib": 512}
```

SSH in with the returned key (save to a file, `chmod 600`). Guests are Alpine
with bash, curl, git, python3, and outbound internet. Limits and headroom:
`GET /v1/capacity`; full spec: `GET /v1/openapi` (no auth).

### Agent prompt snippet

```
You manage VMs via the microvms REST API at http://192.168.0.118:8420.
Send "Authorization: Bearer $TOKEN" on every call. POST /v1/vms creates a VM
and returns an ssh_private_key (when you didn't pass ssh_key) plus the
ssh_command to use it. Poll GET /v1/vms/{id} for state. DELETE the VM when
done. Full spec: GET /v1/openapi.
```

## Behavior notes

- **Stop is clean but specific**: Firecracker's ACPI only advertises S0, so a
  guest `poweroff` can't end the VM. The daemon SSHes in and runs `reboot` —
  openrc shuts down cleanly (disks remount read-only) and the CPU reset ends
  firecracker. This needs the daemon-held key, which exists only when you did
  NOT pass `ssh_key` at create; with your own key, stop falls back to
  terminating the process (data in flight may be lost).
- **Daemon restarts stop all VMs.** Records survive; `start` re-boots them.
- Each VM's serial console is captured to `<data_dir>/vms/<id>/console.log` —
  first place to look when a VM misbehaves.
- Tokens grant full fleet control; port 8420 is open to the LAN (no TLS in
  v1) — firewall it if the LAN isn't fully trusted.
- IPs and SSH ports come from fixed pools (172.16.100.10–.254, ports 2200+).

## Development

```bash
make build && make assets          # binary + kernel + rootfs (Docker)
go test ./...
sudo ./bin/microvmsd -config config.dev.toml   # full local daemon (root)
./bin/microvmsd -config config.dev.toml        # API-only mode (see dev_unprivileged)
```
