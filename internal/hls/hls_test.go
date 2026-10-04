package hls

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brysonreece/kotori/internal/aescbc"
	"github.com/brysonreece/kotori/internal/fetch"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestParseMaster(t *testing.T) {
	text := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2"
720/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1920x1080
https://other.example/1080/index.m3u8
`
	p, err := Parse(text, mustParseURL(t, "https://cdn.example/a/master.m3u8?token=x"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Variant{
		{URI: "https://cdn.example/a/720/index.m3u8", Bandwidth: 800000, Height: 720},
		{URI: "https://other.example/1080/index.m3u8", Bandwidth: 2000000, Height: 1080},
	}
	if len(p.Variants) != 2 || p.Variants[0] != want[0] || p.Variants[1] != want[1] {
		t.Fatalf("got %+v, want %+v", p.Variants, want)
	}
}

func TestParseMedia(t *testing.T) {
	text := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:7
#EXT-X-KEY:METHOD=AES-128,URI="key.bin",IV=0x000102030405060708090a0b0c0d0e0f
#EXTINF:4.0,
a.ts
#EXT-X-KEY:METHOD=NONE
#EXTINF:4.0,
b.ts
#EXT-X-ENDLIST
`
	p, err := Parse(text, mustParseURL(t, "https://cdn.example/v/index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Segments) != 2 {
		t.Fatalf("got %d segments, want 2", len(p.Segments))
	}
	first, second := p.Segments[0], p.Segments[1]
	if first.URI != "https://cdn.example/v/a.ts" || first.Seq != 7 || second.Seq != 8 {
		t.Errorf("unexpected segments %+v %+v", first, second)
	}
	if first.Key == nil || first.Key.URI != "https://cdn.example/v/key.bin" || len(first.Key.IV) != 16 {
		t.Errorf("unexpected key %+v", first.Key)
	}
	if second.Key != nil {
		t.Errorf("METHOD=NONE should clear the key, got %+v", second.Key)
	}
}

func TestParseRejectsNonPlaylist(t *testing.T) {
	if _, err := Parse("<html>blocked</html>", mustParseURL(t, "https://x/")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSelectVariant(t *testing.T) {
	variants := []Variant{
		{URI: "360", Height: 360, Bandwidth: 1},
		{URI: "720", Height: 720, Bandwidth: 2},
		{URI: "1080", Height: 1080, Bandwidth: 3},
	}
	cases := map[int]string{1080: "1080", 2160: "1080", 900: "720", 720: "720", 240: "360"}
	for quality, want := range cases {
		if got := SelectVariant(variants, quality).URI; got != want {
			t.Errorf("SelectVariant(%d) = %s, want %s", quality, got, want)
		}
	}
	unlabelled := []Variant{{URI: "low", Bandwidth: 1}, {URI: "high", Bandwidth: 9}}
	if got := SelectVariant(unlabelled, 1080).URI; got != "high" {
		t.Errorf("without resolutions got %s, want high", got)
	}
}

func obfuscate(t *testing.T, realURL string) string {
	t.Helper()
	ciphertext, err := aescbc.Encrypt(segmentURLKey, segmentURLIV, []byte(realURL))
	if err != nil {
		t.Fatal(err)
	}
	return "https://mt.nekostream.site/seg/" + base64.RawURLEncoding.EncodeToString(ciphertext)
}

func TestRevealSegments(t *testing.T) {
	segments := []Segment{
		{URI: "https://cdn.example/plain.ts"},
		{URI: obfuscate(t, "https://real.example/1.ts")},
		{URI: "https://mt.nekostream.site/seg/bm90LWEtdXJs"},
	}
	got, dropped := revealSegments(segments)
	if dropped != 1 || len(got) != 2 {
		t.Fatalf("got %d segments and %d dropped, want 2 and 1", len(got), dropped)
	}
	if got[0].URI != "https://cdn.example/plain.ts" || got[1].URI != "https://real.example/1.ts" {
		t.Errorf("unexpected segments %+v", got)
	}
}

func fakePNG(payload []byte) []byte {
	out := append([]byte{}, pngSignature...)
	out = append(out, "IHDRjunk"...)
	out = append(out, "IEND"...)
	out = append(out, 0xAE, 0x42, 0x60, 0x82)
	return append(out, payload...)
}

func TestStripPNG(t *testing.T) {
	payload := []byte("G@media")
	if got := stripPNG(fakePNG(payload)); !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
	if got := stripPNG(payload); !bytes.Equal(got, payload) {
		t.Errorf("plain data was changed: %q", got)
	}
}

func TestDownload(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := []byte("fedcba9876543210")
	first := bytes.Repeat([]byte("first-segment."), 100)
	second := bytes.Repeat([]byte("second-segment."), 100)
	third := bytes.Repeat([]byte("third-segment."), 100)
	encrypted, err := aescbc.Encrypt(key, iv, first)
	if err != nil {
		t.Fatal(err)
	}

	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://player.example/" {
			http.Error(w, "missing referer", http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "#EXTM3U\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=1280x720\n720/index.m3u8\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=2,RESOLUTION=1920x1080\n1080/index.m3u8\n")
	})
	mux.HandleFunc("/1080/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#EXTM3U\n"+
			"#EXT-X-KEY:METHOD=AES-128,URI=\"/key\",IV=0x%x\n#EXTINF:4,\nseg0.ts\n"+
			"#EXT-X-KEY:METHOD=NONE\n#EXTINF:4,\nseg1.ts\n"+
			"#EXTINF:4,\n%s\n#EXT-X-ENDLIST\n", iv, obfuscate(t, server.URL+"/hidden.ts"))
	})
	mux.HandleFunc("/key", func(w http.ResponseWriter, r *http.Request) { w.Write(key) })
	mux.HandleFunc("/1080/seg0.ts", func(w http.ResponseWriter, r *http.Request) { w.Write(encrypted) })
	mux.HandleFunc("/1080/seg1.ts", func(w http.ResponseWriter, r *http.Request) { w.Write(fakePNG(second)) })
	mux.HandleFunc("/hidden.ts", func(w http.ResponseWriter, r *http.Request) { w.Write(third) })
	server = httptest.NewServer(mux)
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "episode.download")
	var last Progress
	d := &Downloader{
		Client:      fetch.New(),
		Headers:     map[string]string{"Referer": "https://player.example/"},
		Quality:     1080,
		Concurrency: 3,
		Progress:    func(p Progress) { last = p },
	}
	result, err := d.Download(context.Background(), server.URL+"/master.m3u8", dest)
	if err != nil {
		t.Fatal(err)
	}
	if result.Height != 1080 {
		t.Errorf("unexpected result %+v", result)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Join([][]byte{first, second, third}, nil)
	if !bytes.Equal(got, want) {
		t.Errorf("output is %d bytes, want %d", len(got), len(want))
	}
	if last.Done != 3 || last.Total != 3 || last.Bytes != int64(len(want)) {
		t.Errorf("unexpected final progress %+v", last)
	}
	if _, err := os.Stat(dest + ".parts"); !os.IsNotExist(err) {
		t.Error("parts directory was not cleaned up")
	}
}

