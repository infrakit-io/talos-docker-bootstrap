package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	wizard "github.com/infrakit-io/cli-wizard-core"
	sshutil "github.com/infrakit-io/talos-docker-bootstrap/internal/ssh"
	vmtool "github.com/infrakit-io/talos-docker-bootstrap/internal/tooling/vmbootstrap"
	vmconfig "github.com/infrakit-io/vmware-vm-bootstrap/pkg/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type stage2File struct {
	VM struct {
		Host               string `yaml:"host"`
		Port               int    `yaml:"port"`
		User               string `yaml:"user"`
		SSHPrivateKey      string `yaml:"ssh_private_key"`
		KnownHostsFile     string `yaml:"known_hosts_file"`
		KnownHostsMode     string `yaml:"known_hosts_mode"`
		SSHHostFingerprint string `yaml:"ssh_host_fingerprint"`
	} `yaml:"vm"`
	Hardening struct {
		Enabled          bool  `yaml:"enabled"`
		AllowPasswordSSH bool  `yaml:"allow_password_ssh"`
		EnableUFW        bool  `yaml:"enable_ufw"`
		AllowTCPPorts    []int `yaml:"allow_tcp_ports"`
	} `yaml:"hardening"`
	Docker struct {
		Version string `yaml:"version"`
	} `yaml:"docker"`
	Talos struct {
		Version        string `yaml:"version"`
		SHA256Checksum string `yaml:"sha256_checksum"`
	} `yaml:"talos"`
	Cluster struct {
		Enabled  *bool  `yaml:"enabled,omitempty"`
		Name     string `yaml:"name"`
		StateDir string `yaml:"state_dir"`
		MountSrc string `yaml:"mount_src"`
		MountDst string `yaml:"mount_dst"`
	} `yaml:"cluster"`
	TimeSync struct {
		Enabled     bool     `yaml:"enabled"`
		Servers     []string `yaml:"servers,omitempty"`
		WaitSeconds int      `yaml:"wait_seconds,omitempty"`
	} `yaml:"time_sync"`
	Timeouts struct {
		SSHConnectSeconds int `yaml:"ssh_connect_seconds"`
		SSHRetries        int `yaml:"ssh_retries"`
		SSHRetryDelaySec  int `yaml:"ssh_retry_delay_seconds"`
		TotalMinutes      int `yaml:"total_minutes"`
	} `yaml:"timeouts"`
}

type vmBootstrapFile struct {
	VM struct {
		Name       string `yaml:"name"`
		IPAddress  string `yaml:"ip_address"`
		Username   string `yaml:"username"`
		SSHKeyPath string `yaml:"ssh_key_path"`
		SSHPort    int    `yaml:"ssh_port"`
	} `yaml:"vm"`
}

type configManagerOptions struct {
	Stage2Path       string
	VMBootstrapBin   string
	VMBootstrapRepo  string
	VMBootstrapBuild bool
	UpdateNotify     bool
}

func menuLabel(tag, text string) string {
	return wizard.FormatMenuLabel(tag, text, 17)
}

var stdinReader = bufio.NewReader(os.Stdin)
var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
var caretEscapeRE = regexp.MustCompile(`\^\[\[[0-9;?]*[ -/]*[@-~]`)
var clusterNameUnsafeRE = regexp.MustCompile(`[^a-z0-9-]+`)
var hyphenCollapseRE = regexp.MustCompile(`-+`)

type toolVersionMetadata struct {
	Docker struct {
		LatestVersion     string `yaml:"latest_version"`
		LatestReleaseDate string `yaml:"latest_release_date"`
	} `yaml:"docker"`
	Talosctl struct {
		LatestVersion       string            `yaml:"latest_version"`
		LatestReleaseDate   string            `yaml:"latest_release_date"`
		ChecksumsLinuxAMD64 map[string]string `yaml:"checksums_linux_amd64"`
	} `yaml:"talosctl"`
}

var talosReleaseChecksumsURLFmt = "https://github.com/siderolabs/talos/releases/download/v%s/sha256sum.txt"

