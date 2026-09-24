package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/infrakit-io/talos-docker-bootstrap/internal/bootstrap"
	"github.com/infrakit-io/talos-docker-bootstrap/internal/config"
	"github.com/spf13/cobra"
)

func newClusterStatusCmd() *cobra.Command {
	var (
		configPath string
	)

	cmd := &cobra.Command{
		Use:   "cluster-status",
		Short: "Show Talos-in-Docker cluster status from remote VM",
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger, err := newLogger(logFormat, logLevel)
			if err != nil {
				return err
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			restorePrompt := maybeSetKnownHostsPrompt(cfg, strings.EqualFold(logFormat, "text"))
			defer restorePrompt()
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeouts.TotalDuration())
			defer cancel()

			out, err := bootstrap.ClusterStatus(ctx, logger, cfg)
			if err != nil {
				return explainClusterOpError(err, cfg)
			}
			fmt.Println(out)
			return nil
		},
	}

	defCfg := defaultConfigPath()
	cmd.Flags().StringVar(&configPath, "config", defCfg, "Path to YAML config file")
	if defCfg == "" {
		_ = cmd.MarkFlagRequired("config")
	}
	return cmd
}

func newKubeconfigExportCmd() *cobra.Command {
	var (
		configPath string
		outPath    string
	)

	cmd := &cobra.Command{
		Use:   "kubeconfig-export",
		Short: "Export kubeconfig from remote VM cluster state to local file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger, err := newLogger(logFormat, logLevel)
			if err != nil {
				return err
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			restorePrompt := maybeSetKnownHostsPrompt(cfg, strings.EqualFold(logFormat, "text"))
			defer restorePrompt()
			if outPath == "" {
				return &userError{
					msg:  "--out is required",
					hint: "Run: make kubeconfig-export OUT=build/devvm/kubeconfig",
				}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeouts.TotalDuration())
			defer cancel()
			kubeconfig, err := bootstrap.KubeconfigExport(ctx, logger, cfg)
			if err != nil {
				return explainClusterOpError(err, cfg)
			}

			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return fmt.Errorf("create output directory: %w", err)
			}
			if err := os.WriteFile(outPath, []byte(kubeconfig), 0o600); err != nil {
				return fmt.Errorf("write kubeconfig: %w", err)
			}
			fmt.Printf("Kubeconfig exported: %s\n", outPath)
			return nil
		},
	}

	defCfg := defaultConfigPath()
	cmd.Flags().StringVar(&configPath, "config", defCfg, "Path to YAML config file")
	cmd.Flags().StringVar(&outPath, "out", "", "Local output path for kubeconfig")
	if defCfg == "" {
		_ = cmd.MarkFlagRequired("config")
	}
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func newMountCheckCmd() *cobra.Command {
	var (
		configPath string
	)

	cmd := &cobra.Command{
		Use:   "mount-check",
		Short: "Verify mount path is visible inside Talos node",
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger, err := newLogger(logFormat, logLevel)
			if err != nil {
				return err
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			restorePrompt := maybeSetKnownHostsPrompt(cfg, strings.EqualFold(logFormat, "text"))
			defer restorePrompt()

			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeouts.TotalDuration())
			defer cancel()
			if err := bootstrap.MountCheck(ctx, logger, cfg); err != nil {
				return explainClusterOpError(err, cfg)
			}
			return nil
		},
	}

	defCfg := defaultConfigPath()
	cmd.Flags().StringVar(&configPath, "config", defCfg, "Path to YAML config file")
	if defCfg == "" {
		_ = cmd.MarkFlagRequired("config")
	}
	return cmd
}

func explainClusterOpError(err error, cfg config.Config) error {
	if errors.Is(err, bootstrap.ErrClusterDisabled) {
		return &userError{
			msg:  "this config has no Talos cluster (cluster.enabled: false)",
			hint: "Set cluster.enabled: true (with talos.* and cluster.* filled) and run: make talos-bootstrap",
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "No Talos-in-Docker cluster found on remote VM.") {
		return &userError{
			msg:  fmt.Sprintf("no remote Talos cluster found on %s", cfg.VM.Host),
			hint: "Run: make talos-bootstrap",
		}
	}
	if strings.Contains(msg, "exit status 255") {
		return &userError{
			msg:  fmt.Sprintf("ssh connection to %s@%s:%d failed", cfg.VM.User, cfg.VM.Host, cfg.VM.Port),
			hint: "Verify VM reachability/credentials, then run: make vm-deploy (if VM missing) or make talos-bootstrap",
		}
	}
	return err
}

// clusterLabel names the cluster for human/log summaries, or says there is
// none when the config switches the Talos steps off.
func clusterLabel(cfg config.Config) string {
	if !cfg.Cluster.IsEnabled() {
		return "disabled (docker host only)"
	}
	return cfg.Cluster.Name
}
