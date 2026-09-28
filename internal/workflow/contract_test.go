package workflow

import (
	"testing"

	"github.com/infrakit-io/talos-docker-bootstrap/internal/config"
)

func TestBootstrapResultValidateOK(t *testing.T) {
	r := BootstrapResult{
		VMName:        "devvm-01",
		IPAddress:     "192.168.1.50",
		SSHUser:       "dev",
		SSHPrivateKey: "/home/dev/.ssh/id_ed25519",
		SSHPort:       22,
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("expected valid BootstrapResult, got %v", err)
	}
}

func TestBootstrapResultValidateMissingField(t *testing.T) {
	r := BootstrapResult{
		VMName:        "devvm-01",
		SSHUser:       "dev",
		SSHPrivateKey: "/home/dev/.ssh/id_ed25519",
	}
	if err := r.Validate(); err == nil {
		t.Fatalf("expected validation error for missing ip")
	}
}

func TestMergeBootstrapIntoStage2(t *testing.T) {
	base := mustValidStage2Config(t)
	bootstrapResult := BootstrapResult{
		VMName:             "devvm-01",
		IPAddress:          "192.168.1.50",
		SSHUser:            "developer",
		SSHPrivateKey:      "/tmp/key",
		SSHPort:            2222,
		SSHHostFingerprint: "SHA256:abc123def456",
	}

	got, err := MergeBootstrapIntoStage2(base, bootstrapResult)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	if got.VM.Host != "192.168.1.50" {
		t.Fatalf("unexpected vm.host: %s", got.VM.Host)
	}
	if got.VM.User != "developer" {
		t.Fatalf("unexpected vm.user: %s", got.VM.User)
	}
	if got.VM.SSHPrivateKey != "/tmp/key" {
		t.Fatalf("unexpected vm.ssh_private_key: %s", got.VM.SSHPrivateKey)
	}
	if got.VM.Port != 2222 {
		t.Fatalf("unexpected vm.port: %d", got.VM.Port)
	}
	if got.VM.SSHHostFingerprint != "SHA256:abc123def456" {
		t.Fatalf("unexpected vm.ssh_host_fingerprint: %s", got.VM.SSHHostFingerprint)
	}
}

func mustValidStage2Config(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Config{
		VM: config.VMConfig{
			Host:           "127.0.0.1",
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
		Docker: config.DockerConfig{Version: "28.0.2"},
		Talos: config.TalosConfig{
			Version:        "1.12.3",
			SHA256Checksum: "2baf4747e5f6b7f3655f47c665b45dec0c4b6935f0be9614dfe2262c3079eb93",
		},
		Cluster: config.ClusterConfig{
			Name:     "devvm",
			StateDir: "/tmp/devvm",
			MountSrc: "/tmp",
			MountDst: "/var/mnt/work",
		},
		Timeouts: config.TimeoutsConfig{
			SSHConnectSeconds: 5,
			SSHRetries:        3,
			SSHRetryDelaySec:  1,
			TotalMinutes:      5,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid base config: %v", err)
	}
	return cfg
}

func TestMergeBootstrapIntoDockerHostOnlyConfig(t *testing.T) {
	disabled := false
	base := config.Config{
		VM:        config.VMConfig{Port: 22, KnownHostsMode: "strict"},
		Hardening: config.HardeningConfig{Enabled: true, EnableUFW: true, AllowTCPPorts: []int{22}},
		Docker:    config.DockerConfig{Version: "28.5.2"},
		Cluster:   config.ClusterConfig{Enabled: &disabled},
		TimeSync:  config.TimeSyncConfig{Enabled: true},
		Timeouts:  config.TimeoutsConfig{SSHConnectSeconds: 5, SSHRetries: 1, SSHRetryDelaySec: 1, TotalMinutes: 1},
	}
	got, err := MergeBootstrapIntoStage2(base, BootstrapResult{
		VMName:        "app-01",
		IPAddress:     "192.168.1.60",
		SSHUser:       "sysadmin",
		SSHPrivateKey: "/tmp/key",
		SSHPort:       22,
	})
	if err != nil {
		t.Fatalf("docker-host merge must validate without talos fields: %v", err)
	}
	if got.Cluster.IsEnabled() || got.VM.Host != "192.168.1.60" {
		t.Fatalf("unexpected merge result: %#v", got)
	}
}