func newConfigCmd() *cobra.Command {
	var opts configManagerOptions

	cmd := &cobra.Command{
		Use:   "config",
		Short: "Interactive config manager",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runConfigManager(opts)
		},
	}

	cmd.Flags().StringVar(&opts.Stage2Path, "config", "configs/talos-bootstrap.yaml", "Path to Talos bootstrap config file")
	cmd.Flags().StringVar(&opts.VMBootstrapBin, "vmbootstrap-bin", "bin/vmbootstrap", "vmware-vm-bootstrap CLI binary")
	cmd.Flags().StringVar(&opts.VMBootstrapRepo, "vmbootstrap-repo", "../vmware-vm-bootstrap", "Path to vmware-vm-bootstrap repository (used only with --vmbootstrap-auto-build)")
	cmd.Flags().BoolVar(&opts.VMBootstrapBuild, "vmbootstrap-auto-build", false, "Auto-build vmbootstrap from --vmbootstrap-repo when binary is missing")
	cmd.Flags().BoolVar(&opts.UpdateNotify, "vmbootstrap-update-notify", true, "Show update notice when a newer vmbootstrap module version is available")
	return cmd
}

func runConfigManager(opts configManagerOptions) error {
	if opts.UpdateNotify {
		current, latest, hasUpdate, err := vmtool.IsUpdateAvailable()
		if err == nil && hasUpdate {
			fmt.Printf("\n\033[1;33m⚠ VMBOOTSTRAP UPDATE AVAILABLE: %s -> %s\033[0m\n", current, latest)
			fmt.Println("  Run: make update-vmbootstrap-pin install-vmbootstrap")
			fmt.Println()
		}
	}
	warnPinnedAssetDrift()

	resolvedBin, err := vmtool.ResolveBinary(vmtool.ResolveOptions{
		Bin:       opts.VMBootstrapBin,
		Repo:      opts.VMBootstrapRepo,
		AutoBuild: opts.VMBootstrapBuild,
	})
	if err != nil {
		return fmt.Errorf("resolve vmbootstrap binary: %w", err)
	}

	fmt.Println()
	fmt.Println("\033[1mtalos-docker-bootstrap — Config Manager\033[0m")
	fmt.Println("──────────────────────────────────────────────────")

	for {
		stage2Exists := fileExists(opts.Stage2Path)

		options := []string{}
		actions := map[string]func() error{}

		stage2Label := menuLabel("talos-bootstrap", fmt.Sprintf("Manage %s", filepath.Base(opts.Stage2Path)))
		options = append(options, stage2Label)
		actions[stage2Label] = func() error { return upsertStage2(opts.Stage2Path, stage2Exists, "") }

		drafts := listStage2Drafts(opts.Stage2Path)
		for _, d := range drafts {
			draftPath := d
			resumeLabel := fmt.Sprintf("\033[33m[draft]\033[0m   Resume %s", filepath.Base(draftPath))
			deleteLabel := fmt.Sprintf("\033[31m[draft]\033[0m   Delete %s", filepath.Base(draftPath))
			options = append(options, resumeLabel, deleteLabel)
			actions[resumeLabel] = func() error {
				return upsertStage2(opts.Stage2Path, stage2Exists, draftPath)
			}
			actions[deleteLabel] = func() error {
				if err := os.Remove(draftPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				fmt.Printf("  \033[32m✓ Draft deleted:\033[0m %s\n\n", draftPath)
				return nil
			}
		}

		vmLabel := menuLabel("vm-bootstrap", "Open vmbootstrap config manager")
		options = append(options, vmLabel)
		actions[vmLabel] = func() error { return launchVMBootstrapManager(resolvedBin) }

		options = append(options, "Exit")

		sel := wizard.NewSelector()
		choice := sel.Select(options, "Exit", "Select:")
		if sel.WasInterrupted() {
			return nil
		}
		if choice == "Exit" {
			fmt.Println()
			return nil
		}
		if fn := actions[choice]; fn != nil {
			if err := fn(); err != nil {
				if wizard.IsInterrupted(err) {
					return nil
				}
				return err
			}
		}
	}
}

func launchVMBootstrapManager(bin string) error {
	cmd := exec.Command(bin)
	if workdir := deriveVMBootstrapWorkDir(bin); workdir != "" {
		cmd.Dir = workdir
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 130 {
			return wizard.ErrInterrupted
		}
		return fmt.Errorf("run %s config manager: %w", bin, err)
	}
	return nil
}

func deriveVMBootstrapWorkDir(bin string) string {
	if !strings.Contains(bin, "/") {
		return ""
	}
	// If vmbootstrap is passed as ../vmware-vm-bootstrap/bin/vmbootstrap,
	// run from repo root so it sees its own configs/*.sops.yaml files.
	dir := filepath.Dir(bin)
	if filepath.Base(dir) == "bin" {
		return filepath.Dir(dir)
	}
	return ""
}

func upsertStage2(path string, edit bool, draftPath string) error {
	cfg := stage2File{}
	toolVersions := loadToolVersionMetadata("configs/tool-versions.yaml")
	session := wizard.NewSession(
		path,
		draftPath,
		&cfg,
		nil,
		loadStage2DraftYAML,
		startStage2YAMLDraftHandler,
		cleanupStage2Draft,
	)
	if draftPath != "" {
		loaded, err := session.LoadDraft()
		if err != nil {
			return fmt.Errorf("load draft %s: %w", draftPath, err)
		}
		if !loaded {
			return fmt.Errorf("load draft %s: no data", draftPath)
		}
		fmt.Printf("\n\033[33m⚠ Resuming draft:\033[0m %s\n", filepath.Base(draftPath))
	} else if edit {
		if err := loadYAML(path, &cfg); err != nil {
			return err
		}
	} else {
		if err := loadYAML("configs/talos-bootstrap.example.yaml", &cfg); err != nil {
			return err
		}
		applySmartStage2Defaults(&cfg)
	}
	applyClusterNameSuggestionFromBootstrap(&cfg)

	fmt.Printf("\n%s: %s\n", map[bool]string{true: "Edit", false: "Create"}[edit], filepath.Base(path))
	fmt.Println(strings.Repeat("─", 40))
	session.Start()
	defer session.Stop()

	cfg.VM.Host = askString("VM host", cfg.VM.Host)
	cfg.VM.Port = askInt("VM SSH port", cfg.VM.Port)
	cfg.VM.User = askString("VM user", cfg.VM.User)
	cfg.VM.SSHPrivateKey = askString("VM SSH private key path", cfg.VM.SSHPrivateKey)
	cfg.VM.KnownHostsFile = askString("Known hosts file", cfg.VM.KnownHostsFile)
	cfg.VM.KnownHostsMode = normalizeKnownHostsModeOrDefault(cfg.VM.KnownHostsMode)
	if strings.TrimSpace(cfg.VM.SSHHostFingerprint) == "" {
		cfg.VM.SSHHostFingerprint = detectSSHHostFingerprint(cfg)
	}
	if askBool("Customize SSH trust settings (advanced)", false) {
		cfg.VM.KnownHostsMode = askKnownHostsMode("Known hosts mode", cfg.VM.KnownHostsMode)
		cfg.VM.SSHHostFingerprint = askString("SSH host fingerprint (SHA256:...)", cfg.VM.SSHHostFingerprint)
	}

	cfg.Hardening.Enabled = askBool("Apply OS security hardening", cfg.Hardening.Enabled)
	if cfg.Hardening.Enabled {
		customizeHardening := askBool("Customize hardening settings", false)
		if customizeHardening {
			cfg.Hardening.AllowPasswordSSH = askBool("Allow password SSH", cfg.Hardening.AllowPasswordSSH)
			cfg.Hardening.EnableUFW = askBool("Enable UFW", cfg.Hardening.EnableUFW)
			cfg.Hardening.AllowTCPPorts = askIntList("Allowed TCP ports", cfg.Hardening.AllowTCPPorts)
		}
	}

	cfg.TimeSync.Enabled = askBool("Ensure NTP time sync and verify the clock is synchronized", cfg.TimeSync.Enabled)

	cfg.Docker.Version = askString(versionPrompt("Docker version", toolVersions.Docker.LatestVersion, toolVersions.Docker.LatestReleaseDate), cfg.Docker.Version)

	clusterEnabled := askBool("Create a Talos-in-Docker Kubernetes cluster (no = Docker host only)", stage2ClusterEnabled(cfg))
	setStage2ClusterEnabled(&cfg, clusterEnabled)
	if clusterEnabled {
		cfg.Talos.Version = askString(versionPrompt("Talosctl version", toolVersions.Talosctl.LatestVersion, toolVersions.Talosctl.LatestReleaseDate), cfg.Talos.Version)
		checksum, err := resolveTalosChecksum(toolVersions, cfg.Talos.Version)
		if err != nil {
			return fmt.Errorf("resolve talosctl checksum for version %q: %w", cfg.Talos.Version, err)
		}
		cfg.Talos.SHA256Checksum = checksum
		applyClusterNameFallback(&cfg)

		previousClusterName := cfg.Cluster.Name
		cfg.Cluster.Name = askString("Cluster name", cfg.Cluster.Name)
		cfg.Cluster.StateDir = adjustStateDirForClusterName(cfg.Cluster.StateDir, previousClusterName, cfg.Cluster.Name)
		cfg.Cluster.StateDir = askString("Cluster state dir", cfg.Cluster.StateDir)
		cfg.Cluster.MountSrc = askString("Talos host path (mount source)", cfg.Cluster.MountSrc)
		cfg.Cluster.MountDst = askString("Talos node path (mount destination)", cfg.Cluster.MountDst)
	}

	if askBool("Customize connectivity/timeouts (advanced)", false) {
		cfg.Timeouts.SSHConnectSeconds = askInt("SSH connect seconds", cfg.Timeouts.SSHConnectSeconds)
		cfg.Timeouts.SSHRetries = askInt("SSH retries", cfg.Timeouts.SSHRetries)
		cfg.Timeouts.SSHRetryDelaySec = askInt("SSH retry delay seconds", cfg.Timeouts.SSHRetryDelaySec)
		cfg.Timeouts.TotalMinutes = askInt("Total timeout minutes", cfg.Timeouts.TotalMinutes)
	}

	if err := saveYAML(path, cfg); err != nil {
		return err
	}
	_ = session.Finalize()
	fmt.Printf("  \033[32m✓ Saved:\033[0m %s\n\n", path)
	return nil
}

func askString(msg, def string) string {
	def = sanitizeSuggestion(def)
	prompt := ""
	if def != "" {
		prompt = fmt.Sprintf("  %s [\033[36m%s\033[0m]: ", msg, def)
	} else {
		prompt = fmt.Sprintf("  %s: ", msg)
	}
	s := readLineClean(prompt)
	if s == "" {
		return def
	}
	return s
}

func askKnownHostsMode(msg, def string) string {
	options := []string{
		"strict",
		"prompt",
		"accept-new",
		"auto-refresh",
	}
	def = strings.ToLower(strings.TrimSpace(def))
	if def == "" {
		def = "strict"
	}
	for {
		fmt.Printf("  %s [\033[36m%s\033[0m]: ", msg, def)
		raw := readLineClean("")
		if raw == "" {
			return def
		}
		raw = strings.ToLower(strings.TrimSpace(raw))
		for _, opt := range options {
			if raw == opt {
				return raw
			}
		}
		fmt.Printf("  Invalid mode. Options: %s\n", strings.Join(options, ", "))
	}
}

func normalizeKnownHostsModeOrDefault(mode string) string {
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
		return "strict"
	}
}

