package apod

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apodclient "github.com/kamaln7/airlock.space/pkg/apod"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// a video day is an absence, not a failure: it must resolve to nil bytes so it
// caches, instead of sending every new session back through the fetch
func TestVideoDayResolvesToNilBytes(t *testing.T) {
	old := cacheDir
	cacheDir = t.TempDir()
	defer func() { cacheDir = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("not an image"))
	}))
	defer srv.Close()

	a := mustAPOD(t, &apodclient.Picture{ImageURL: srv.URL + "/apod.mp4"})
	for range 2 {
		byt, err := a.ImageBytes()
		if err != nil || byt != nil {
			t.Fatalf("ImageBytes() = %v, %v; want nil, nil", byt, err)
		}
	}
}

// the size is the layout's to know before the photo lands: it must be out as
// soon as the header is in, not after the megabytes behind it
func TestImageSizeIsKnownBeforeTheBodyArrives(t *testing.T) {
	old := cacheDir
	cacheDir = t.TempDir()
	defer func() { cacheDir = old }()

	var buf bytes.Buffer
	png.Encode(&buf, image.NewGray(image.Rect(0, 0, 40, 30)))
	full := buf.Bytes()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(full[:33]) // signature and IHDR: the size, nothing more
		w.(http.Flusher).Flush()
		<-release
		w.Write(full[33:])
	}))
	defer srv.Close()
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()

	a := mustAPOD(t, &apodclient.Picture{ImageURL: srv.URL + "/apod.png"})
	got := make(chan image.Point, 1)
	go func() { got <- a.WaitImageSize() }()
	select {
	case sz := <-got:
		if sz != (image.Point{X: 40, Y: 30}) {
			t.Errorf("WaitImageSize() = %v; want 40x30", sz)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("size not reported until the whole image arrived")
	}
	finish()
	if _, err := a.ImageBytes(); err != nil { // let the fetch finish before cleanup
		t.Fatal(err)
	}
}

func TestImageCacheSurvivesANewAPODInstance(t *testing.T) {
	cacheDir = t.TempDir()
	defer func() { cacheDir = imageCacheDir() }()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Write(onePixelPNG(t))
	}))
	defer srv.Close()

	img := &apodclient.Picture{Date: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC), ImageURL: srv.URL + "/apod.png"}
	first, err := mustAPOD(t, img).ImageBytes()
	if err != nil || len(first) == 0 {
		t.Fatalf("ImageBytes() = %d bytes, %v", len(first), err)
	}

	// a fresh instance stands in for the day being evicted from memory, or the
	// process restarting: the bytes must come off disk, not the network
	again := mustAPOD(t, img)
	second, err := again.ImageBytes()
	if err != nil || !bytes.Equal(second, first) {
		t.Fatalf("cached ImageBytes() = %d bytes, %v; want the same %d", len(second), err, len(first))
	}
	if hits != 1 {
		t.Errorf("fetched %d times; want 1", hits)
	}
	if again.ImageSize.X != 1 || again.ImageSize.Y != 1 {
		t.Errorf("ImageSize = %v; want 1x1 from the cached bytes", again.ImageSize)
	}
}

func TestVideoDayIsRecordedWithoutAnImageFile(t *testing.T) {
	cacheDir = t.TempDir()
	defer func() { cacheDir = imageCacheDir() }()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "video/mp4")
	}))
	defer srv.Close()

	img := &apodclient.Picture{Date: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), ImageURL: srv.URL + "/apod.mp4"}
	for range 2 {
		if byt, err := mustAPOD(t, img).ImageBytes(); err != nil || byt != nil {
			t.Fatalf("ImageBytes() = %v, %v; want nil, nil", byt, err)
		}
	}
	if hits != 1 {
		t.Errorf("fetched %d times; want 1", hits)
	}
}

