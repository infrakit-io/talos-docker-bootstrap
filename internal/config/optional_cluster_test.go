package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func baseDockerHostConfig() Config {
	cfg := defaultConfig()
	cfg.VM.Host = "192.168.1.100"
	cfg.VM.User = "dev"
	cfg.VM.SSHPrivateKey = "~/.ssh/id_ed25519"
	cfg.Docker.Version = "28.5.2"
	disabled := false
	cfg.Cluster.Enabled = &disabled
	return cfg
}

func TestClusterIsEnabledDefaultsToTrue(t *testing.T) {
	var c ClusterConfig
	if !c.IsEnabled() {
		t.Fatalf("omitted cluster.enabled must mean enabled")
	}
	on, off := true, false
	if !(ClusterConfig{Enabled: &on}).IsEnabled() {
		t.Fatalf("explicit true must be enabled")
	}
	if (ClusterConfig{Enabled: &off}).IsEnabled() {
		t.Fatalf("explicit false must be disabled")
	}
}

func TestValidateClusterDisabledDoesNotRequireTalos(t *testing.T) {
	cfg := baseDockerHostConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("docker-host config must validate without talos/cluster fields: %v", err)
	}
}

func TestValidateClusterOmittedStillRequiresTalos(t *testing.T) {
	// Positive control: the identical config with cluster.enabled omitted
	// must keep failing exactly as it did before the field existed.
	cfg := baseDockerHostConfig()
	cfg.Cluster.Enabled = nil
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "talos.version is required") {
		t.Fatalf("expected talos.version required, got %v", err)
	}
}

func TestValidateClusterDisabledRejectsMalformedTalosValues(t *testing.T) {
	cfg := baseDockerHostConfig()
	cfg.Talos.Version = "1.12.4; rm -rf /"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("malformed talos.version must be rejected even when the cluster is disabled")
	}
	cfg = baseDockerHostConfig()
	cfg.Talos.SHA256Checksum = "deadbeef"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("malformed talos.sha256_checksum must be rejected even when the cluster is disabled")
	}
}

func TestValidateTimeSync(t *testing.T) {
	cases := []struct {
		name    string
		ts      TimeSyncConfig
		wantErr bool
	}{
		{"disabled default", TimeSyncConfig{}, false},
		{"enabled defaults", TimeSyncConfig{Enabled: true}, false},
		{"hostnames and ips", TimeSyncConfig{Enabled: true, Servers: []string{"0.ubuntu.pool.ntp.org", "192.168.1.1", "2001:db8::1"}}, false},
		{"max wait", TimeSyncConfig{Enabled: true, WaitSeconds: MaxTimeSyncWaitSeconds}, false},
		{"negative wait", TimeSyncConfig{Enabled: true, WaitSeconds: -1}, true},
		{"wait too long", TimeSyncConfig{Enabled: true, WaitSeconds: MaxTimeSyncWaitSeconds + 1}, true},
		{"shell injection", TimeSyncConfig{Enabled: true, Servers: []string{"pool.ntp.org; reboot"}}, true},
		{"command substitution", TimeSyncConfig{Enabled: true, Servers: []string{"$(id)"}}, true},
		{"quote", TimeSyncConfig{Enabled: true, Servers: []string{`a"b`}}, true},
		{"empty entry", TimeSyncConfig{Enabled: true, Servers: []string{""}}, true},
		{"leading dash", TimeSyncConfig{Enabled: true, Servers: []string{"-x"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseDockerHostConfig()
			cfg.TimeSync = tc.ts
			err := cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got %v", tc.wantErr, err)
			}
		})
	}
}

func TestTimeSyncEffectiveWaitSeconds(t *testing.T) {
	if got := (TimeSyncConfig{}).EffectiveWaitSeconds(); got != DefaultTimeSyncWaitSeconds {
		t.Fatalf("expected default %d, got %d", DefaultTimeSyncWaitSeconds, got)
	}
	if got := (TimeSyncConfig{WaitSeconds: 30}).EffectiveWaitSeconds(); got != 30 {
		t.Fatalf("expected 30, got %d", got)
	}
}

func TestLoadDockerHostOnlyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	content := []byte(`
vm:
  host: 10.0.0.1
  user: dev
  ssh_private_key: /tmp/key
docker:
  version: "28.5.2"
cluster:
  enabled: false
time_sync:
  enabled: true
  servers: ["0.pool.ntp.org"]
  wait_seconds: 60
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Cluster.IsEnabled() {
		t.Fatalf("cluster.enabled: false was not honoured")
	}
	if !cfg.TimeSync.Enabled || cfg.TimeSync.WaitSeconds != 60 || len(cfg.TimeSync.Servers) != 1 {
		t.Fatalf("time_sync not loaded: %#v", cfg.TimeSync)
	}
	if !cfg.Hardening.Enabled || !cfg.Hardening.EnableUFW {
		t.Fatalf("hardening defaults must still apply: %#v", cfg.Hardening)
	}
}

func TestLoadRejectsDockerHostConfigWithoutEnabledFalse(t *testing.T) {
	// Same file minus `cluster.enabled: false` is the pre-existing contract:
	// talos fields are required.
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	content := []byte(`
vm:
  host: 10.0.0.1
  user: dev
  ssh_private_key: /tmp/key
docker:
  version: "28.5.2"
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("expected talos fields to be required when cluster.enabled is omitted")
	}
}

func TestShippedExampleConfigsLoad(t *testing.T) {
	dockerHost, err := Load(filepath.Join("..", "..", "configs", "docker-host.example.yaml"))
	if err != nil {
		t.Fatalf("docker-host example must load: %v", err)
	}
	if dockerHost.Cluster.IsEnabled() || !dockerHost.TimeSync.Enabled {
		t.Fatalf("docker-host example must disable the cluster and enable time sync: %#v", dockerHost)
	}
	talos, err := Load(filepath.Join("..", "..", "configs", "talos-bootstrap.example.yaml"))
	if err != nil {
		t.Fatalf("talos example must load: %v", err)
	}
	if !talos.Cluster.IsEnabled() || talos.TimeSync.Enabled {
		t.Fatalf("talos example must keep the historical defaults: %#v", talos)
	}
}
