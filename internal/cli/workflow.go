package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	wizard "github.com/infrakit-io/cli-wizard-core"
	"github.com/infrakit-io/talos-docker-bootstrap/internal/config"
	vmtool "github.com/infrakit-io/talos-docker-bootstrap/internal/tooling/vmbootstrap"
	"github.com/infrakit-io/talos-docker-bootstrap/internal/workflow"
	"github.com/spf13/cobra"
)

func newProvisionAndBootstrapCmd() *cobra.Command {
	var (
		configPath        string
		bootstrapPath     string
		vmConfigPath      string
		dryRun            bool
		jsonOut           bool
		vmbootstrapBin    string
		vmbootstrapRepo   string
		vmbootstrapBuild  bool
		vmbootstrapNotify bool
	)

	cmd := &cobra.Command{
		Use:   "provision-and-bootstrap",
		Short: "Run orchestrated workflow using bootstrap result + Talos bootstrap",
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger, err := newLogger(logFormat, logLevel)
			if err != nil {
				return err
			}
			warnPinnedAssetDrift()
			human := !jsonOut && strings.EqualFold(logFormat, "text")
			progress := newWorkflowProgress(3, human)
			progress.start("bootstrap-input", "Acquire VM bootstrap result")

			stage2Cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			restorePrompt := maybeSetKnownHostsPrompt(stage2Cfg, !jsonOut && strings.EqualFold(logFormat, "text"))
			defer restorePrompt()
			bootstrapFromRun := false
			if strings.TrimSpace(bootstrapPath) == "" && strings.TrimSpace(vmConfigPath) == "" {
				path, err := runVMBootstrapAndCaptureResult(vmbootstrapBin, vmbootstrapRepo, vmbootstrapBuild, vmbootstrapNotify)
				if err != nil {
					if errors.Is(err, errVMBootstrapCancelled) {
						return nil
					}
					return err
				}
				bootstrapPath = path
				bootstrapFromRun = true
			}
			bootstrapResult, err := resolveBootstrapResult(bootstrapPath, vmConfigPath)
			if err != nil {
				return err
			}
			progress.done("bootstrap result ready")

			progress.start("ssh-identity", "Stabilize SSH host trust for fresh VM")
			if bootstrapFromRun {
				changed, err := workflow.RefreshBootstrapFingerprint(bootstrapPath, &bootstrapResult)
				if err != nil {
					return err
				}
				if changed {
					if human {
						logger.Debug("bootstrap fingerprint refreshed", "path", bootstrapPath)
					} else {
						logger.Info("bootstrap fingerprint refreshed", "path", bootstrapPath)
					}
				}
				// After identity stabilization, enforce strict verification for Talos bootstrap.
				if !strings.EqualFold(strings.TrimSpace(stage2Cfg.VM.KnownHostsMode), "strict") {
					stage2Cfg.VM.KnownHostsMode = "strict"
					if human {
						logger.Debug("known_hosts mode set to strict for talos bootstrap after fingerprint stabilization")
					} else {
						logger.Info("known_hosts mode set to strict for talos bootstrap after fingerprint stabilization")
					}
				}
			}
			progress.done("ssh trust state ready")

			ctx, cancel := context.WithTimeout(cmd.Context(), stage2Cfg.Timeouts.TotalDuration())
			defer cancel()

			progress.start("talos-bootstrap", "Run Talos bootstrap on target VM")
			res, err := workflow.ProvisionAndBootstrap(ctx, logger, stage2Cfg, bootstrapResult, workflow.ProvisionAndBootstrapOptions{
				DryRun:        dryRun,
				HumanProgress: human,
			})
			if err != nil {
				if jsonOut {
					return printJSON(res)
				}
				return err
			}
			progress.done("talos bootstrap completed")

			if jsonOut {
				return printJSON(res)
			}

			if human {
				fmt.Printf("\n\033[32m✓ workflow completed\033[0m\n")
				fmt.Printf("  VM:      \033[36m%s\033[0m\n", stage2Cfg.VM.Host)
				fmt.Printf("  Cluster: \033[36m%s\033[0m\n", clusterLabel(stage2Cfg))
				fmt.Printf("  Total:   \033[36m%s\033[0m\n", time.Since(res.StartedAt).Truncate(time.Millisecond))
			} else {
				logger.Info("workflow completed",
					"vm", stage2Cfg.VM.Host,
					"cluster", clusterLabel(stage2Cfg),
					"duration", time.Since(res.StartedAt).String(),
				)
			}
			return nil
		},
	}

	defCfg := defaultConfigPath()
	cmd.Flags().StringVar(&configPath, "config", defCfg, "Path to Talos bootstrap YAML config file")
	cmd.Flags().StringVar(&bootstrapPath, "bootstrap-result", "", "Path to bootstrap result JSON/YAML")
	cmd.Flags().StringVar(&vmConfigPath, "vm-config", "", "Path to vmware-vm-bootstrap VM config (SOPS/cleartext)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and print planned operations without changes")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print machine-readable result JSON")
	cmd.Flags().StringVar(&vmbootstrapBin, "vmbootstrap-bin", "bin/vmbootstrap", "vmware-vm-bootstrap CLI binary")
	cmd.Flags().StringVar(&vmbootstrapRepo, "vmbootstrap-repo", "../vmware-vm-bootstrap", "Path to vmware-vm-bootstrap repository (used only with --vmbootstrap-auto-build)")
	cmd.Flags().BoolVar(&vmbootstrapBuild, "vmbootstrap-auto-build", false, "Auto-build vmbootstrap from --vmbootstrap-repo when binary is missing")
	cmd.Flags().BoolVar(&vmbootstrapNotify, "vmbootstrap-update-notify", true, "Show update notice when a newer vmbootstrap module version is available")
	if defCfg == "" {
		_ = cmd.MarkFlagRequired("config")
	}
	// bootstrap-result or vm-config is required; validated at runtime.

	return cmd
}

