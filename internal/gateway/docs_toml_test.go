package gateway

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/fpt/klein-cli/internal/config"
)

// docFiles are the documents whose ```toml blocks are settings examples people
// copy. Anything else in them would have to be excluded here.
var docFiles = []string{
	"README.md", "CLAUDE.md", "doc/CONFIGS.md", "doc/DESIGN.md", "testsuite/README.md",
}

var tomlBlock = regexp.MustCompile("(?sm)^```toml\n(.*?)^```$")

// Documented settings are copied verbatim by people who then wonder why nothing
// happened. A key that TOML parses but klein does not read fails silently — the
// setting is simply absent — which is exactly how the testsuite's `maxTokens`
// went unnoticed for however long it sat there next to a schema saying
// `max_tokens`.
//
// So every example is decoded against the real structs and any key klein would
// not have read is a failure. This lives in gateway because it is the one
// package that can see both halves: config owns the top level, and the [claw]
// block is opaque to config by design.
func TestDocumentedSettingsUseRealKeys(t *testing.T) {
	t.Parallel()

	checked := 0
	for _, rel := range docFiles {
		src, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		for _, m := range tomlBlock.FindAllSubmatchIndex(src, -1) {
			block := string(src[m[2]:m[3]])
			where := blockLocation(rel, src, m[0])
			checked++

			var s config.Settings
			md, err := toml.Decode(block, &s)
			if err != nil {
				t.Errorf("%s: example does not parse: %v\n%s", where, err, block)
				continue
			}
			// Keys under claw land in a generic map, so the top-level decoder
			// reports them as undecoded; they are checked against the gateway's
			// own schema below instead.
			if unknown := withoutClaw(md.Undecoded()); len(unknown) > 0 {
				t.Errorf("%s: klein does not read %v\n%s", where, unknown, block)
			}
			if len(s.Claw) > 0 {
				checkClawKeys(t, where, block, s.Claw)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no TOML examples found; the extractor or the docs moved")
	}
	t.Logf("checked %d documented settings examples", checked)
}

// checkClawKeys decodes the claw block against the gateway's own schema, which
// is the only place those key names are known.
func checkClawKeys(t *testing.T, where, block string, claw map[string]any) {
	t.Helper()

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(claw); err != nil {
		t.Errorf("%s: re-encoding the claw block: %v", where, err)
		return
	}
	var cfg GatewayConfig
	md, err := toml.Decode(buf.String(), &cfg)
	if err != nil {
		t.Errorf("%s: claw block does not decode: %v", where, err)
		return
	}
	// `heartbeat` is documented precisely as the key that is ignored.
	var unknown []string
	for _, k := range md.Undecoded() {
		if s := k.String(); !strings.HasPrefix(s, "heartbeat") {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		t.Errorf("%s: the gateway does not read %v\n%s", where, unknown, block)
	}
}

// withoutClaw drops keys the top-level decoder cannot judge.
func withoutClaw(keys []toml.Key) []string {
	var out []string
	for _, k := range keys {
		if s := k.String(); !strings.HasPrefix(s, "claw") {
			out = append(out, s)
		}
	}
	return out
}

// blockLocation renders file:line for the start of a fenced block.
func blockLocation(rel string, src []byte, offset int) string {
	line := bytes.Count(src[:offset], []byte("\n")) + 1
	return rel + ":" + itoa(line)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