func TestByDateCoalescesConcurrentMisses(t *testing.T) {
	cacheDir = t.TempDir()
	defer func() { cacheDir = imageCacheDir() }()

	var hits atomic.Int32
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-block
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"date":"2026-08-30","title":"t","url":"http://x/article","hdurl":"http://x/a.jpg","explanation":"e"}`))
	}))
	defer srv.Close()

	days.Lock()
	days.byDate = map[string]*APOD{}
	days.order = nil
	days.Unlock()
	old := live.client
	live.client = apodclient.NewClient(apodclient.WithBaseURL(srv.URL), apodclient.WithHTTPClient(srv.Client()))
	defer func() { live.client = old }()

	day := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	errc := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ByDate(day)
			errc <- err
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Fatalf("NASA hits while blocked = %d; want 1", got)
	}
	close(block)
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("NASA hits after join = %d; want 1", got)
	}
}

func TestCacheFileRejectsADateThatIsNotOne(t *testing.T) {
	cacheDir = t.TempDir()
	defer func() { cacheDir = imageCacheDir() }()

	if got := cacheFile("../../etc/passwd", "json"); got != "" {
		t.Errorf("cacheFile() = %q; want empty", got)
	}
}

func TestMissingFeaturedImageDoesNotFetchArticle(t *testing.T) {
	old := cacheDir
	cacheDir = t.TempDir()
	defer func() { cacheDir = old }()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "article is not an image", http.StatusInternalServerError)
	}))
	defer srv.Close()
	a := mustAPOD(t, &apodclient.Picture{
		Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), ArticleURL: srv.URL,
	})
	body, err := a.ImageBytes()
	if err != nil || body != nil || hits != 0 {
		t.Fatalf("ImageBytes() = %v, %v; article fetches = %d", body, err, hits)
	}
}

func TestCacheRejectsLegacyAndWrongDayRecords(t *testing.T) {
	old := cacheDir
	cacheDir = t.TempDir()
	defer func() { cacheDir = old }()
	for _, record := range []day{
		{Version: 1, APOD: &apodclient.Picture{Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}},
		{Version: dayVersion, APOD: &apodclient.Picture{Date: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)}},
	} {
		body, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cacheFile("2026-09-01", "json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := loadDay("2026-09-01"); got != nil {
			t.Fatalf("accepted invalid cache record: %+v", got)
		}
	}
}

func mustAPOD(t *testing.T, entry *apodclient.Picture) *APOD {
	t.Helper()
	a, err := newAPOD(entry)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func resetDays() {
	days.Lock()
	days.byDate = map[string]*APOD{}
	days.order = nil
	days.Unlock()
}

func TestCachedDayReconstructsMarkdownWithoutNetwork(t *testing.T) {
	old := cacheDir
	cacheDir = t.TempDir()
	defer func() { cacheDir = old }()
	resetDays()
	defer resetDays()

	oldClient := live.client
	live.client = apodclient.NewClient(apodclient.WithBaseURL("http://127.0.0.1:0"))
	defer func() { live.client = oldClient }()

	rec, err := json.Marshal(day{
		Version: dayVersion,
		APOD: &apodclient.Picture{
			Date:            time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			ArticleURL:      "https://example.com/articles/one/",
			ExplanationHTML: `<strong>Explanation:</strong> See <a href="/other">more</a>.`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheFile("2026-09-01", "json"), rec, 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := ByDate(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := parsedMarkdown{
		Paragraphs: []string{"See more."},
		Links:      []parsedLink{{"more", "https://example.com/other"}},
	}
	if got := parseMarkdown(a.Explanation); !reflect.DeepEqual(got, want) {
		t.Errorf("Explanation %q parsed as %+v; want %+v", a.Explanation, got, want)
	}
	if want := `<strong>Explanation:</strong> See <a href="/other">more</a>.`; a.ExplanationHTML != want {
		t.Errorf("ExplanationHTML = %q; want the published HTML %q", a.ExplanationHTML, want)
	}
}

type parsedLink struct{ Text, Destination string }

// parsedMarkdown is what a reader sees, independent of which delimiters the
// converter chose to write it with.
type parsedMarkdown struct {
	Paragraphs []string // visible text, line breaks as \n
	Em, Strong []string
	Links      []parsedLink
}

func parseMarkdown(md string) parsedMarkdown {
	src := []byte(md)
	doc := goldmark.New().Parser().Parse(text.NewReader(src))
	var inline func(ast.Node) string
	inline = func(n ast.Node) string {
		var b strings.Builder
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Text:
				b.Write(c.Segment.Value(src))
				if c.HardLineBreak() || c.SoftLineBreak() {
					b.WriteByte('\n')
				}
			case *ast.String:
				b.Write(c.Value)
			default:
				b.WriteString(inline(c))
			}
		}
		return b.String()
	}
	var p parsedMarkdown
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Paragraph:
			p.Paragraphs = append(p.Paragraphs, strings.TrimSpace(inline(n)))
		case *ast.Emphasis:
			if n.Level == 2 {
				p.Strong = append(p.Strong, inline(n))
			} else {
				p.Em = append(p.Em, inline(n))
			}
		case *ast.Link:
			p.Links = append(p.Links, parsedLink{inline(n), string(n.Destination)})
		}
		return ast.WalkContinue, nil
	})
	return p
}

// newAPOD must turn NASA's HTML into Markdown that keeps paragraphs, emphasis,
// link labels and (article-relative resolved) targets, and drops hidden content.
func TestNewAPODExplanationKeepsReadableStructure(t *testing.T) {
	const article = "https://science.nasa.gov/image-article/apod-one/"
	for _, tt := range []struct {
		name, html string
		want       parsedMarkdown
		absent     []string
	}{
		{"paragraphs", `<p>First</p><p>Second</p>`,
			parsedMarkdown{Paragraphs: []string{"First", "Second"}}, nil},
		{"line break", `<p>First<br>line.</p>`,
			parsedMarkdown{Paragraphs: []string{"First\nline."}}, nil},
		{"emphasis", `<strong>Explanation:</strong> A <em>faint</em> glow.`,
			parsedMarkdown{Paragraphs: []string{"A faint glow."}, Em: []string{"faint"}}, nil},
		{"label in b", `<p><b>Explanation:</b> Body.</p>`,
			parsedMarkdown{Paragraphs: []string{"Body."}}, nil},
		{"label in b and i", `<b><i>Explanation:</i></b> Body.`,
			parsedMarkdown{Paragraphs: []string{"Body."}}, nil},
		{"plain text label", `<p>Explanation: Body.</p>`,
			parsedMarkdown{Paragraphs: []string{"Body."}}, nil},
		{"label alone in its paragraph", `<p><strong>Explanation:</strong></p><p>Body.</p>`,
			parsedMarkdown{Paragraphs: []string{"Body."}}, nil},
		{"label then br", `<b>Explanation:</b><br>Body<br>line.`,
			parsedMarkdown{Paragraphs: []string{"Body\nline."}}, nil},
		{"label with body emphasis", `<strong>Explanation: Bold body</strong> tail`,
			parsedMarkdown{Paragraphs: []string{"Bold body tail"}, Strong: []string{"Bold body"}}, nil},
		{"label followed by strong body", `<b>Explanation:</b> <strong>Bold</strong> end.`,
			parsedMarkdown{Paragraphs: []string{"Bold end."}, Strong: []string{"Bold"}}, nil},
		{"indentation is not a line break", "<p>\n   <b>Explanation:</b>\n   First\n   line.\n</p>\n<p>Second.</p>",
			parsedMarkdown{Paragraphs: []string{"First line.", "Second."}}, nil},
		{"later label text stays", `<p><b>Explanation:</b> Body.</p><p>Explanation: later.</p>`,
			parsedMarkdown{Paragraphs: []string{"Body.", "Explanation: later."}}, nil},
		{"label not at the start stays", `<p>Intro.</p><p><strong>Explanation:</strong> Later.</p>`,
			parsedMarkdown{Paragraphs: []string{"Intro.", "Explanation: Later."}, Strong: []string{"Explanation:"}}, nil},
		{"other leading sentence stays", `<p>An Explanation: of sorts.</p>`,
			parsedMarkdown{Paragraphs: []string{"An Explanation: of sorts."}}, nil},
		{"relative and absolute links", `See <a href="/other/">the <em>other</em> post</a> and <a href="https://example.com/x">star</a>.`,
			parsedMarkdown{
				Paragraphs: []string{"See the other post and star."},
				Em:         []string{"other"},
				Links:      []parsedLink{{"the other post", "https://science.nasa.gov/other/"}, {"star", "https://example.com/x"}},
			}, nil},
		{"entities", `A &amp; B &#x2014; &quot;C&quot;`,
			parsedMarkdown{Paragraphs: []string{`A & B — "C"`}}, []string{"&amp;", "&#x2014;", "&quot;"}},
		{"hidden content", `Visible<script>not text</script><style>not style</style> words<!-- hidden comment -->.`,
			parsedMarkdown{Paragraphs: []string{"Visible words."}}, []string{"not text", "not style", "hidden comment"}},
		{"inline boundaries", `A <em>super</em>nova and dark <em>matter </em>halo.`,
			parsedMarkdown{Paragraphs: []string{"A supernova and dark matter halo."}, Em: []string{"super", "matter"}}, nil},
		{"unclosed HTML", `<p>One<p>Two <em>open`,
			parsedMarkdown{Paragraphs: []string{"One", "Two open"}, Em: []string{"open"}}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := mustAPOD(t, &apodclient.Picture{ArticleURL: article, ExplanationHTML: tt.html})
			if got := parseMarkdown(a.Explanation); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Explanation %q parsed as %+v; want %+v", a.Explanation, got, tt.want)
			}
			for _, s := range tt.absent {
				if strings.Contains(a.Explanation, s) {
					t.Errorf("Explanation %q must not contain %q", a.Explanation, s)
				}
			}
		})
	}
}

