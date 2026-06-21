package researcher

import (
	"os"
	"testing"
)

// TestLoadDefaults_SeedsConfigWhenAbsent confirms the embedded default
// config.yaml is dropped in place on first run.
func TestLoadDefaults_SeedsConfigWhenAbsent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	d, err := LoadDefaults()
	if err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	body, err := os.ReadFile(d.ConfigPath)
	if err != nil {
		t.Fatalf("config not seeded: %v", err)
	}
	if len(body) == 0 {
		t.Error("seeded config is empty")
	}
}