func TestDownloadFailsOnMissingSegment(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\ngone.ts\n#EXT-X-ENDLIST\n")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	defer func(d time.Duration) { retryBackoff = d }(retryBackoff)
	retryBackoff = time.Millisecond

	dest := filepath.Join(t.TempDir(), "x.download")
	d := &Downloader{Client: fetch.New(), Concurrency: 1}
	if _, err := d.Download(context.Background(), server.URL+"/index.m3u8", dest); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a partial output file was left behind")
	}
}

func TestDownloadBacksOffWhenRateLimited(t *testing.T) {
	defer func(d time.Duration) { retryBackoff = d }(retryBackoff)
	retryBackoff = time.Millisecond

	var mu sync.Mutex
	rejected := map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\na.ts\n#EXTINF:4,\nb.ts\n#EXT-X-ENDLIST\n")
	})
	// Every segment is refused three times before it is served.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		rejected[r.URL.Path]++
		refuse := rejected[r.URL.Path] <= 3
		mu.Unlock()
		if refuse {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, r.URL.Path)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "x.download")
	d := &Downloader{Client: fetch.New(), Concurrency: 2}
	if d.Paused() != 0 {
		t.Error("a download that has not started reports a pause")
	}
	if _, err := d.Download(context.Background(), server.URL+"/index.m3u8", dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "/a.ts/b.ts" {
		t.Errorf("got %q", got)
	}
	// Each segment was asked for until it was served, refusals and all.
	if rejected["/a.ts"] != 4 || rejected["/b.ts"] != 4 {
		t.Errorf("requests per segment = %v, want 4 each", rejected)
	}
}

func TestRateLimitPause(t *testing.T) {
	// The server's Retry-After is used as given, however the attempt count
	// or the back-off cap compare.
	for _, c := range []struct {
		retryAfter time.Duration
		attempt    int
		want       time.Duration
	}{
		{10 * time.Second, 1, 10 * time.Second},
		{time.Second, 5, time.Second},
		{5 * time.Minute, 1, 5 * time.Minute},
		// Without one, the pause doubles per attempt, up to the cap.
		{0, 1, time.Second},
		{0, 2, 2 * time.Second},
		{0, 4, 8 * time.Second},
		{0, 7, time.Minute},
	} {
		if got := rateLimitPause(c.retryAfter, c.attempt); got != c.want {
			t.Errorf("rateLimitPause(%s, %d) = %s, want %s", c.retryAfter, c.attempt, got, c.want)
		}
	}
}