func versionPrompt(base, latestVersion, latestReleaseDate string) string {
	latestVersion = strings.TrimSpace(latestVersion)
	latestReleaseDate = strings.TrimSpace(latestReleaseDate)
	if latestVersion == "" && latestReleaseDate == "" {
		return base
	}
	if latestVersion == "" {
		return fmt.Sprintf("%s (latest known release: %s)", base, latestReleaseDate)
	}
	if latestReleaseDate == "" {
		return fmt.Sprintf("%s (latest known: %s)", base, latestVersion)
	}
	return fmt.Sprintf("%s (latest known: %s, released %s)", base, latestVersion, latestReleaseDate)
}

func talosChecksumForVersion(meta toolVersionMetadata, version string) string {
	v := strings.TrimSpace(version)
	v = strings.TrimPrefix(v, "v")
	if v == "" || len(meta.Talosctl.ChecksumsLinuxAMD64) == 0 {
		return ""
	}
	if sum := strings.TrimSpace(meta.Talosctl.ChecksumsLinuxAMD64[v]); sum != "" {
		return sum
	}
	if sum := strings.TrimSpace(meta.Talosctl.ChecksumsLinuxAMD64["v"+v]); sum != "" {
		return sum
	}
	return ""
}

func resolveTalosChecksum(meta toolVersionMetadata, version string) (string, error) {
	v := strings.TrimSpace(strings.TrimPrefix(version, "v"))
	if v == "" {
		return "", errors.New("empty talos version")
	}

	if sum := talosChecksumForVersion(meta, v); sum != "" {
		return sum, nil
	}

	sum, err := fetchTalosChecksumFromRelease(v)
	if err != nil {
		return "", fmt.Errorf("checksum missing in configs/tool-versions.yaml and auto-fetch failed: %w", err)
	}
	return sum, nil
}

