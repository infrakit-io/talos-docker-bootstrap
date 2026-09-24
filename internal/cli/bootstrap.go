package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/infrakit-io/talos-docker-bootstrap/internal/bootstrap"
	"github.com/infrakit-io/talos-docker-bootstrap/internal/config"
	"github.com/spf13/cobra"
)

func newBootstrapCmd() *cobra.Command {
	var (
		configPath string
		dryRun     bool
		jsonOut    bool
	)

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Run Talos bootstrap workflow on an existing Ubuntu VM",
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger, err := newLogger(logFormat, logLevel)
			if err != nil {
				return err
			}
			warnPinnedAssetDrift()

			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			restorePrompt := maybeSetKnownHostsPrompt(cfg, !jsonOut && strings.EqualFold(logFormat, "text"))
			defer restorePrompt()

			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeouts.TotalDuration())
			defer cancel()

			human := !jsonOut && strings.EqualFold(logFormat, "text")
			res, err := bootstrap.Run(ctx, logger, cfg, bootstrap.Options{
				DryRun:        dryRun,
				HumanProgress: human,
			})
			if err != nil {
				if jsonOut {
					return printJSON(res)
				}
				return err
			}

			if jsonOut {
				return printJSON(res)
			}

			if human {
				fmt.Printf("\n\033[32m✓ bootstrap completed\033[0m\n")
				fmt.Printf("  VM:      \033[36m%s\033[0m\n", cfg.VM.Host)
				fmt.Printf("  Cluster: \033[36m%s\033[0m\n", clusterLabel(cfg))
				fmt.Printf("  Total:   \033[36m%s\033[0m\n", time.Since(res.StartedAt).Truncate(time.Millisecond))
			} else {
				logger.Info("bootstrap completed",
					"vm", cfg.VM.Host,
					"cluster", clusterLabel(cfg),
					"duration", time.Since(res.StartedAt).String(),
				)
			}
			return nil
		},
	}

	defCfg := defaultConfigPath()
	cmd.Flags().StringVar(&configPath, "config", defCfg, "Path to YAML config file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and print planned operations without changes")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print machine-readable result JSON")
	if defCfg == "" {
		_ = cmd.MarkFlagRequired("config")
	}

	return cmd
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode json output: %w", err)
	}
	return nil
}
