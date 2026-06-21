package source

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/fpt/klein-cli/internal/researcher/model"
)

type Fetcher struct {
	Client *http.Client
	Now    func() time.Time
}

func (f Fetcher) Fetch(ctx context.Context, src model.Source) ([]model.Event, error) {
	if strings.ToLower(src.Type) != "rss" && strings.ToLower(src.Type) != "atom" {
		return nil, fmt.Errorf("unsupported source type %q", src.Type)
	}

	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "klein-researcher/0.1 (+https://github.com/fpt/klein-cli)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d", src.URL, resp.StatusCode)
	}

	return ParseFeed(src, resp.Body, f.now())
}

func (f Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now().UTC()
}

func ParseFeed(src model.Source, r io.Reader, fetchedAt time.Time) ([]model.Event, error) {
	src = model.NormalizeSource(src)
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	var rss rssFeed
	if err := xml.Unmarshal(b, &rss); err == nil && len(rss.Channel.Items) > 0 {
		events := make([]model.Event, 0, len(rss.Channel.Items))
		for _, item := range rss.Channel.Items {
			ev := model.Event{
				Source:      src.Name,
				Intake:      src.Intake,
				Role:        src.Role,
				TrustTier:   src.TrustTier,
				Weight:      src.Weight,
				Title:       cleanText(item.Title),
				URL:         strings.TrimSpace(item.Link),
				PublishedAt: parseTime(firstNonEmpty(item.PubDate, item.Date)),
				Summary:     cleanText(firstNonEmpty(item.Description, item.Content)),
				FetchedAt:   fetchedAt,
			}
			if ev.PublishedAt.IsZero() {
				ev.PublishedAt = fetchedAt
			}
			ev.ID = eventID(ev)
			if ev.Title != "" {
				events = append(events, ev)
			}
		}
		return events, nil
	}

	var rdf rdfFeed
	if err := xml.Unmarshal(b, &rdf); err == nil && len(rdf.Items) > 0 {
		events := make([]model.Event, 0, len(rdf.Items))
		for _, item := range rdf.Items {
			ev := model.Event{
				Source:      src.Name,
				Intake:      src.Intake,
				Role:        src.Role,
				TrustTier:   src.TrustTier,
				Weight:      src.Weight,
				Title:       cleanText(item.Title),
				URL:         strings.TrimSpace(item.Link),
				PublishedAt: parseTime(item.Date),
				Summary:     cleanText(item.Description),
				FetchedAt:   fetchedAt,
			}
			if ev.PublishedAt.IsZero() {
				ev.PublishedAt = fetchedAt
			}
			ev.ID = eventID(ev)
			if ev.Title != "" {
				events = append(events, ev)
			}
		}
		return events, nil
	}

	var atom atomFeed
	if err := xml.Unmarshal(b, &atom); err != nil {
		return nil, err
	}
	if len(atom.Entries) == 0 {
		return nil, fmt.Errorf("feed contains no rss items or atom entries")
	}

	events := make([]model.Event, 0, len(atom.Entries))
	for _, entry := range atom.Entries {
		ev := model.Event{
			Source:      src.Name,
			Intake:      src.Intake,
			Role:        src.Role,
			TrustTier:   src.TrustTier,
			Weight:      src.Weight,
			Title:       cleanText(entry.Title),
			URL:         atomLink(entry.Links),
			PublishedAt: parseTime(firstNonEmpty(entry.Published, entry.Updated)),
			Summary:     cleanText(firstNonEmpty(entry.Summary, entry.Content)),
			FetchedAt:   fetchedAt,
		}
		if ev.PublishedAt.IsZero() {
			ev.PublishedAt = fetchedAt
		}
		ev.ID = eventID(ev)
		if ev.Title != "" {
			events = append(events, ev)
		}
	}
	return events, nil
}

type rssFeed struct {
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	PubDate     string `xml:"pubDate"`
	Date        string `xml:"date"`
	Description string `xml:"description"`
	Content     string `xml:"encoded"`
}

type rdfFeed struct {
	Items []rdfItem `xml:"item"`
}

type rdfItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Date        string `xml:"date"`
	Description string `xml:"description"`
}

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title     string      `xml:"title"`
	Links     []atomLinkT `xml:"link"`
	Published string      `xml:"published"`
	Updated   string      `xml:"updated"`
	Summary   string      `xml:"summary"`
	Content   string      `xml:"content"`
}

type atomLinkT struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

func atomLink(links []atomLinkT) string {
	for _, link := range links {
		if link.Rel == "" || link.Rel == "alternate" {
			return strings.TrimSpace(link.Href)
		}
	}
	if len(links) > 0 {
		return strings.TrimSpace(links[0].Href)
	}
	return ""
}

func eventID(ev model.Event) string {
	base := ev.URL
	if base == "" {
		base = ev.Source + "\n" + ev.Title + "\n" + ev.PublishedAt.Format(time.RFC3339)
	}
	sum := sha1.Sum([]byte(base))
	return hex.EncodeToString(sum[:])
}

func parseTime(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		time.RFC3339Nano,
		"Mon, 02 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

var tagRE = regexp.MustCompile(`<[^>]+>`)
var spaceRE = regexp.MustCompile(`\s+`)

func cleanText(v string) string {
	v = html.UnescapeString(v)
	v = tagRE.ReplaceAllString(v, " ")
	v = spaceRE.ReplaceAllString(v, " ")
	return strings.TrimSpace(v)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
