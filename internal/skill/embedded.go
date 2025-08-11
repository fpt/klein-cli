package skill

import "embed"

//go:embed skills/*/SKILL.md
var embeddedSkills embed.FS
