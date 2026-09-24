package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/infrakit-io/talos-docker-bootstrap/internal/config"
	"github.com/infrakit-io/talos-docker-bootstrap/internal/ssh"
)

// ErrClusterDisabled is returned by the cluster operations when the config
// sets cluster.enabled: false, so no Talos state exists to query.
var ErrClusterDisabled = errors.New("talos cluster is disabled in config (cluster.enabled: false)")

func runClusterCreate(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

TARGET_USER=%q
CLUSTER_NAME=%q
STATE_DIR=%q
MOUNT_SRC=%q
MOUNT_DST=%q
TALOS_HOME="/home/${TARGET_USER}/.talos"
TALOSCONFIG="${STATE_DIR}/talosconfig"
KUBECONFIG="${STATE_DIR}/kubeconfig"

if [ ! -d "${MOUNT_SRC}" ]; then
  if ! sudo -n -u "${TARGET_USER}" -H env MOUNT_SRC="${MOUNT_SRC}" bash -lc 'set -euo pipefail; install -d -m 0755 "${MOUNT_SRC}"'; then
    echo "Mount source not found and could not be created: ${MOUNT_SRC}" >&2
    exit 1
  fi
  echo "Mount source created: ${MOUNT_SRC}"
fi

show="$(sudo -n -u "${TARGET_USER}" -H env CLUSTER_NAME="${CLUSTER_NAME}" STATE_DIR="${STATE_DIR}" bash -lc 'set -euo pipefail; talosctl cluster --name "${CLUSTER_NAME}" --state "${STATE_DIR}" show --provisioner docker 2>/dev/null || true')"
if printf "%%s\n" "${show}" | grep -Eiq 'controlplane|worker'; then
  worker_count="$(printf "%%s\n" "${show}" | awk 'tolower($2) == "worker" {c++} END {print c+0}')"
  if [ "${worker_count}" -gt 0 ]; then
    echo "Cluster has worker nodes (${worker_count}); recreating as single-node controlplane: ${CLUSTER_NAME}"
    sudo -n -u "${TARGET_USER}" -H env CLUSTER_NAME="${CLUSTER_NAME}" STATE_DIR="${STATE_DIR}" bash -lc 'set -euo pipefail; timeout 60s talosctl cluster --name "${CLUSTER_NAME}" --state "${STATE_DIR}" destroy --force || true'
  elif [ -s "${TALOSCONFIG}" ] && [ -s "${KUBECONFIG}" ]; then
    echo "Cluster already running: ${CLUSTER_NAME} (single-node, artifacts present, skipping create)"
    exit 0
  else
    echo "Cluster running but Talos artifacts missing; recreating to self-heal state: ${CLUSTER_NAME}"
    sudo -n -u "${TARGET_USER}" -H env CLUSTER_NAME="${CLUSTER_NAME}" STATE_DIR="${STATE_DIR}" bash -lc 'set -euo pipefail; timeout 60s talosctl cluster --name "${CLUSTER_NAME}" --state "${STATE_DIR}" destroy --force || true'
  fi
fi

STATE_PARENT="$(dirname "${STATE_DIR}")"
mkdir -p "${STATE_PARENT}" "${STATE_DIR}"
chown -R "${TARGET_USER}:${TARGET_USER}" "${STATE_PARENT}" "${STATE_DIR}" 2>/dev/null || true
mkdir -p "${TALOS_HOME}"
chown -R "${TARGET_USER}:${TARGET_USER}" "${TALOS_HOME}" 2>/dev/null || true
if ! sudo -n -u "${TARGET_USER}" -H env STATE_DIR="${STATE_DIR}" bash -lc 'set -euo pipefail; test -d "${STATE_DIR}" && test -w "${STATE_DIR}"'; then
  echo "Unable to prepare writable state directory for ${TARGET_USER}: ${STATE_DIR}" >&2
  exit 1
fi

sudo -n -u "${TARGET_USER}" -H env \
  CLUSTER_NAME="${CLUSTER_NAME}" \
  STATE_DIR="${STATE_DIR}" \
  MOUNT_SRC="${MOUNT_SRC}" \
  MOUNT_DST="${MOUNT_DST}" \
  TALOSCONFIG="${TALOSCONFIG}" \
  bash -lc 'set -euo pipefail
    if ! timeout 600s talosctl cluster --name "${CLUSTER_NAME}" --state "${STATE_DIR}" create docker --workers 0 --talosconfig-destination "${TALOSCONFIG}" --mount "type=bind,src=${MOUNT_SRC},dst=${MOUNT_DST}"; then
      rc=$?
      show_after="$(talosctl cluster --name "${CLUSTER_NAME}" --state "${STATE_DIR}" show --provisioner docker 2>/dev/null || true)"
      if printf "%%s\n" "${show_after}" | grep -Eiq "controlplane|worker"; then
        echo "Cluster create reported rc=${rc}, but cluster appears running; continuing."
      else
        exit ${rc}
      fi
    fi
  '

if [ ! -s "${TALOSCONFIG}" ]; then
  echo "Missing talosconfig after cluster create: ${TALOSCONFIG}" >&2
  exit 1
fi

node_ip="$(sudo -n -u "${TARGET_USER}" -H env CLUSTER_NAME="${CLUSTER_NAME}" STATE_DIR="${STATE_DIR}" bash -lc 'set -euo pipefail; talosctl cluster --name "${CLUSTER_NAME}" --state "${STATE_DIR}" show --provisioner docker | awk '"'"'tolower($2) ~ /controlplane|worker/ {print $3; exit}'"'"'')"
if [ -z "${node_ip}" ]; then
  echo "Failed to detect Talos node IP from cluster state." >&2
  exit 1
fi

for i in $(seq 1 20); do
  if sudo -n -u "${TARGET_USER}" -H env TALOSCONFIG="${TALOSCONFIG}" KUBECONFIG="${KUBECONFIG}" NODE_IP="${node_ip}" bash -lc 'set -euo pipefail; timeout 5s talosctl --talosconfig "${TALOSCONFIG}" --nodes "${NODE_IP}" --endpoints "${NODE_IP}" kubeconfig "${KUBECONFIG}" --merge=false --force >/dev/null 2>&1'; then
    break
  fi
  sleep 1
done
if [ ! -s "${KUBECONFIG}" ]; then
  echo "Failed to generate kubeconfig at ${KUBECONFIG}." >&2
  exit 1
fi
`, cfg.VM.User, cfg.Cluster.Name, cfg.Cluster.StateDir, cfg.Cluster.MountSrc, cfg.Cluster.MountDst)

	return runRemoteScript(ctx, logger, cfg, "cluster_create", script)
}

func ClusterStatus(ctx context.Context, logger *slog.Logger, cfg config.Config) (string, error) {
	if !cfg.Cluster.IsEnabled() {
		return "", ErrClusterDisabled
	}
	sshCfg := execConfig(cfg)
	cmd := fmt.Sprintf("sudo -n -u %q -H env STATE_DIR=%q CLUSTER_NAME=%q bash -lc 'set -euo pipefail; out=\"$(talosctl cluster --name \"${CLUSTER_NAME}\" --state \"${STATE_DIR}\" show --provisioner docker 2>/dev/null || true)\"; if [ -z \"$(printf \"%%s\" \"$out\" | tr -d \"[:space:]\")\" ]; then echo \"No Talos-in-Docker cluster found on remote VM.\" >&2; exit 2; fi; printf \"%%s\\n\" \"$out\"'", cfg.VM.User, cfg.Cluster.StateDir, cfg.Cluster.Name)
	stdout, stderr, err := sshRunCommandFn(ctx, sshCfg, cmd)
	if stderr != "" {
		logger.Debug("cluster_status stderr", "output", strings.TrimSpace(stderr))
	}
	if err != nil {
		return strings.TrimSpace(stdout), err
	}
	return strings.TrimSpace(stdout), nil
}

func KubeconfigExport(ctx context.Context, logger *slog.Logger, cfg config.Config) (string, error) {
	if !cfg.Cluster.IsEnabled() {
		return "", ErrClusterDisabled
	}
	sshCfg := execConfig(cfg)
	remotePath := filepath.Join(cfg.Cluster.StateDir, "kubeconfig")
	talosConfigPath := filepath.Join(cfg.Cluster.StateDir, "talosconfig")
	cmd := fmt.Sprintf(`sudo -n -u %q -H env STATE_DIR=%q CLUSTER_NAME=%q REMOTE_KUBECONFIG=%q TALOSCONFIG=%q bash -lc '
set -euo pipefail
if [ ! -s "${REMOTE_KUBECONFIG}" ]; then
  if [ ! -s "${TALOSCONFIG}" ]; then
    echo "Remote Talos config missing: ${TALOSCONFIG} (run make talos-bootstrap to self-heal)." >&2
    exit 2
  fi
  node_ip="$(talosctl cluster --name "${CLUSTER_NAME}" --state "${STATE_DIR}" show --provisioner docker | awk '"'"'tolower($2) ~ /controlplane|worker/ {print $3; exit}'"'"')"
  if [ -z "${node_ip}" ]; then
    echo "No remote Talos cluster found on VM." >&2
    exit 2
  fi
  timeout 5s talosctl --talosconfig "${TALOSCONFIG}" --nodes "${node_ip}" --endpoints "${node_ip}" kubeconfig "${REMOTE_KUBECONFIG}" --merge=false --force >/dev/null
fi
cat "${REMOTE_KUBECONFIG}"
'`, cfg.VM.User, cfg.Cluster.StateDir, cfg.Cluster.Name, remotePath, talosConfigPath)
	stdout, stderr, err := sshRunCommandFn(ctx, sshCfg, cmd)
	if stderr != "" {
		logger.Debug("kubeconfig_export stderr", "output", strings.TrimSpace(stderr))
	}
	if err != nil {
		return "", err
	}
	return stdout, nil
}

