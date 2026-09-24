package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/infrakit-io/talos-docker-bootstrap/internal/config"
	"github.com/infrakit-io/talos-docker-bootstrap/internal/ssh"
)

func testConfig() config.Config {
	return config.Config{
		VM: config.VMConfig{
			Host:           "192.168.1.10",
			Port:           22,
			User:           "dev",
			SSHPrivateKey:  "/tmp/id_ed25519",
			KnownHostsFile: "/tmp/known_hosts",
		},
		Hardening: config.HardeningConfig{
			Enabled:          true,
			AllowPasswordSSH: false,
			EnableUFW:        true,
			AllowTCPPorts:    []int{22},
		},
		Docker: config.DockerConfig{Version: "28.5.2"},
		Talos: config.TalosConfig{
			Version:        "1.12.4",
			SHA256Checksum: "6b85f633721e02d31c8a28a633c9cd8ebfb7e41677ff29e94236a082d4cd6cd9",
		},
		Cluster: config.ClusterConfig{
			Name:     "devvm",
			StateDir: "/home/dev/.talos/clusters/devvm",
			MountSrc: "/home/dev/work",
			MountDst: "/var/mnt/work",
		},
		Timeouts: config.TimeoutsConfig{
			SSHConnectSeconds: 1,
			SSHRetries:        1,
			SSHRetryDelaySec:  1,
			TotalMinutes:      1,
		},
	}
}

func TestRunClusterCreateBuildsSingleNodeScript(t *testing.T) {
	cfg := testConfig()
	orig := sshRunScriptFn
	t.Cleanup(func() { sshRunScriptFn = orig })

	var script string
	sshRunScriptFn = func(_ context.Context, _ ssh.ExecConfig, s string) (string, string, error) {
		script = s
		return "", "", nil
	}

	if err := runClusterCreate(context.Background(), slog.Default(), cfg); err != nil {
		t.Fatalf("runClusterCreate failed: %v", err)
	}
	if !strings.Contains(script, "--workers 0") {
		t.Fatalf("expected single-node worker flag in script")
	}
	if !strings.Contains(script, "talosconfig") || !strings.Contains(script, "kubeconfig") {
		t.Fatalf("expected talos artifacts handling in script")
	}
}

func TestClusterStatusUsesConfiguredNameAndState(t *testing.T) {
	cfg := testConfig()
	orig := sshRunCommandFn
	t.Cleanup(func() { sshRunCommandFn = orig })

	var gotCmd string
	sshRunCommandFn = func(_ context.Context, _ ssh.ExecConfig, cmd string) (string, string, error) {
		gotCmd = cmd
		return "ok\n", "", nil
	}

	out, err := ClusterStatus(context.Background(), slog.Default(), cfg)
	if err != nil {
		t.Fatalf("ClusterStatus failed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %q", out)
	}
	if !strings.Contains(gotCmd, "devvm") || !strings.Contains(gotCmd, cfg.Cluster.StateDir) {
		t.Fatalf("cluster status command does not contain expected name/state")
	}
}

func TestKubeconfigExportReturnsRemoteContent(t *testing.T) {
	cfg := testConfig()
	orig := sshRunCommandFn
	t.Cleanup(func() { sshRunCommandFn = orig })

	sshRunCommandFn = func(_ context.Context, _ ssh.ExecConfig, _ string) (string, string, error) {
		return "apiVersion: v1\n", "", nil
	}

	out, err := KubeconfigExport(context.Background(), slog.Default(), cfg)
	if err != nil {
		t.Fatalf("KubeconfigExport failed: %v", err)
	}
	if !strings.Contains(out, "apiVersion: v1") {
		t.Fatalf("unexpected kubeconfig output: %q", out)
	}
}

func TestMountCheckScriptUsesDockerInspect(t *testing.T) {
	cfg := testConfig()
	orig := sshRunScriptFn
	t.Cleanup(func() { sshRunScriptFn = orig })

	var script string
	sshRunScriptFn = func(_ context.Context, _ ssh.ExecConfig, s string) (string, string, error) {
		script = s
		return "", "", nil
	}

	if err := MountCheck(context.Background(), slog.Default(), cfg); err != nil {
		t.Fatalf("MountCheck failed: %v", err)
	}
	if !strings.Contains(script, "docker inspect") {
		t.Fatalf("expected docker inspect in mount-check script")
	}
}

