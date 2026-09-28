package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds Talos bootstrap settings.
type Config struct {
	VM        VMConfig        `yaml:"vm"`
	Hardening HardeningConfig `yaml:"hardening"`
	Docker    DockerConfig    `yaml:"docker"`
	Talos     TalosConfig     `yaml:"talos"`
	Cluster   ClusterConfig   `yaml:"cluster"`
	TimeSync  TimeSyncConfig  `yaml:"time_sync"`
	Timeouts  TimeoutsConfig  `yaml:"timeouts"`
}

var (
	safeVersionTokenRE = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
	sha256HexRE        = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
	sshFingerprintRE   = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]+$`)
	// ntpServerRE admits hostnames, IPv4 and IPv6 literals only. Server names
	// are interpolated into a remote shell script, so anything outside this
	// set (spaces, quotes, $, ;) is rejected at validation time.
	ntpServerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)
)

// MaxTimeSyncWaitSeconds bounds how long the time_sync step may wait for the
// clock to report synchronized.
const MaxTimeSyncWaitSeconds = 3600

// DefaultTimeSyncWaitSeconds is applied when time_sync is enabled and
// wait_seconds is omitted.
const DefaultTimeSyncWaitSeconds = 120

type VMConfig struct {
	Host               string `yaml:"host"`
	Port               int    `yaml:"port"`
	User               string `yaml:"user"`
	SSHPrivateKey      string `yaml:"ssh_private_key"`
	KnownHostsFile     string `yaml:"known_hosts_file"`
	KnownHostsMode     string `yaml:"known_hosts_mode"`
	SSHHostFingerprint string `yaml:"ssh_host_fingerprint"`
}

type DockerConfig struct {
	Version string `yaml:"version"`
}

type HardeningConfig struct {
	Enabled          bool  `yaml:"enabled"`
	AllowPasswordSSH bool  `yaml:"allow_password_ssh"`
	EnableUFW        bool  `yaml:"enable_ufw"`
	AllowTCPPorts    []int `yaml:"allow_tcp_ports"`
}

type TalosConfig struct {
	Version        string `yaml:"version"`
	SHA256Checksum string `yaml:"sha256_checksum"`
}

type ClusterConfig struct {
	// Enabled controls the Talos steps (talosctl_install, cluster_create).
	// Omitted means enabled, which keeps the behaviour of configs written
	// before this field existed. Set to false to stop after OS hardening,
	// time sync and Docker: the talos.* and cluster name/state/mount fields
	// are then not required.
	Enabled  *bool  `yaml:"enabled,omitempty"`
	Name     string `yaml:"name"`
	StateDir string `yaml:"state_dir"`
	MountSrc string `yaml:"mount_src"`
	MountDst string `yaml:"mount_dst"`
}

// IsEnabled reports whether the Talos-in-Docker cluster steps should run.
// A nil Enabled (field omitted) means true.
func (c ClusterConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// TimeSyncConfig controls the time_sync step. It is opt-in so that existing
// configs keep their behaviour; production hosts should enable it.
type TimeSyncConfig struct {
	Enabled bool `yaml:"enabled"`
	// Servers optionally replaces the NTP servers of the active time daemon
	// (chrony when it is running, systemd-timesyncd otherwise). Empty keeps
	// the distribution defaults.
	Servers []string `yaml:"servers"`
	// WaitSeconds is how long to wait for the kernel clock to report
	// synchronized before failing. 0 means DefaultTimeSyncWaitSeconds.
	WaitSeconds int `yaml:"wait_seconds"`
}

// EffectiveWaitSeconds returns WaitSeconds, or the default when unset.
func (t TimeSyncConfig) EffectiveWaitSeconds() int {
	if t.WaitSeconds <= 0 {
		return DefaultTimeSyncWaitSeconds
	}
	return t.WaitSeconds
}

type TimeoutsConfig struct {
	SSHConnectSeconds int `yaml:"ssh_connect_seconds"`
	SSHRetries        int `yaml:"ssh_retries"`
	SSHRetryDelaySec  int `yaml:"ssh_retry_delay_seconds"`
	TotalMinutes      int `yaml:"total_minutes"`
}

func (t TimeoutsConfig) SSHConnectDuration() time.Duration {
	return time.Duration(t.SSHConnectSeconds) * time.Second
}

func (t TimeoutsConfig) SSHRetryDelayDuration() time.Duration {
	return time.Duration(t.SSHRetryDelaySec) * time.Second
}

func (t TimeoutsConfig) TotalDuration() time.Duration {
	return time.Duration(t.TotalMinutes) * time.Minute
}

func defaultConfig() Config {
	return Config{
		VM: VMConfig{Port: 22, KnownHostsMode: "strict"},
		Hardening: HardeningConfig{
			Enabled:          true,
			AllowPasswordSSH: false,
			EnableUFW:        true,
			AllowTCPPorts:    []int{22},
		},
		Timeouts: TimeoutsConfig{
			SSHConnectSeconds: 5,
			SSHRetries:        12,
			SSHRetryDelaySec:  10,
			TotalMinutes:      20,
		},
	}
}

func Load(path string) (Config, error) {
	cfg := defaultConfig()

	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	expanded := os.ExpandEnv(string(content))
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}

	applyEnvOverrides(&cfg)
	expandHomePaths(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("TDB_VM_HOST"); v != "" {
		cfg.VM.Host = v
	}
	if v := os.Getenv("TDB_VM_USER"); v != "" {
		cfg.VM.User = v
	}
	if v := os.Getenv("TDB_VM_SSH_PRIVATE_KEY"); v != "" {
		cfg.VM.SSHPrivateKey = v
	}
	if v := os.Getenv("TDB_CLUSTER_STATE_DIR"); v != "" {
		cfg.Cluster.StateDir = v
	}
}

func expandHomePaths(cfg *Config) {
	cfg.VM.SSHPrivateKey = expandHome(cfg.VM.SSHPrivateKey)
	cfg.VM.KnownHostsFile = expandHome(cfg.VM.KnownHostsFile)
	cfg.Cluster.StateDir = expandHome(cfg.Cluster.StateDir)
	cfg.Cluster.MountSrc = expandHome(cfg.Cluster.MountSrc)
}

func expandHome(path string) string {
	p := strings.TrimSpace(path)
	if p == "" || !strings.HasPrefix(p, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}
	return path
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.VM.Host) == "" {
		return fmt.Errorf("vm.host is required")
	}
	if c.VM.Port <= 0 || c.VM.Port > 65535 {
		return fmt.Errorf("vm.port must be in range 1..65535")
	}
	if strings.TrimSpace(c.VM.User) == "" {
		return fmt.Errorf("vm.user is required")
	}
	if strings.TrimSpace(c.VM.SSHPrivateKey) == "" {
		return fmt.Errorf("vm.ssh_private_key is required")
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(c.VM.SSHPrivateKey)), ".pub") {
		return fmt.Errorf("vm.ssh_private_key must point to a private key, not a .pub file")
	}
	if mode := normalizeKnownHostsMode(c.VM.KnownHostsMode); mode == "" {
		return fmt.Errorf("vm.known_hosts_mode must be one of: strict, prompt, accept-new, auto-refresh")
	}
	if fp := strings.TrimSpace(c.VM.SSHHostFingerprint); fp != "" {
		if !sshFingerprintRE.MatchString(fp) {
			return fmt.Errorf("vm.ssh_host_fingerprint must be in SHA256:... format")
		}
		if strings.TrimSpace(c.VM.KnownHostsFile) == "" {
			return fmt.Errorf("vm.known_hosts_file is required when vm.ssh_host_fingerprint is set")
		}
	}
	if mode := normalizeKnownHostsMode(c.VM.KnownHostsMode); (mode == "prompt" || mode == "auto-refresh") && strings.TrimSpace(c.VM.KnownHostsFile) == "" {
		return fmt.Errorf("vm.known_hosts_file is required when vm.known_hosts_mode is %s", mode)
	}
	if strings.TrimSpace(c.Docker.Version) == "" {
		return fmt.Errorf("docker.version is required")
	}
	if !isSafeVersionToken(c.Docker.Version) {
		return fmt.Errorf("docker.version has invalid characters")
	}
	if err := c.validateTalosAndCluster(); err != nil {
		return err
	}
	if err := c.TimeSync.validate(); err != nil {
		return err
	}
	if c.Timeouts.SSHConnectSeconds <= 0 {
		return fmt.Errorf("timeouts.ssh_connect_seconds must be > 0")
	}
	if c.Timeouts.SSHRetries <= 0 {
		return fmt.Errorf("timeouts.ssh_retries must be > 0")
	}
	if c.Timeouts.SSHRetryDelaySec <= 0 {
		return fmt.Errorf("timeouts.ssh_retry_delay_seconds must be > 0")
	}
	if c.Timeouts.TotalMinutes <= 0 {
		return fmt.Errorf("timeouts.total_minutes must be > 0")
	}
	for _, p := range c.Hardening.AllowTCPPorts {
		if p <= 0 || p > 65535 {
			return fmt.Errorf("hardening.allow_tcp_ports entries must be in range 1..65535 (got %d)", p)
		}
	}
	return nil
}

// validateTalosAndCluster requires the Talos and cluster fields only when the
// cluster steps will run. When the cluster is disabled, any Talos value that
// IS present must still be well-formed, so a typo is never silently carried.
func (c Config) validateTalosAndCluster() error {
	if !c.Cluster.IsEnabled() {
		if v := strings.TrimSpace(c.Talos.Version); v != "" && !isSafeVersionToken(v) {
			return fmt.Errorf("talos.version has invalid characters")
		}
		if v := strings.TrimSpace(c.Talos.SHA256Checksum); v != "" && !sha256HexRE.MatchString(v) {
			return fmt.Errorf("talos.sha256_checksum must be a valid SHA256 hex digest")
		}
		return nil
	}
	if strings.TrimSpace(c.Talos.Version) == "" {
		return fmt.Errorf("talos.version is required (or set cluster.enabled: false)")
	}
	if !isSafeVersionToken(c.Talos.Version) {
		return fmt.Errorf("talos.version has invalid characters")
	}
	if strings.TrimSpace(c.Talos.SHA256Checksum) == "" {
		return fmt.Errorf("talos.sha256_checksum is required (or set cluster.enabled: false)")
	}
	if !sha256HexRE.MatchString(c.Talos.SHA256Checksum) {
		return fmt.Errorf("talos.sha256_checksum must be a valid SHA256 hex digest")
	}
	if strings.TrimSpace(c.Cluster.Name) == "" {
		return fmt.Errorf("cluster.name is required")
	}
	if strings.TrimSpace(c.Cluster.StateDir) == "" {
		return fmt.Errorf("cluster.state_dir is required")
	}
	if strings.TrimSpace(c.Cluster.MountSrc) == "" {
		return fmt.Errorf("cluster.mount_src is required")
	}
	if strings.TrimSpace(c.Cluster.MountDst) == "" {
		return fmt.Errorf("cluster.mount_dst is required")
	}
	return nil
}

func (t TimeSyncConfig) validate() error {
	if t.WaitSeconds < 0 || t.WaitSeconds > MaxTimeSyncWaitSeconds {
		return fmt.Errorf("time_sync.wait_seconds must be in range 0..%d", MaxTimeSyncWaitSeconds)
	}
	for _, s := range t.Servers {
		if !ntpServerRE.MatchString(s) {
			return fmt.Errorf("time_sync.servers entry %q is not a valid hostname or IP address", s)
		}
	}
	return nil
}

func isSafeVersionToken(v string) bool {
	return safeVersionTokenRE.MatchString(v)
}

func normalizeKnownHostsMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "strict":
		return "strict"
	case "prompt":
		return "prompt"
	case "accept-new", "accept_new":
		return "accept-new"
	case "auto-refresh", "auto_refresh":
		return "auto-refresh"
	default:
		return ""
	}
}
