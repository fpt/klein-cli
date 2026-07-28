package agentserver

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/fpt/klein-cli/internal/config"
)

// codexConfigArgs renders klein's codex config blocks as `-c key=value`
// overrides to append to the codex launch line.
//
// This is the only route for these settings. codex's sandbox_workspace_write
// and shell_environment_policy tables have no thread-scoped equivalent —
// thread/start's `sandbox` is the bare mode name — so unlike sandbox_mode and
// approval_policy, klein cannot state them per-thread and must configure the
// process instead. `-c` reconfigures that one process, leaving
// ~/.codex/config.toml and ordinary `codex` runs untouched.
//
// Only fields the user actually set are emitted. Passing a zero value the user
// never asked for would override whatever their own config.toml says, which is
// the opposite of what an omitted setting should mean.
func codexConfigArgs(c config.CodexSettings) []string {
	var args []string
	add := func(key, value string) {
		args = append(args, flagConfig, key+"="+value)
	}

	sw := c.SandboxWorkspaceWrite
	addBool(add, "sandbox_workspace_write.network_access", sw.NetworkAccess)
	addBool(add, "sandbox_workspace_write.exclude_tmpdir_env_var", sw.ExcludeTmpdirEnvVar)
	addBool(add, "sandbox_workspace_write.exclude_slash_tmp", sw.ExcludeSlashTmp)
	addList(add, "sandbox_workspace_write.writable_roots", sw.WritableRoots)

	sep := c.ShellEnvironmentPolicy
	if sep.Inherit != "" {
		add("shell_environment_policy.inherit", tomlValue(sep.Inherit))
	}
	addBool(add, "shell_environment_policy.ignore_default_excludes", sep.IgnoreDefaultExcludes)
	addList(add, "shell_environment_policy.exclude", sep.Exclude)
	addList(add, "shell_environment_policy.include_only", sep.IncludeOnly)
	// Sorted: a map's iteration order is random, and an argument list that
	// reshuffles between launches is a needless source of irreproducibility.
	for _, name := range slices.Sorted(maps.Keys(sep.Set)) {
		add("shell_environment_policy.set."+name, tomlValue(sep.Set[name]))
	}

	return args
}

// flagConfig is codex's config-override flag: `-c key=value`, value parsed as
// TOML (and, failing that, taken as a literal string).
const flagConfig = "-c"

func addBool(add func(key, value string), key string, v *bool) {
	if v != nil {
		add(key, strconv.FormatBool(*v))
	}
}

func addList(add func(key, value string), key string, values []string) {
	if len(values) > 0 {
		add(key, tomlValue(values))
	}
}

// tomlValue renders a string or []string as a TOML value.
//
// JSON encoding does the escaping. That is not a shortcut: for these two shapes
// the encodings coincide. Go emits exactly the escapes a TOML basic string
// accepts (\" \\ \b \f \n \r \t and \uXXXX for the rest, never JSON's \/), and
// a JSON array of strings is spelled the same as a TOML one. So a value
// containing a quote, a backslash, or a newline survives intact rather than
// producing an override codex would silently reinterpret as a literal.
func tomlValue(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		// Unreachable for string/[]string, which never fail to marshal.
		return `""`
	}
	return string(encoded)
}

// validateCodexConfig rejects a shell_environment_policy.inherit codex would
// refuse. Worth catching here: codex validates it at startup and exits with
// "unknown variant", which surfaces as a spawn failure naming neither the
// setting nor the file it came from.
func validateCodexConfig(c config.CodexSettings) error {
	mode := c.ShellEnvironmentPolicy.Inherit
	if mode != "" && !slices.Contains(config.ShellEnvironmentInheritModes, mode) {
		return fmt.Errorf(
			"codex.shell_environment_policy.inherit is %q, must be one of %s",
			mode, strings.Join(config.ShellEnvironmentInheritModes, ", "))
	}
	return nil
}