func TestRunDryRunPlansAllSteps(t *testing.T) {
	cfg := testConfig()
	res, err := Run(context.Background(), slog.Default(), cfg, Options{DryRun: true})
	if err != nil {
		t.Fatalf("Run dry-run failed: %v", err)
	}
	if res.Status != "planned" {
		t.Fatalf("expected planned status, got %q", res.Status)
	}
	if len(res.Steps) != 6 {
		t.Fatalf("expected 6 planned steps, got %d", len(res.Steps))
	}
}

func TestRunSuccessWithInjectedSteps(t *testing.T) {
	cfg := testConfig()
	reset := patchRunDeps()
	defer reset()

	waitForTCPPortWithStatsFn = func(_ context.Context, _ string, _ int, _ int, _, _ time.Duration) (ssh.TCPCheckStats, error) {
		return ssh.TCPCheckStats{Attempts: 1, Elapsed: time.Millisecond}, nil
	}
	runOSHardeningFn = func(context.Context, *slog.Logger, config.Config) error { return nil }
	runDockerInstallFn = func(context.Context, *slog.Logger, config.Config) error { return nil }
	runTalosctlInstallFn = func(context.Context, *slog.Logger, config.Config) error { return nil }
	runClusterCreateFn = func(context.Context, *slog.Logger, config.Config) error { return nil }

	res, err := Run(context.Background(), slog.Default(), cfg, Options{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("expected success status, got %q", res.Status)
	}
	if len(res.Steps) != 6 {
		t.Fatalf("expected 6 steps, got %d", len(res.Steps))
	}
}

func TestRunFailurePropagatesStepName(t *testing.T) {
	cfg := testConfig()
	reset := patchRunDeps()
	defer reset()

	waitForTCPPortWithStatsFn = func(_ context.Context, _ string, _ int, _ int, _, _ time.Duration) (ssh.TCPCheckStats, error) {
		return ssh.TCPCheckStats{Attempts: 1, Elapsed: time.Millisecond}, nil
	}
	runOSHardeningFn = func(context.Context, *slog.Logger, config.Config) error { return errors.New("boom") }
	runDockerInstallFn = func(context.Context, *slog.Logger, config.Config) error { return nil }
	runTalosctlInstallFn = func(context.Context, *slog.Logger, config.Config) error { return nil }
	runClusterCreateFn = func(context.Context, *slog.Logger, config.Config) error { return nil }

	res, err := Run(context.Background(), slog.Default(), cfg, Options{})
	if err == nil {
		t.Fatalf("expected failure")
	}
	if res.Status != "failed" {
		t.Fatalf("expected failed status, got %q", res.Status)
	}
	if !strings.Contains(res.Error, "os_hardening") {
		t.Fatalf("expected failing step in error, got %q", res.Error)
	}
}

func TestRunOSHardeningDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.Hardening.Enabled = false
	if err := runOSHardening(context.Background(), slog.Default(), cfg); err != nil {
		t.Fatalf("runOSHardening disabled should be no-op: %v", err)
	}
}

func TestRunOSHardeningBuildsScript(t *testing.T) {
	cfg := testConfig()
	orig := sshRunScriptFn
	t.Cleanup(func() { sshRunScriptFn = orig })

	var script string
	sshRunScriptFn = func(_ context.Context, _ ssh.ExecConfig, s string) (string, string, error) {
		script = s
		return "", "", nil
	}
	if err := runOSHardening(context.Background(), slog.Default(), cfg); err != nil {
		t.Fatalf("runOSHardening failed: %v", err)
	}
	if !strings.Contains(script, "PasswordAuthentication no") {
		t.Fatalf("expected hardened SSH config in script")
	}
	if !strings.Contains(script, "ufw --force enable") {
		t.Fatalf("expected ufw setup in script")
	}
}

func TestRunOSHardeningCustomFlags(t *testing.T) {
	cfg := testConfig()
	cfg.Hardening.AllowPasswordSSH = true
	cfg.Hardening.EnableUFW = false
	orig := sshRunScriptFn
	t.Cleanup(func() { sshRunScriptFn = orig })

	var script string
	sshRunScriptFn = func(_ context.Context, _ ssh.ExecConfig, s string) (string, string, error) {
		script = s
		return "", "", nil
	}
	if err := runOSHardening(context.Background(), slog.Default(), cfg); err != nil {
		t.Fatalf("runOSHardening failed: %v", err)
	}
	if !strings.Contains(script, "PasswordAuthentication yes") {
		t.Fatalf("expected password auth toggle in script")
	}
	if !strings.Contains(script, `if [ "false" = "true" ]; then`) {
		t.Fatalf("expected ufw disabled branch in script")
	}
}

