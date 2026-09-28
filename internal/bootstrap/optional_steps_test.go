package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/infrakit-io/talos-docker-bootstrap/internal/config"
	"github.com/infrakit-io/talos-docker-bootstrap/internal/ssh"
	"github.com/infrakit-io/talos-docker-bootstrap/pkg/model"
)

func boolPtr(v bool) *bool { return &v }

// dockerHostConfig is a config that only wants a hardened Docker host: the
// cluster is switched off and no talos/cluster fields are filled in.
func dockerHostConfig() config.Config {
	cfg := testConfig()
	cfg.Talos = config.TalosConfig{}
	cfg.Cluster = config.ClusterConfig{Enabled: boolPtr(false)}
	return cfg
}

func stepByName(t *testing.T, steps []Step, name string) Step {
	t.Helper()
	for _, s := range steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("step %q not found in %#v", name, steps)
	return Step{}
}

func TestRunClusterDisabledSkipsTalosStepsAndNeverCallsThem(t *testing.T) {
	cfg := dockerHostConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("docker-host config should validate without talos fields: %v", err)
	}
	reset := patchRunDeps()
	defer reset()

	var called []string
	waitForTCPPortWithStatsFn = func(_ context.Context, _ string, _ int, _ int, _, _ time.Duration) (ssh.TCPCheckStats, error) {
		return ssh.TCPCheckStats{Attempts: 1, Elapsed: time.Millisecond}, nil
	}
	runOSHardeningFn = func(context.Context, *slog.Logger, config.Config) error {
		called = append(called, "os_hardening")
		return nil
	}
	runDockerInstallFn = func(context.Context, *slog.Logger, config.Config) error {
		called = append(called, "docker_install")
		return nil
	}
	runTalosctlInstallFn = func(context.Context, *slog.Logger, config.Config) error {
		t.Fatalf("talosctl_install must not run when cluster is disabled")
		return nil
	}
	runClusterCreateFn = func(context.Context, *slog.Logger, config.Config) error {
		t.Fatalf("cluster_create must not run when cluster is disabled")
		return nil
	}

	res, err := Run(context.Background(), slog.Default(), cfg, Options{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("expected success, got %q", res.Status)
	}
	if strings.Join(called, ",") != "os_hardening,docker_install" {
		t.Fatalf("unexpected executed steps: %v", called)
	}
	for _, name := range []string{"talosctl_install", "cluster_create"} {
		st := stepByName(t, res.Steps, name)
		if st.Status != model.StepStatusSkipped || st.Message != SkipReasonClusterDisabled {
			t.Fatalf("expected %s skipped with reason, got %#v", name, st)
		}
	}
	if res.Cluster != "" || res.KubeconfigPath != "" {
		t.Fatalf("docker-host result must not name a cluster or kubeconfig: %#v", res)
	}
}

