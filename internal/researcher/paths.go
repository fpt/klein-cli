// Package researcher provides defaults and the public surface used by the
// Researcher* tools and the market-narratives skill. The subpackages
// (config, source, narrative, store, pipeline) implement the RSS/Atom
// fetch + narrative clustering pipeline; the crawler subpackage adds
// primary-source ingestion for HTML and PDF.
package researcher

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fpt/klein-cli/internal/researcher/embed"
)

// Defaults returns the canonical klein-managed directory layout. All EH data
// lives under ~/.klein/researcher/ so it survives across project roots and
// doesn't pollute the user's working directory.
type Defaults struct {
	BaseDir    string // ~/.klein/researcher
	DataDir    string // ~/.klein/researcher/data
	ReportsDir string // ~/.klein/researcher/reports
	ConfigPath string // ~/.klein/researcher/config.yaml
}

// LoadDefaults computes the standard paths and ensures the directories exist.
// If config.yaml doesn't exist, it is seeded from the embedded default. This
// is idempotent — calling repeatedly is fine.
func LoadDefaults() (Defaults, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Defaults{}, fmt.Errorf("resolving home directory: %w", err)
	}
	base := filepath.Join(home, ".klein", "researcher")
	d := Defaults{
		BaseDir:    base,
		DataDir:    filepath.Join(base, "data"),
		ReportsDir: filepath.Join(base, "reports"),
		ConfigPath: filepath.Join(base, "config.yaml"),
	}
	for _, dir := range []string{d.DataDir, d.ReportsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return d, fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	if _, err := os.Stat(d.ConfigPath); os.IsNotExist(err) {
		if err := os.WriteFile(d.ConfigPath, embed.DefaultConfigYAML, 0o644); err != nil {
			return d, fmt.Errorf("seeding %s: %w", d.ConfigPath, err)
		}
	}
	return d, nil
}
