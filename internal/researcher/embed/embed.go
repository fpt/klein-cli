// Package embed packages the default researcher configuration so the
// Researcher tools work out-of-the-box without the user authoring a YAML
// file. On first use, the tool copies this into
// ~/.klein/researcher/config.yaml where the user can then customise it.
package embed

import _ "embed"

// DefaultConfigYAML is the seed config written to
// ~/.klein/researcher/config.yaml on first run.
//
//go:embed default_config.yaml
var DefaultConfigYAML []byte
