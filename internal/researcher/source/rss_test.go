package source

import (
	"strings"
	"testing"
	"time"

	"github.com/fpt/klein-cli/internal/researcher/model"
)

func TestParseRSSFeed(t *testing.T) {
	feed := `<?xml version="1.0"?>
<rss version="2.0">
  <channel>
    <item>
      <title>Oil rises as Iran tensions hit markets</title>
      <link>https://example.com/a</link>
      <pubDate>Mon, 02 Jan 2006 15:04:05 +0000</pubDate>
      <description><![CDATA[Crude and naphtha prices moved higher.]]></description>
    </item>
  </channel>
</rss>`
	src := model.Source{Name: "test", Type: "rss", URL: "https://example.com/rss.xml", Intake: "government-us", TrustTier: "primary"}
	events, err := ParseFeed(src, strings.NewReader(feed), time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Source != "test" {
		t.Fatalf("source = %q, want test", events[0].Source)
	}
	if events[0].ID == "" {
		t.Fatal("id is empty")
	}
	if events[0].TrustTier != "primary" || events[0].Intake != "government-us" || events[0].Role != "signal" {
		t.Fatalf("provenance not copied: %+v", events[0])
	}
}

func TestParseRDFFeed(t *testing.T) {
	feed := `<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:dc="http://purl.org/dc/elements/1.1/">
  <item rdf:about="https://example.go.jp/a">
    <title>Government releases energy security statement</title>
    <link>https://example.go.jp/a</link>
    <dc:date>2026-05-15T00:00:00+09:00</dc:date>
    <description>Oil, gas, and supply chain measures.</description>
  </item>
</rdf:RDF>`
	src := model.Source{Name: "gov", Type: "rss", URL: "https://example.go.jp/rss.rdf", TrustTier: "primary"}
	events, err := ParseFeed(src, strings.NewReader(feed), time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].PublishedAt.IsZero() {
		t.Fatal("published time is zero")
	}
}
