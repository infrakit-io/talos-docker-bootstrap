package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/infrakit-io/talos-docker-bootstrap/internal/bootstrap"
	"github.com/infrakit-io/talos-docker-bootstrap/internal/config"
	"gopkg.in/yaml.v3"
)

func TestExplainClusterOpError(t *testing.T) {
	cfg := config.Config{
		VM: config.VMConfig{Host: "1.2.3.4", Port: 22, User: "dev"},
	}

	err := explainClusterOpError(errors.New("No Talos-in-Docker cluster found on remote VM."), cfg)
	ue, ok := err.(*userError)
	if !ok || ue.Hint() == "" {
		t.Fatalf("expected userError with hint for missing cluster")
	}

	err = explainClusterOpError(errors.New("ssh run command failed: exit status 255"), cfg)
	ue, ok = err.(*userError)
	if !ok || ue.Hint() == "" {
		t.Fatalf("expected userError with hint for ssh failure")
	}
}

func TestExplainClusterOpErrorClusterDisabled(t *testing.T) {
	cfg := config.Config{VM: config.VMConfig{Host: "1.2.3.4", Port: 22, User: "dev"}}
	err := explainClusterOpError(fmt.Errorf("wrap: %w", bootstrap.ErrClusterDisabled), cfg)
	ue, ok := err.(*userError)
	if !ok || !strings.Contains(ue.Error(), "cluster.enabled: false") || ue.Hint() == "" {
		t.Fatalf("expected userError naming cluster.enabled, got %#v", err)
	}
}

func TestClusterLabel(t *testing.T) {
	cfg := config.Config{Cluster: config.ClusterConfig{Name: "devvm"}}
	if got := clusterLabel(cfg); got != "devvm" {
		t.Fatalf("expected cluster name, got %q", got)
	}
	off := false
	cfg.Cluster.Enabled = &off
	if got := clusterLabel(cfg); !strings.Contains(got, "disabled") {
		t.Fatalf("expected disabled label, got %q", got)
	}
}

func TestStage2ClusterEnabledRoundTrip(t *testing.T) {
	var cfg stage2File
	if !stage2ClusterEnabled(cfg) {
		t.Fatalf("omitted cluster.enabled must read as enabled")
	}
	setStage2ClusterEnabled(&cfg, false)
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), "enabled: false") {
		t.Fatalf("disabled cluster must be written explicitly:\n%s", out)
	}
	if stage2ClusterEnabled(cfg) {
		t.Fatalf("expected disabled after set false")
	}
	setStage2ClusterEnabled(&cfg, true)
	out, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe struct {
		Cluster map[string]any `yaml:"cluster"`
	}
	if err := yaml.Unmarshal(out, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := probe.Cluster["enabled"]; present {
		t.Fatalf("enabled cluster must omit the field to stay compatible:\n%s", out)
	}
}
