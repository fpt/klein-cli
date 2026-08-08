package agentserver

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// kleinModule is the import prefix that must not appear in the client half.
const kleinModule = "github.com/fpt/klein-cli/"

// kleinSideFiles are the files that stay behind when the client is extracted to
// pkg/agentserver. They are klein's half of the boundary — the adapters that
// convert klein's types into the client's interfaces, the settings plumbing, and
// the allowlist policy — and they are expected to import klein freely.
//
// Everything else in this package is the client, and must import nothing of
// klein's at all. The list names the exceptions rather than the rule so a new
// file is held to the rule by default: adding a klein import to it fails this
// test until someone decides, in writing, that the file belongs on this side.
var kleinSideFiles = map[string]bool{
	// The adapters themselves — the whole point of them is to touch both sides.
	"adapters.go":      true,
	"adapters_test.go": true,
	// The command allowlist: which programs this user trusts is a klein policy
	// read from klein's settings, not anything the protocol has an opinion about.
	"autoapprove.go":      true,
	"autoapprove_test.go": true,
	// Settings → Config plumbing, and the backend selection the app layer asks
	// for. appserverconfig.go imports nothing of klein's today, but it reads a
	// path klein's settings name and feeds Config.Env, so it stays on this side
	// too — this list is "what does not travel with the client", not merely
	// "what happens to import klein".
	"backend.go":              true,
	"backend_test.go":         true,
	"settings.go":             true,
	"codexconfig.go":          true,
	"codexconfig_test.go":     true,
	"mcpconfig.go":            true,
	"mcpconfig_test.go":       true,
	"appserverconfig.go":      true,
	"appserverconfig_test.go": true,
}

// The extraction is a file-level boundary until the package split lands, and a
// boundary nothing checks is a boundary that drifts. This is the check: it fails
// the moment a client file imports klein, which is the one way the move stops
// being a rename.
//
// It reads imports rather than dependencies on purpose. `go list -deps` answers
// for the package as a whole, which still pulls klein in through the files
// listed above and will keep saying so until they leave — it cannot see the
// split that is actually being built.
func TestClientFilesImportNothingOfKleins(t *testing.T) {
	t.Parallel()

	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing package files: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no Go files found; this test is looking in the wrong directory")
	}

	checked := 0
	for _, path := range names {
		name := filepath.Base(path)
		if kleinSideFiles[name] {
			continue
		}
		checked++
		for _, imported := range importsOf(t, path) {
			if strings.HasPrefix(imported, kleinModule) {
				t.Errorf(
					"%s is a client file and imports %s\n"+
						"\tthe client must depend on nothing of klein's — take the klein type through an\n"+
						"\tinterface in types.go and adapt it in adapters.go, or add %s to kleinSideFiles\n"+
						"\tif it genuinely belongs on klein's side of the split",
					name, imported, name)
			}
		}
	}
	if checked == 0 {
		t.Error("every file was excused; kleinSideFiles has swallowed the package")
	}
}

// Named files that no longer exist would silently stop being checked, and the
// exception list is the one place where being out of date is invisible.
func TestKleinSideFilesAllExist(t *testing.T) {
	t.Parallel()

	for name := range kleinSideFiles {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("kleinSideFiles names %s, which is not in this package: %v", name, err)
		}
	}
}

// importsOf returns the import paths of one Go file.
func importsOf(t *testing.T, path string) []string {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := make([]string, 0, len(f.Imports))
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("%s: unquoting import %s: %v", path, spec.Path.Value, err)
		}
		out = append(out, p)
	}
	return out
}
