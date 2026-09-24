# talos-docker-bootstrap

![CI](https://github.com/infrakit-io/talos-docker-bootstrap/actions/workflows/ci.yml/badge.svg)
![Coverage Core](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/infrakit-io/talos-docker-bootstrap/master/docs/coverage/coverage-core.json)
![Coverage All](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/infrakit-io/talos-docker-bootstrap/master/docs/coverage/coverage-all.json)
[![Go Report Card](https://goreportcard.com/badge/github.com/infrakit-io/talos-docker-bootstrap)](https://goreportcard.com/report/github.com/infrakit-io/talos-docker-bootstrap)
![Go Version](https://img.shields.io/github/go-mod/go-version/infrakit-io/talos-docker-bootstrap)
![Release](https://img.shields.io/github/v/release/infrakit-io/talos-docker-bootstrap)

Enterprise-grade post-bootstrap for Ubuntu dev VMs.

## Scope

- Configure an existing Ubuntu VM for dev workflows
- Apply idempotent OS hardening baseline
- Install and verify Docker
- Install and verify talosctl
- Create Talos-in-Docker cluster idempotently (single-node controlplane by default)
- Optional: verify NTP time sync (`time_sync.enabled`)
- Optional: stop after Docker for plain Docker hosts (`cluster.enabled: false`, see `configs/docker-host.example.yaml`)

## Status

Public alpha. First functional release baseline: `v0.1.0`.

## Quick Start

```bash
cp configs/talos-bootstrap.example.yaml configs/talos-bootstrap.yaml
# edit configs/talos-bootstrap.yaml (interactive wizard can auto-fill checksum)

make vm-deploy              # VM bootstrap via vmbootstrap
make talos-bootstrap-dry    # Talos bootstrap dry-run
make talos-bootstrap        # Talos bootstrap apply
```

Note: `talos.sha256_checksum` must match the VM architecture (`amd64` or `arm64`), since the installer downloads the corresponding `talosctl` binary.

## SSH Host Key Verification

By default, the bootstrap runs in **strict** mode and refuses to connect if the SSH host key changes.
You can control this behavior with:

- `vm.known_hosts_mode`: `strict` | `prompt` | `accept-new` | `auto-refresh`
- `vm.ssh_host_fingerprint`: optional `SHA256:...` fingerprint to pin the expected host key

Best practice for automation is to pass the fingerprint produced by `vmbootstrap` bootstrap output, so the host key is verified without interactive prompts.

## CLI

```bash
talos-docker-bootstrap vm-deploy
talos-docker-bootstrap bootstrap --config configs/talos-bootstrap.yaml [--dry-run] [--json]
talos-docker-bootstrap cluster-status --config configs/talos-bootstrap.yaml
talos-docker-bootstrap mount-check --config configs/talos-bootstrap.yaml
talos-docker-bootstrap kubeconfig-export --config configs/talos-bootstrap.yaml --out build/devvm/kubeconfig
talos-docker-bootstrap provision-and-bootstrap --config configs/talos-bootstrap.yaml --bootstrap-result bootstrap-result.yaml [--vm-config configs/vm.example.yaml]
```

## Config Files

- `configs/talos-bootstrap.yaml`: main runtime config for Docker/Talos bootstrap on the target VM.
- `configs/talos-bootstrap.example.yaml`: template for creating `talos-bootstrap.yaml`.
- `configs/vcenter.sops.yaml`: vCenter credentials/defaults used by delegated `vmbootstrap` VM deploy flow.
- `configs/vm.*.sops.yaml`: VM definitions consumed by delegated `vmbootstrap` commands.
- `configs/vm.example.yaml`: template VM config synced from `vmware-vm-bootstrap`.
- `configs/defaults.yaml`: local defaults for this repo's bootstrap behavior.
- `configs/tool-versions.yaml`: pinned tool/version metadata used by automation scripts.

## Quality Gates

- `go test ./...`
- `go vet ./...`
- `golangci-lint`
- `make test-cover` (core logic coverage report for `internal/bootstrap`, `internal/config`, `internal/workflow`)

`Coverage Core` tracks production packages (`internal/bootstrap`, `internal/config`, `internal/workflow`).
`Coverage All` tracks total project coverage (including CLI and integration layers).