func TestRunClusterEnabledByDefaultStillRunsTalosSteps(t *testing.T) {
	// Positive control for the test above: the same harness with the field
	// omitted (nil) must reach both Talos steps.
	cfg := testConfig()
	if cfg.Cluster.Enabled != nil {
		t.Fatalf("testConfig must leave cluster.enabled omitted")
	}
	reset := patchRunDeps()
	defer reset()

	var talosRan, clusterRan bool
	waitForTCPPortWithStatsFn = func(_ context.Context, _ string, _ int, _ int, _, _ time.Duration) (ssh.TCPCheckStats, error) {
		return ssh.TCPCheckStats{Attempts: 1}, nil
	}
	runOSHardeningFn = func(context.Context, *slog.Logger, config.Config) error { return nil }
	runDockerInstallFn = func(context.Context, *slog.Logger, config.Config) error { return nil }
	runTalosctlInstallFn = func(context.Context, *slog.Logger, config.Config) error { talosRan = true; return nil }
	runClusterCreateFn = func(context.Context, *slog.Logger, config.Config) error { clusterRan = true; return nil }

	res, err := Run(context.Background(), slog.Default(), cfg, Options{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !talosRan || !clusterRan {
		t.Fatalf("expected talos steps to run by default (talos=%v cluster=%v)", talosRan, clusterRan)
	}
	if res.Cluster != cfg.Cluster.Name || res.KubeconfigPath == "" {
		t.Fatalf("expected cluster name and kubeconfig path in result: %#v", res)
	}
	if st := stepByName(t, res.Steps, "cluster_create"); st.Status != model.StepStatusSuccess {
		t.Fatalf("expected cluster_create success, got %#v", st)
	}
}

func TestRunDryRunMarksDisabledStepsSkipped(t *testing.T) {
	cfg := dockerHostConfig()
	res, err := Run(context.Background(), slog.Default(), cfg, Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	want := map[string]model.StepStatus{
		"ssh_connectivity": model.StepStatusPlanned,
		"os_hardening":     model.StepStatusPlanned,
		"time_sync":        model.StepStatusSkipped,
		"docker_install":   model.StepStatusPlanned,
		"talosctl_install": model.StepStatusSkipped,
		"cluster_create":   model.StepStatusSkipped,
	}
	if len(res.Steps) != len(want) {
		t.Fatalf("expected %d steps, got %d", len(want), len(res.Steps))
	}
	for name, status := range want {
		if got := stepByName(t, res.Steps, name).Status; got != status {
			t.Fatalf("step %s: expected %s, got %s", name, status, got)
		}
	}
}

func TestRunTimeSyncRunsWhenEnabled(t *testing.T) {
	cfg := dockerHostConfig()
	cfg.TimeSync.Enabled = true
	reset := patchRunDeps()
	defer reset()

	var order []string
	waitForTCPPortWithStatsFn = func(_ context.Context, _ string, _ int, _ int, _, _ time.Duration) (ssh.TCPCheckStats, error) {
		return ssh.TCPCheckStats{Attempts: 1}, nil
	}
	runOSHardeningFn = func(context.Context, *slog.Logger, config.Config) error {
		order = append(order, "hardening")
		return nil
	}
	runTimeSyncFn = func(context.Context, *slog.Logger, config.Config) error {
		order = append(order, "time_sync")
		return nil
	}
	runDockerInstallFn = func(context.Context, *slog.Logger, config.Config) error { order = append(order, "docker"); return nil }

	res, err := Run(context.Background(), slog.Default(), cfg, Options{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if strings.Join(order, ",") != "hardening,time_sync,docker" {
		t.Fatalf("time_sync must run between hardening and docker, got %v", order)
	}
	if st := stepByName(t, res.Steps, "time_sync"); st.Status != model.StepStatusSuccess {
		t.Fatalf("expected time_sync success, got %#v", st)
	}
}

func TestRunTimeSyncFailureStopsBootstrap(t *testing.T) {
	cfg := dockerHostConfig()
	cfg.TimeSync.Enabled = true
	reset := patchRunDeps()
	defer reset()

	waitForTCPPortWithStatsFn = func(_ context.Context, _ string, _ int, _ int, _, _ time.Duration) (ssh.TCPCheckStats, error) {
		return ssh.TCPCheckStats{Attempts: 1}, nil
	}
	runOSHardeningFn = func(context.Context, *slog.Logger, config.Config) error { return nil }
	runTimeSyncFn = func(context.Context, *slog.Logger, config.Config) error { return errors.New("clock not synchronized") }
	runDockerInstallFn = func(context.Context, *slog.Logger, config.Config) error {
		t.Fatalf("docker_install must not run after a time_sync failure")
		return nil
	}

	res, err := Run(context.Background(), slog.Default(), cfg, Options{})
	if err == nil || !strings.Contains(res.Error, "time_sync") {
		t.Fatalf("expected time_sync failure, got err=%v res.Error=%q", err, res.Error)
	}
}

func TestRunTimeSyncDisabledIsNoop(t *testing.T) {
	cfg := testConfig()
	orig := sshRunScriptFn
	t.Cleanup(func() { sshRunScriptFn = orig })
	sshRunScriptFn = func(context.Context, ssh.ExecConfig, string) (string, string, error) {
		t.Fatalf("no remote script may run when time_sync is disabled")
		return "", "", nil
	}
	if err := runTimeSync(context.Background(), slog.Default(), cfg); err != nil {
		t.Fatalf("disabled time_sync should be a no-op: %v", err)
	}
}

func TestRunTimeSyncBuildsScript(t *testing.T) {
	cfg := testConfig()
	cfg.TimeSync = config.TimeSyncConfig{Enabled: true, Servers: []string{"0.pool.ntp.org", "192.168.1.1"}, WaitSeconds: 45}
	orig := sshRunScriptFn
	t.Cleanup(func() { sshRunScriptFn = orig })

	var script string
	sshRunScriptFn = func(_ context.Context, _ ssh.ExecConfig, s string) (string, string, error) {
		script = s
		return "", "", nil
	}
	if err := runTimeSync(context.Background(), slog.Default(), cfg); err != nil {
		t.Fatalf("runTimeSync failed: %v", err)
	}
	for _, want := range []string{
		`NTP_SERVERS="0.pool.ntp.org 192.168.1.1"`,
		"WAIT_SECONDS=45",
		"timedatectl show -p NTPSynchronized --value",
		"systemctl is-active --quiet chrony",
		"systemd-timesyncd",
		"exit 1",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("time_sync script missing %q", want)
		}
	}
}

func TestTimeSyncScriptUsesDefaultWait(t *testing.T) {
	script := timeSyncScript(config.TimeSyncConfig{Enabled: true})
	if !strings.Contains(script, "WAIT_SECONDS=120") || !strings.Contains(script, `NTP_SERVERS=""`) {
		t.Fatalf("expected default wait and no server override")
	}
}

func TestClusterOperationsRefuseWhenClusterDisabled(t *testing.T) {
	cfg := dockerHostConfig()
	origCmd, origScript := sshRunCommandFn, sshRunScriptFn
	t.Cleanup(func() { sshRunCommandFn, sshRunScriptFn = origCmd, origScript })
	sshRunCommandFn = func(context.Context, ssh.ExecConfig, string) (string, string, error) {
		t.Fatalf("no ssh command may run for a disabled cluster")
		return "", "", nil
	}
	sshRunScriptFn = func(context.Context, ssh.ExecConfig, string) (string, string, error) {
		t.Fatalf("no ssh script may run for a disabled cluster")
		return "", "", nil
	}

	if _, err := ClusterStatus(context.Background(), slog.Default(), cfg); !errors.Is(err, ErrClusterDisabled) {
		t.Fatalf("ClusterStatus: expected ErrClusterDisabled, got %v", err)
	}
	if _, err := KubeconfigExport(context.Background(), slog.Default(), cfg); !errors.Is(err, ErrClusterDisabled) {
		t.Fatalf("KubeconfigExport: expected ErrClusterDisabled, got %v", err)
	}
	if err := MountCheck(context.Background(), slog.Default(), cfg); !errors.Is(err, ErrClusterDisabled) {
		t.Fatalf("MountCheck: expected ErrClusterDisabled, got %v", err)
	}
}
