package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`sources:
  - name: markets
    type: rss
    intake: news
    role: signal
    trust_tier: news
    url: https://example.com/rss.xml
  - name: world
    role: outcome
    url: https://example.com/world.xml
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(cfg.Sources))
	}
	if cfg.Sources[1].Type != "rss" {
		t.Fatalf("default type = %q, want rss", cfg.Sources[1].Type)
	}
	if cfg.Sources[1].TrustTier != "outcome" {
		t.Fatalf("outcome trust tier = %q, want outcome", cfg.Sources[1].TrustTier)
	}
	if cfg.Sources[0].Weight != 0.45 || cfg.Sources[1].Weight != 0.65 {
		t.Fatalf("weights = %f/%f, want 0.45/0.65", cfg.Sources[0].Weight, cfg.Sources[1].Weight)
	}
}
