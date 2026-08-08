package agentserver

import (
	"bytes"
	"strings"
	"testing"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// klein's logger is the only implementation in the tree, and the interface is
// sized to it: if this stops compiling, Logger grew a method the client does not
// need or klein's logger lost one it does.
var _ Logger = (*pkgLogger.Logger)(nil)

// reportPanicked runs a drift report against logger and says whether it blew up.
func reportPanicked(logger Logger) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	tp := &turnProgress{logger: logger, reported: map[string]bool{}}
	tp.reportUnrendered("toolResult")
	return false
}

// The trap backendLogger exists for, pinned as behavior rather than as a claim
// about interface identity: a nil *pkgLogger.Logger assigned straight into a
// Logger field is not a nil interface, so the client's `logger != nil` guards
// wave it through and the first drift report panics on the nil receiver.
// Converting at the injection site is what turns that into silence.
func TestBackendLogger_ConvertsTheTypedNilThatWouldPanic(t *testing.T) {
	t.Parallel()
	var absent *pkgLogger.Logger

	if !reportPanicked(absent) {
		t.Fatal("a nil *pkgLogger.Logger no longer panics when reported through; " +
			"if that is now true, backendLogger is dead code")
	}
	if reportPanicked(backendLogger(absent)) {
		t.Error("backendLogger did not disarm the typed nil")
	}
}

// A real logger has to survive the conversion, and still write where it was
// pointed — a guard that swallowed every logger would pass the test above and
// lose the reports this path exists to deliver.
func TestBackendLogger_RealLoggerStillReports(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer

	got := backendLogger(pkgLogger.NewLoggerWithConsoleWriter(pkgLogger.LogLevelWarn, &buf))
	if got == nil {
		t.Fatal("backendLogger dropped a real logger")
	}
	got.Warn("drifted", "method", "gallium/whatever")

	if !strings.Contains(buf.String(), "gallium/whatever") {
		t.Errorf("the converted logger did not write through: %q", buf.String())
	}
}
