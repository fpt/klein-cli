package skill

import "embed"

//go:embed skills/*/SKILL.md
var embeddedSkills embed.FS

// Roles are embedded separately because they are a different kind of thing: a
// role is the startup prompt a session opens with, a skill is a capability
// reached from inside one.
//
//go:embed roles/*/ROLE.md
var embeddedRoles embed.FS
