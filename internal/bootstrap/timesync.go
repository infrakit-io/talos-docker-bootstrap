package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/infrakit-io/talos-docker-bootstrap/internal/config"
)

// runTimeSync makes sure the VM keeps its clock synchronized and proves it.
//
// It never replaces a running chrony with systemd-timesyncd (or the reverse):
// whichever daemon is active is kept and, when time_sync.servers is set, told
// to use those servers. When neither is active, systemd-timesyncd (Ubuntu's
// default) is installed if missing and enabled. The step then waits until the
// kernel reports the clock as synchronized (timedatectl NTPSynchronized=yes,
// which reflects the kernel state for both daemons) and fails otherwise:
// a host whose clock is not synchronized is not considered bootstrapped.
func runTimeSync(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	if !cfg.TimeSync.Enabled {
		logger.Info("time_sync disabled by config")
		return nil
	}
	return runRemoteScript(ctx, logger, cfg, "time_sync", timeSyncScript(cfg.TimeSync))
}

func timeSyncScript(ts config.TimeSyncConfig) string {
	// Servers are validated against a hostname/IP pattern in config.Validate,
	// so joining them into the script cannot inject shell syntax.
	servers := strings.Join(ts.Servers, " ")
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

NTP_SERVERS="%s"
WAIT_SECONDS=%d

if systemctl is-active --quiet chrony 2>/dev/null || systemctl is-active --quiet chronyd 2>/dev/null; then
  DAEMON=chrony
  if [ -n "${NTP_SERVERS}" ]; then
    install -d -m 0755 /etc/chrony/sources.d
    SRC=/etc/chrony/sources.d/99-talos-docker-bootstrap.sources
    TMP_SRC="$(mktemp)"
    for S in ${NTP_SERVERS}; do printf 'server %%s iburst\n' "${S}" >> "${TMP_SRC}"; done
    if [ ! -f "${SRC}" ] || ! cmp -s "${TMP_SRC}" "${SRC}"; then
      install -m 0644 "${TMP_SRC}" "${SRC}"
      chronyc reload sources >/dev/null
    fi
    rm -f "${TMP_SRC}"
  fi
else
  DAEMON=systemd-timesyncd
  if ! dpkg -s systemd-timesyncd >/dev/null 2>&1; then
    apt-get update -y
    apt-get install -y --no-install-recommends systemd-timesyncd
  fi
  if [ -n "${NTP_SERVERS}" ]; then
    install -d -m 0755 /etc/systemd/timesyncd.conf.d
    CONF=/etc/systemd/timesyncd.conf.d/99-talos-docker-bootstrap.conf
    TMP_CONF="$(mktemp)"
    printf '[Time]\nNTP=%%s\n' "${NTP_SERVERS}" > "${TMP_CONF}"
    if [ ! -f "${CONF}" ] || ! cmp -s "${TMP_CONF}" "${CONF}"; then
      install -m 0644 "${TMP_CONF}" "${CONF}"
      systemctl restart systemd-timesyncd
    fi
    rm -f "${TMP_CONF}"
  fi
  timedatectl set-ntp true
  systemctl enable --now systemd-timesyncd >/dev/null
fi

ELAPSED=0
while [ "$(timedatectl show -p NTPSynchronized --value 2>/dev/null || echo no)" != "yes" ]; do
  if [ "${ELAPSED}" -ge "${WAIT_SECONDS}" ]; then
    echo "Clock not synchronized after ${WAIT_SECONDS}s (daemon: ${DAEMON}). Check NTP reachability (UDP 123) and 'timedatectl timesync-status' / 'chronyc tracking'." >&2
    exit 1
  fi
  sleep 5
  ELAPSED=$((ELAPSED + 5))
done
echo "Clock synchronized (daemon: ${DAEMON})."
`, servers, ts.EffectiveWaitSeconds())
}