type workflowProgress struct {
	total   int
	current int
	enabled bool
}

func newWorkflowProgress(total int, enabled bool) *workflowProgress {
	return &workflowProgress{total: total, enabled: enabled}
}

func (p *workflowProgress) start(step, desc string) {
	if !p.enabled {
		return
	}
	p.current++
	pct := (p.current - 1) * 100 / p.total
	fmt.Printf("\n\033[35m[workflow %d/%d]\033[0m \033[1m%s\033[0m \033[90m(%d%%)\033[0m\n", p.current, p.total, step, pct)
	fmt.Printf("  \033[90m%s\033[0m\n", desc)
}

func (p *workflowProgress) done(msg string) {
	if !p.enabled {
		return
	}
	pct := p.current * 100 / p.total
	fmt.Printf("  \033[32m✓ %s\033[0m \033[90m[%d/%d %d%%]\033[0m\n", msg, p.current, p.total, pct)
}

func resolveBootstrapResult(bootstrapPath, vmConfigPath string) (workflow.BootstrapResult, error) {
	if strings.TrimSpace(bootstrapPath) != "" && strings.TrimSpace(vmConfigPath) != "" {
		return workflow.BootstrapResult{}, fmt.Errorf("use either --bootstrap-result or --vm-config, not both")
	}
	if strings.TrimSpace(vmConfigPath) != "" {
		return workflow.LoadBootstrapResultFromVMConfig(vmConfigPath)
	}
	if strings.TrimSpace(bootstrapPath) == "" {
		return workflow.BootstrapResult{}, fmt.Errorf("bootstrap result is required (set --bootstrap-result or --vm-config)")
	}
	return workflow.LoadBootstrapResult(bootstrapPath)
}

var (
	bootstrapResultSavedRE = regexp.MustCompile(`(?i)Bootstrap result saved:\s*(\S+)`)
	cancelledOutputRE      = regexp.MustCompile(`(?im)^Cancelled\.`)
	selectorOutputRE       = regexp.MustCompile(`(?im)Select VM config to bootstrap`)
)

var errVMBootstrapCancelled = wizard.ErrInterrupted

func runVMBootstrapAndCaptureResult(bin, repo string, autoBuild, notify bool) (string, error) {
	if notify {
		current, latest, hasUpdate, err := vmtool.IsUpdateAvailable()
		if err == nil && hasUpdate {
			fmt.Printf("\n\033[1;33m⚠ VMBOOTSTRAP UPDATE AVAILABLE: %s -> %s\033[0m\n", current, latest)
			fmt.Println("  Run: make update-vmbootstrap-pin install-vmbootstrap")
			fmt.Println()
		}
	}

	resolved, err := vmtool.ResolveBinary(vmtool.ResolveOptions{
		Bin:       bin,
		Repo:      repo,
		AutoBuild: autoBuild,
	})
	if err != nil {
		return "", fmt.Errorf("resolve vmbootstrap binary: %w", err)
	}

	run := exec.Command(resolved, "run")
	if workdir := deriveVMBootstrapWorkDir(resolved); workdir != "" {
		run.Dir = workdir
	}
	run.Stdin = os.Stdin

	var stdout bytes.Buffer
	run.Stdout = io.MultiWriter(os.Stdout, &stdout)
	run.Stderr = os.Stderr

	if err := run.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 130 {
			return "", errVMBootstrapCancelled
		}
		return "", fmt.Errorf("run vmbootstrap deployment: %w", err)
	}

	output := stdout.String()
	if cancelledOutputRE.MatchString(output) {
		return "", errVMBootstrapCancelled
	}
	matches := bootstrapResultSavedRE.FindStringSubmatch(output)
	if len(matches) < 2 {
		if selectorOutputRE.MatchString(output) {
			return "", errVMBootstrapCancelled
		}
		return "", fmt.Errorf("vmbootstrap finished but bootstrap result path was not found in output")
	}
	return matches[1], nil
}
