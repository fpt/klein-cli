package agentserver

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// kleinModule is the import prefix this package must never name.
const kleinModule = "github.com/fpt/klein-cli/"

// This package is klein's app-server client, extracted so other Go programs can
// drive an app-server without taking klein with them. That is only true while it
// imports nothing of klein's, and an import is one line for someone who does not
// know the constraint exists — a klein type reachable from Config or Observer is
// a type every outside importer inherits.
//
// So the constraint is a test. It replaces the file-by-file version that guarded
// the same rule while the two halves shared a package, and unlike that one it
// admits no exceptions: everything here is the client now.
//
// Reading imports rather than `go list -deps` keeps the failure legible. A
// dependency walk would name some package deep in the graph; this names the file
// and the line someone just wrote.
func TestPackageImportsNothingOfKleins(t *testing.T) {
	t.Parallel()

	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing package files: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no Go files found; this test is looking in the wrong directory")
	}

	fset := token.NewFileSet()
	for _, path := range names {
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, spec := range f.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquoting import %s: %v", path, spec.Path.Value, err)
			}
			if strings.HasPrefix(imported, kleinModule) {
				t.Errorf(
					"%s imports %s\n"+
						"\tthis package is standalone — an outside importer gets everything it depends on.\n"+
						"\tTake the klein type through an interface in types.go and adapt it in\n"+
						"\tinternal/agentbackend/adapters.go, which is where klein's side of this lives",
					filepath.Base(path), imported)
			}
		}
	}
}