func fetchTalosChecksumFromRelease(version string) (string, error) {
	url := fmt.Sprintf(talosReleaseChecksumsURLFmt, version)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasSuffix(line, "talosctl-linux-amd64") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 1 && len(parts[0]) == 64 {
			return strings.ToLower(parts[0]), nil
		}
	}
	return "", errors.New("talosctl-linux-amd64 checksum not found in release checksums")
}

func askInt(msg string, def int) int {
	for {
		raw := readLineClean(fmt.Sprintf("  %s [\033[36m%d\033[0m]: ", msg, def))
		if raw == "" {
			return def
		}
		v, err := strconv.Atoi(raw)
		if err == nil {
			return v
		}
		fmt.Println("  Invalid number.")
	}
}

func askBool(msg string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for {
		s := strings.ToLower(readLineClean(fmt.Sprintf("  %s %s: ", msg, hint)))
		switch s {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		fmt.Println("  Please answer yes or no.")
	}
}

func askIntList(msg string, def []int) []int {
	vals := make([]string, 0, len(def))
	for _, v := range def {
		vals = append(vals, strconv.Itoa(v))
	}
	defVal := strings.Join(vals, ",")
	for {
		prompt := ""
		if defVal != "" {
			prompt = fmt.Sprintf("  %s (comma-separated) [\033[36m%s\033[0m]: ", msg, defVal)
		} else {
			prompt = fmt.Sprintf("  %s (comma-separated): ", msg)
		}
		raw := readLineClean(prompt)
		if raw == "" {
			raw = defVal
		}
		if raw == "" {
			return []int{}
		}
		parts := strings.Split(raw, ",")
		out := make([]int, 0, len(parts))
		valid := true
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			n, err := strconv.Atoi(p)
			if err != nil {
				valid = false
				break
			}
			out = append(out, n)
		}
		if valid {
			return out
		}
		fmt.Println("  Invalid list format. Use numbers separated by commas.")
	}
}