// A failed metadata refresh surfaces as an error and leaves the last good APOD
// untouched; the next good response, for a new day, replaces it.
func TestGetAPODErrorKeepsLastAndNewDayReplacesIt(t *testing.T) {
	var mu sync.Mutex
	body, status := `[{"date":"2026-08-30","title":"first","url":"http://x/a","explanation":"e"}]`, http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		b, s := body, status
		mu.Unlock()
		if s != http.StatusOK {
			http.Error(w, "upstream down", s)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(b))
	}))
	defer srv.Close()
	respond := func(b string, s int) {
		mu.Lock()
		body, status = b, s
		mu.Unlock()
	}
	n := &apod{client: apodclient.NewClient(apodclient.WithBaseURL(srv.URL), apodclient.WithHTTPClient(srv.Client()))}
	t.Setenv("APOD_DATE", "")

	first, err := n.getAPOD(t.Context())
	if err != nil || first.Title != "first" || n.last != first {
		t.Fatalf("getAPOD() = %+v, %v; want first installed as last", first, err)
	}

	respond("", http.StatusInternalServerError)
	if got, err := n.getAPOD(t.Context()); err == nil || got != nil {
		t.Fatalf("getAPOD() during outage = %+v, %v; want nil and an error", got, err)
	}
	if n.last != first || n.last.Title != "first" {
		t.Fatalf("last = %+v after failed refresh; want the original %p intact", n.last, first)
	}

	respond(`[{"date":"2026-08-31","title":"second","url":"http://x/b","explanation":"e"}]`, http.StatusOK)
	second, err := n.getAPOD(t.Context())
	if err != nil || second.Title != "second" || n.last != second || second == first {
		t.Fatalf("getAPOD() after recovery = %+v, %v; want second installed as last", second, err)
	}
}