func TestRunDockerInstallBuildsScript(t *testing.T) {
	cfg := testConfig()
	orig := sshRunScriptFn
	t.Cleanup(func() { sshRunScriptFn = orig })

	var script string
	sshRunScriptFn = func(_ context.Context, _ ssh.ExecConfig, s string) (string, string, error) {
		script = s
		return "", "", nil
	}
	if err := runDockerInstall(context.Background(), slog.Default(), cfg); err != nil {
		t.Fatalf("runDockerInstall failed: %v", err)
	}
	if !strings.Contains(script, cfg.Docker.Version) || !strings.Contains(script, "apt-cache madison docker-ce") {
		t.Fatalf("expected docker version install script")
	}
}

func TestRunTalosctlInstallBuildsScript(t *testing.T) {
	cfg := testConfig()
	orig := sshRunScriptFn
	t.Cleanup(func() { sshRunScriptFn = orig })

	var script string
	sshRunScriptFn = func(_ context.Context, _ ssh.ExecConfig, s string) (string, string, error) {
		script = s
		return "", "", nil
	}
	if err := runTalosctlInstall(context.Background(), slog.Default(), cfg); err != nil {
		t.Fatalf("runTalosctlInstall failed: %v", err)
	}
	if !strings.Contains(script, cfg.Talos.Version) || !strings.Contains(script, cfg.Talos.SHA256Checksum) {
		t.Fatalf("expected talosctl install script with version/checksum")
	}
}

func TestHumanStepLabel(t *testing.T) {
	if got := humanStepLabel("cluster_create"); got != "cluster-create" {
		t.Fatalf("unexpected label: %q", got)
	}
}

func TestExecConfigMapping(t *testing.T) {
	cfg := testConfig()
	got := execConfig(cfg)
	if got.Host != cfg.VM.Host || got.User != cfg.VM.User || got.Port != cfg.VM.Port {
		t.Fatalf("unexpected ssh exec config mapping: %#v", got)
	}
}

func TestClusterStatusPropagatesError(t *testing.T) {
	cfg := testConfig()
	orig := sshRunCommandFn
	t.Cleanup(func() { sshRunCommandFn = orig })

	sshRunCommandFn = func(_ context.Context, _ ssh.ExecConfig, _ string) (string, string, error) {
		return "", "", errors.New("boom")
	}
	if _, err := ClusterStatus(context.Background(), slog.Default(), cfg); err == nil {
		t.Fatalf("expected error")
	}
}

func TestRunRemoteScriptPropagatesError(t *testing.T) {
	cfg := testConfig()
	orig := sshRunScriptFn
	t.Cleanup(func() { sshRunScriptFn = orig })

	sshRunScriptFn = func(_ context.Context, _ ssh.ExecConfig, _ string) (string, string, error) {
		return "", "stderr", errors.New("boom")
	}
	if err := runRemoteScript(context.Background(), slog.Default(), cfg, "x", "echo"); err == nil {
		t.Fatalf("expected runRemoteScript error")
	}
}

func TestSetKnownHostsPromptRestore(t *testing.T) {
	orig := knownHostsPromptFn
	restore := SetKnownHostsPrompt(func(message string) (bool, error) { return true, nil })
	if knownHostsPromptFn == nil {
		t.Fatalf("expected prompt function to be set")
	}
	restore()
	if fmt.Sprintf("%p", knownHostsPromptFn) != fmt.Sprintf("%p", orig) {
		t.Fatalf("expected prompt function to be restored")
	}
}

func TestStartStepHeartbeatCanStartAndStop(t *testing.T) {
	stop := startStepHeartbeat("cluster_create")
	time.Sleep(10 * time.Millisecond)
	stop()
}

func patchRunDeps() func() {
	origWait := waitForTCPPortWithStatsFn
	origHardening := runOSHardeningFn
	origTimeSync := runTimeSyncFn
	origDocker := runDockerInstallFn
	origTalos := runTalosctlInstallFn
	origCluster := runClusterCreateFn
	return func() {
		waitForTCPPortWithStatsFn = origWait
		runOSHardeningFn = origHardening
		runTimeSyncFn = origTimeSync
		runDockerInstallFn = origDocker
		runTalosctlInstallFn = origTalos
		runClusterCreateFn = origCluster
	}
}