func readLineClean(prompt string) string {
	raw := readLineEditable(prompt)
	raw = ansiEscapeRE.ReplaceAllString(raw, "")
	raw = caretEscapeRE.ReplaceAllString(raw, "")
	raw = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
	return strings.TrimSpace(raw)
}

func readLineEditable(prompt string) string {
	fmt.Print(prompt)
	raw, _ := stdinReader.ReadString('\n')
	return raw
}

func loadYAML(path string, out any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(content, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func loadToolVersionMetadata(path string) toolVersionMetadata {
	var meta toolVersionMetadata
	_ = loadYAML(path, &meta)
	return meta
}

func saveYAML(path string, data any) error {
	content, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func sanitizeSuggestion(in string) string {
	in = strings.TrimSpace(os.ExpandEnv(in))
	if strings.Contains(in, "${") {
		return ""
	}
	return in
}

func applySmartStage2Defaults(cfg *stage2File) {
	cfg.VM.Host = sanitizeSuggestion(cfg.VM.Host)
	cfg.VM.User = sanitizeSuggestion(cfg.VM.User)
	cfg.VM.SSHPrivateKey = sanitizeSuggestion(cfg.VM.SSHPrivateKey)
	cfg.VM.KnownHostsFile = sanitizeSuggestion(cfg.VM.KnownHostsFile)

	// If bootstrap VM configs exist, prefer latest VM values as suggestions.
	latestVMPath, err := latestVMConfigPath()
	if err != nil || latestVMPath == "" {
		return
	}
	bootstrapVM, err := loadVMBootstrapConfig(latestVMPath)
	if err != nil {
		return
	}
	if strings.TrimSpace(bootstrapVM.VM.IPAddress) != "" {
		cfg.VM.Host = bootstrapVM.VM.IPAddress
	}
	if normalized := normalizeClusterName(bootstrapVM.VM.Name); normalized != "" && strings.TrimSpace(cfg.Cluster.Name) == "devvm" {
		cfg.Cluster.Name = normalized
	}
	if strings.TrimSpace(bootstrapVM.VM.Username) != "" {
		cfg.VM.User = bootstrapVM.VM.Username
	}
	if strings.TrimSpace(bootstrapVM.VM.SSHKeyPath) != "" {
		cfg.VM.SSHPrivateKey = suggestSSHPrivateKeyPath(bootstrapVM.VM.SSHKeyPath)
	}
	if bootstrapVM.VM.SSHPort > 0 {
		cfg.VM.Port = bootstrapVM.VM.SSHPort
	}
	applyClusterNameFallback(cfg)
}

// stage2ClusterEnabled mirrors config.ClusterConfig.IsEnabled for the wizard's
// file model: an omitted cluster.enabled means enabled.
func stage2ClusterEnabled(cfg stage2File) bool {
	return cfg.Cluster.Enabled == nil || *cfg.Cluster.Enabled
}

// setStage2ClusterEnabled writes cluster.enabled only when it is false, so a
// config that keeps the default stays byte-compatible with older versions.
func setStage2ClusterEnabled(cfg *stage2File, enabled bool) {
	if enabled {
		cfg.Cluster.Enabled = nil
		return
	}
	disabled := false
	cfg.Cluster.Enabled = &disabled
}

func normalizeClusterName(in string) string {
	s := strings.ToLower(strings.TrimSpace(in))
	s = clusterNameUnsafeRE.ReplaceAllString(s, "-")
	s = hyphenCollapseRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

func applyClusterNameFallback(cfg *stage2File) {
	if strings.TrimSpace(cfg.Cluster.Name) == "" {
		if normalized := normalizeClusterName(cfg.VM.Host); normalized != "" {
			cfg.Cluster.Name = normalized
		}
	}
	if strings.TrimSpace(cfg.Cluster.Name) == "" {
		cfg.Cluster.Name = "devvm"
	}
}

func applyClusterNameSuggestionFromBootstrap(cfg *stage2File) {
	current := strings.TrimSpace(cfg.Cluster.Name)
	if current != "" && current != "devvm" {
		return
	}
	latestVMPath, err := latestVMConfigPath()
	if err != nil || latestVMPath == "" {
		return
	}
	bootstrapVM, err := loadVMBootstrapConfig(latestVMPath)
	if err != nil {
		return
	}
	if normalized := normalizeClusterName(bootstrapVM.VM.Name); normalized != "" {
		cfg.Cluster.Name = normalized
	}
}

func suggestSSHPrivateKeyPath(sourcePath string) string {
	p := strings.TrimSpace(os.ExpandEnv(sourcePath))
	if p == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(p), ".pub") {
		priv := strings.TrimSuffix(p, ".pub")
		if st, err := os.Stat(priv); err == nil && !st.IsDir() {
			return priv
		}
	}
	return p
}

func adjustStateDirForClusterName(stateDir, previousClusterName, newClusterName string) string {
	sd := strings.TrimSpace(stateDir)
	oldName := strings.TrimSpace(previousClusterName)
	newName := strings.TrimSpace(newClusterName)
	if sd == "" || newName == "" || oldName == "" || oldName == newName {
		return stateDir
	}
	oldSuffix := "/clusters/" + oldName
	if strings.HasSuffix(sd, oldSuffix) {
		return strings.TrimSuffix(sd, oldSuffix) + "/clusters/" + newName
	}
	return stateDir
}

func latestVMConfigPath() (string, error) {
	paths, err := filepath.Glob("configs/vm.*.sops.yaml")
	if err != nil || len(paths) == 0 {
		return "", err
	}
	sort.Slice(paths, func(i, j int) bool {
		ii, errI := os.Stat(paths[i])
		jj, errJ := os.Stat(paths[j])
		if errI != nil || errJ != nil {
			return paths[i] > paths[j]
		}
		return ii.ModTime().After(jj.ModTime())
	})
	return paths[0], nil
}

func loadVMBootstrapConfig(path string) (vmBootstrapFile, error) {
	if _, err := exec.LookPath("sops"); err != nil {
		return vmBootstrapFile{}, fmt.Errorf("sops not found")
	}
	cmd := exec.Command("sops", "--decrypt", "--output-type", "yaml", path)
	out, err := cmd.Output()
	if err != nil {
		return vmBootstrapFile{}, fmt.Errorf("decrypt %s: %w", path, err)
	}
	var vm vmBootstrapFile
	if err := yaml.Unmarshal(out, &vm); err != nil {
		return vmBootstrapFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return vm, nil
}

func detectSSHHostFingerprint(cfg stage2File) string {
	if fp := latestBootstrapResultFingerprint(); fp != "" {
		return fp
	}
	host := strings.TrimSpace(cfg.VM.Host)
	if host == "" {
		return ""
	}
	port := cfg.VM.Port
	if port <= 0 {
		port = 22
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	fp, err := sshutil.ScanHostKeyFingerprint(ctx, host, port)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(fp)
}

func latestBootstrapResultFingerprint() string {
	patterns := []string{
		filepath.Join("tmp", "bootstrap-result*.yaml"),
		filepath.Join("tmp", "bootstrap-result*.yml"),
		filepath.Join("tmp", "bootstrap-result*.json"),
	}
	candidates := make([]string, 0, 6)
	for _, p := range patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			continue
		}
		candidates = append(candidates, matches...)
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		ii, errI := os.Stat(candidates[i])
		jj, errJ := os.Stat(candidates[j])
		if errI != nil || errJ != nil {
			return candidates[i] > candidates[j]
		}
		return ii.ModTime().After(jj.ModTime())
	})
	for _, p := range candidates {
		res, err := vmconfig.LoadBootstrapResult(p)
		if err != nil {
			continue
		}
		if fp := strings.TrimSpace(res.SSHHostFingerprint); fp != "" {
			return fp
		}
	}
	return ""
}

func cleanupStage2Draft(targetPath string) error {
	for _, p := range listStage2Drafts(targetPath) {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func stage2DraftPath(targetPath string) string {
	base := filepath.Base(targetPath)
	return filepath.Join("tmp", fmt.Sprintf("%s.draft.yaml", base))
}

func listStage2Drafts(targetPath string) []string {
	base := filepath.Base(targetPath)
	pattern := filepath.Join("tmp", fmt.Sprintf("%s.draft*.yaml", base))
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Slice(matches, func(i, j int) bool {
		ii, errI := os.Stat(matches[i])
		jj, errJ := os.Stat(matches[j])
		if errI != nil || errJ != nil {
			return matches[i] > matches[j]
		}
		return ii.ModTime().After(jj.ModTime())
	})
	return matches
}

func loadStage2DraftYAML(draftPath string, out any) (bool, error) {
	draftPath = strings.TrimSpace(draftPath)
	if draftPath == "" {
		return false, nil
	}
	cfg, ok := out.(*stage2File)
	if !ok || cfg == nil {
		return false, fmt.Errorf("invalid draft target type")
	}
	if err := loadYAML(draftPath, cfg); err != nil {
		return false, err
	}
	return true, nil
}

func startStage2YAMLDraftHandler(targetPath, draftPath string, state any, isEmpty func() bool) func() {
	return startStage2DraftInterruptHandler(targetPath, draftPath, func() ([]byte, bool) {
		cfg, ok := state.(*stage2File)
		if !ok || cfg == nil {
			return nil, false
		}
		if isEmpty != nil && isEmpty() {
			return nil, false
		}
		data, err := yaml.Marshal(cfg)
		if err != nil {
			return nil, false
		}
		return data, true
	})
}

func startStage2DraftInterruptHandler(targetPath, draftPath string, dataFn func() ([]byte, bool)) func() {
	localSigCh := make(chan os.Signal, 1)
	signal.Notify(localSigCh, os.Interrupt)
	go func() {
		<-localSigCh
		data, ok := dataFn()
		if ok {
			if dp, err := writeStage2Draft(targetPath, draftPath, data); err == nil {
				fmt.Printf("\n\033[33m⚠ Interrupted\033[0m\n")
				fmt.Printf("  Draft saved: %s\n", dp)
			}
		}
		fmt.Println("Cancelled.")
		wizard.RestoreTTY()
		os.Exit(0)
	}()
	return func() {
		signal.Stop(localSigCh)
	}
}

func writeStage2Draft(targetPath, draftPath string, data []byte) (string, error) {
	if err := os.MkdirAll("tmp", 0o700); err != nil {
		return "", err
	}
	if strings.TrimSpace(draftPath) == "" {
		draftPath = stage2DraftPath(targetPath)
	}
	if err := os.WriteFile(draftPath, data, 0o600); err != nil {
		return "", err
	}
	return draftPath, nil
}