func TestGetAPODUsesLatestAndAPODDate(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/apod-basic/") {
			w.Write([]byte(`{"date":"2026-08-29","title":"dated","url":"http://x/a","explanation":"e"}`))
			return
		}
		w.Write([]byte(`[{"date":"2026-08-30","title":"latest","url":"http://x/b","explanation":"e"}]`))
	}))
	defer srv.Close()
	n := &apod{client: apodclient.NewClient(apodclient.WithBaseURL(srv.URL), apodclient.WithHTTPClient(srv.Client()))}

	t.Setenv("APOD_DATE", "")
	latest, err := n.getAPOD(t.Context())
	if err != nil || latest.Title != "latest" {
		t.Fatalf("getAPOD() = %+v, %v; want latest", latest, err)
	}

	t.Setenv("APOD_DATE", "2026-08-29")
	dated, err := n.getAPOD(t.Context())
	if err != nil || dated.Title != "dated" {
		t.Fatalf("getAPOD() with APOD_DATE = %+v, %v; want dated", dated, err)
	}
	again, err := n.getAPOD(t.Context())
	if err != nil || again != dated || len(paths) != 2 {
		t.Fatalf("repeat APOD_DATE refetched: %v, paths = %v", err, paths)
	}

	t.Setenv("APOD_DATE", "not-a-date")
	if _, err := n.getAPOD(t.Context()); err == nil {
		t.Fatal("getAPOD() accepted an invalid APOD_DATE")
	}
}

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