func MountCheck(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	if !cfg.Cluster.IsEnabled() {
		return ErrClusterDisabled
	}
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

TARGET_USER=%q
MOUNT_DST=%q
STATE_DIR=%q
CLUSTER_NAME=%q

sudo -n -u "${TARGET_USER}" -H env MOUNT_DST="${MOUNT_DST}" STATE_DIR="${STATE_DIR}" CLUSTER_NAME="${CLUSTER_NAME}" bash -lc '
  set -euo pipefail
  show="$(talosctl cluster --name "${CLUSTER_NAME}" --state "${STATE_DIR}" show --provisioner docker 2>/dev/null || true)"
  node_name="$(printf "%%s\n" "${show}" | awk "tolower(\$2) ~ /controlplane|worker/ {print \$1; exit}")"
  if [ -z "${node_name}" ]; then
    echo "No Talos-in-Docker cluster found on remote VM." >&2
    exit 2
  fi
  mount_src="$(docker inspect "${node_name}" --format "{{range .Mounts}}{{if eq .Destination \"${MOUNT_DST}\"}}{{.Source}}{{end}}{{end}}")"
  if [ -z "${mount_src}" ]; then
    echo "Mount path not configured on node ${node_name}: ${MOUNT_DST}" >&2
    exit 1
  fi
  echo "Mount path mapped on node: ${MOUNT_DST} <- ${mount_src}"
'
`, cfg.VM.User, cfg.Cluster.MountDst, cfg.Cluster.StateDir, cfg.Cluster.Name)

	return runRemoteScript(ctx, logger, cfg, "mount_check", script)
}

func execConfig(cfg config.Config) ssh.ExecConfig {
	return ssh.ExecConfig{
		Host:                  cfg.VM.Host,
		Port:                  cfg.VM.Port,
		User:                  cfg.VM.User,
		PrivateKeyPath:        cfg.VM.SSHPrivateKey,
		KnownHostsFile:        cfg.VM.KnownHostsFile,
		KnownHostsMode:        cfg.VM.KnownHostsMode,
		ExpectedHostKeySHA256: cfg.VM.SSHHostFingerprint,
		Prompt:                knownHostsPromptFn,
		ConnectTimeout:        cfg.Timeouts.SSHConnectDuration(),
	}
}
