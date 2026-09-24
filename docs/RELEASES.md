# Release Notes

## Unreleased

Highlights:
- `cluster.enabled` (default `true` when omitted): set `false` to stop after SSH check, OS hardening,
  time sync and Docker. `talos.*` and the cluster name/state/mount fields are then not required, the
  `talosctl_install` and `cluster_create` steps are recorded as `skipped`, and `cluster-status`,
  `kubeconfig-export` and `mount-check` refuse with a clear message instead of querying a cluster that
  does not exist. This makes the tool usable for plain Docker hosts (e.g. single-VM Compose deployments).
- New `time_sync` step (opt-in, `time_sync.enabled: true`): keeps the active daemon (chrony if running,
  otherwise systemd-timesyncd, installed if missing), optionally sets `time_sync.servers`, then waits up
  to `time_sync.wait_seconds` (default 120) for `timedatectl NTPSynchronized=yes` and fails otherwise.

Notes:
- Existing configs behave exactly as before: an omitted `cluster.enabled` means enabled and `time_sync`
  defaults to disabled (its step is recorded as `skipped`). The result JSON gains one `time_sync` step entry.
- `time_sync.servers` entries are validated as hostnames/IP literals because they are written into a remote script.

## v0.2.1 (2026-03-01)

Highlights:
- Bumped `vmware-vm-bootstrap` dependency from `v0.2.2` to `v0.2.3`.

Notes:
- Pulls VM config editor UX improvement that offers filename rename when `vm.name` changes.

## v0.2.0 (2026-03-01)

Highlights:
- Project renamed from `talos-vm-bootstrap` to `talos-docker-bootstrap` for semantic clarity.
- Go module path and internal imports updated to `github.com/infrakit-io/talos-docker-bootstrap`.
- CLI binary/command name updated to `talos-docker-bootstrap`.

Notes:
- This is a breaking rename for import paths and command invocations.
- GitHub repository has been renamed to `infrakit-io/talos-docker-bootstrap`.

## v0.1.3 (2026-03-01)

Highlights:
- Bumped `cli-wizard-core` dependency from `v0.1.1` to `v0.1.2`.

Notes:
- Pulls badge/docs patch release from shared wizard core.

## v0.1.2 (2026-03-01)

Highlights:
- Bumped `cli-wizard-core` dependency from `v0.1.0` to `v0.1.1`.

Notes:
- Pulls latest shared wizard core release used by config draft/session flows.

## v0.1.1 (2026-03-01)

Highlights:
- Config manager draft lifecycle now uses `cli-wizard-core` session primitives for consistent behavior across repositories.
- Stage2 draft handling was simplified and aligned with shared wizard semantics.

Notes:
- Replaces custom ad-hoc draft flow in `internal/cli/config_manager.go` with `wizard.Session` integration.
- Improves reuse and keeps future wizard behavior changes centralized in `cli-wizard-core`.

## v0.1.0 (2026-02-26)

Highlights:

- Enterprise-grade CLI scaffold with `bootstrap`, `vm-deploy`, and `provision-and-bootstrap`.
- Idempotent Talos bootstrap flow: OS hardening, pinned Docker install, pinned talosctl install, single-node Talos-in-Docker create.
- Integration via `vmware-vm-bootstrap` module pin (config manager entrypoint + VM deploy + workflow integration).
- SSH host trust hardening: strict/prompt/accept-new/auto-refresh modes, fingerprint pinning, stability refresh for fresh VM workflows.
- Workflow UX polish: human progress steps, long-step heartbeat, better error messages and recovery hints.
- Release pipeline hardening: GitHub release publish with retry/backoff, deterministic assets sync from pinned `vmbootstrap`.

Notes:

- Talos-in-Docker create enforces single-node topology (`--workers 0`) by default.
- `kubeconfig-export` can regenerate remote kubeconfig when cluster state exists.
- Core coverage is tracked with `make test-cover`; current values should be read from the generated report.
