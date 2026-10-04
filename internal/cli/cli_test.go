package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/brysonreece/kotori/internal/anikoto"
	"github.com/brysonreece/kotori/internal/ffmpeg"
)

func TestParseEpisodeSpec(t *testing.T) {
	got, err := parseEpisodeSpec("5-7, 1,6,3", 12)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 3, 5, 6, 7}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"0", "13", "4-2", "a", "1-", "1,,2"} {
		if _, err := parseEpisodeSpec(bad, 12); err == nil {
			t.Errorf("parseEpisodeSpec(%q) should fail", bad)
		}
	}
}

func TestSelectEpisodes(t *testing.T) {
	all := []anikoto.Episode{{Number: 1}, {Number: 2}, {Number: 3}}
	numbers := func(eps []anikoto.Episode) []int {
		var out []int
		for _, ep := range eps {
			out = append(out, ep.Number)
		}
		return out
	}
	cases := []struct {
		spec string
		last bool
		want []int
	}{
		{"", false, []int{1, 2, 3}},
		{"", true, []int{3}},
		{"1-2", false, []int{1, 2}},
		{"1-2", true, []int{2}},
	}
	for _, c := range cases {
		got, err := selectEpisodes(all, c.spec, c.last)
		if err != nil || !reflect.DeepEqual(numbers(got), c.want) {
			t.Errorf("selectEpisodes(%q, %v) = %v, %v; want %v", c.spec, c.last, numbers(got), err, c.want)
		}
	}
}

func TestCleanName(t *testing.T) {
	if got := cleanName("  Re:Zero  \t Part 2/3? "); got != "Re_Zero Part 2_3_" {
		t.Errorf("got %q", got)
	}
}

func TestLanguageCode(t *testing.T) {
	cases := map[string]string{"English": "en", "Spanish (Latin America)": "es", "Klingon": "klingon", "": "und"}
	for label, want := range cases {
		if got := languageCode(label); got != want {
			t.Errorf("languageCode(%q) = %q, want %q", label, got, want)
		}
	}
}

func TestWatchURL(t *testing.T) {
	cases := []struct {
		arg, base string
		explicit  bool
		want      string
	}{
		// A full URL is left alone until a base is given explicitly.
		{"https://anikoto.tv/watch/show/ep-1", defaultBaseURL, false, "https://anikoto.tv/watch/show/ep-1"},
		{"https://anikoto.tv/watch/show/ep-1?x=1", "https://anikototv.to", true, "https://anikototv.to/watch/show/ep-1?x=1"},
		{"https://anikoto.tv/watch/show/ep-1", "new.example/", true, "https://new.example/watch/show/ep-1"},
		{"https://anikoto.tv/watch/show/ep-1", "http://localhost:8080", true, "http://localhost:8080/watch/show/ep-1"},
		// A slug is the usual form, and always uses the base.
		{"dragon-ball-gxrfm", defaultBaseURL, false, "https://anikototv.to/watch/dragon-ball-gxrfm"},
		{" dragon-ball-gxrfm ", "new.example", true, "https://new.example/watch/dragon-ball-gxrfm"},
		// A URL pasted without its scheme is still a URL.
		{"anikototv.to/watch/show/ep-1", defaultBaseURL, false, "https://anikototv.to/watch/show/ep-1"},
		{"anikoto.tv/watch/show", "new.example", true, "https://new.example/watch/show"},
		// A bare path always uses the base.
		{"/watch/show/ep-1", defaultBaseURL, false, "https://anikototv.to/watch/show/ep-1"},
		{"watch/show/ep-1", "new.example", true, "https://new.example/watch/show/ep-1"},
	}
	for _, c := range cases {
		got, err := watchURL(c.arg, c.base, c.explicit)
		if err != nil || got != c.want {
			t.Errorf("watchURL(%q, %q, %v) = %q, %v; want %q", c.arg, c.base, c.explicit, got, err, c.want)
		}
	}
	for _, bad := range [][2]string{
		{"https://anikoto.tv/watch/x", "ftp://nope"},
		{"https://anikoto.tv/watch/x", "https://"},
		{"ftp://anikoto.tv/watch/x", defaultBaseURL},
		{"  ", defaultBaseURL},
	} {
		if got, err := watchURL(bad[0], bad[1], false); err == nil {
			t.Errorf("watchURL(%q, %q) = %q, want an error", bad[0], bad[1], got)
		}
	}
}

func TestBaseURLPrecedence(t *testing.T) {
	const arg = "https://old.example/watch/x"
	url := func(args ...string) string {
		t.Helper()
		opts, err := parseFlags(append(args, arg), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		return opts.url
	}

	t.Setenv(baseURLEnv, "")
	if got := url(); got != arg {
		t.Errorf("no override: got %q", got)
	}
	t.Setenv(baseURLEnv, "env.example")
	if got := url(); got != "https://env.example/watch/x" {
		t.Errorf("environment: got %q", got)
	}
	if got := url("--base-url", "flag.example"); got != "https://flag.example/watch/x" {
		t.Errorf("flag over environment: got %q", got)
	}
}

func TestParseFlags(t *testing.T) {
	t.Setenv(baseURLEnv, "")
	opts, err := parseFlags([]string{"my-show-abc12", "-q", "720", "-a", "SUB", "-s", "hd, kiwi", "-f", ".MKV", "--no-subtitles"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.url != "https://anikototv.to/watch/my-show-abc12" || opts.quality != 720 || opts.audio != "sub" || opts.format != "mkv" || opts.subtitles {
		t.Errorf("unexpected options %+v", opts)
	}
	if !reflect.DeepEqual(opts.sources, []string{"hd", "kiwi"}) {
		t.Errorf("unexpected sources %v", opts.sources)
	}
	if opts, err := parseFlags([]string{"url"}, io.Discard); err != nil || opts.format != "mp4" {
		t.Errorf("default format: %+v, %v", opts, err)
	}

	for _, bad := range [][]string{
		{"url", "-q", "999"},
		{"url", "-a", "raw"},
		{"url", "-s", "nope"},
		{"url", "-c", "0"},
		{"url", "-j", "0"},
		{"url", "-f", "ts"},
		{"url", "--subtitle-format", "sub"},
	} {
		if _, err := parseFlags(bad, io.Discard); err == nil {
			t.Errorf("parseFlags(%v) should fail", bad)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--version"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), version) {
		t.Errorf("--version: code %d, output %q", code, stdout.String())
	}
	stdout.Reset()
	if code := Run(context.Background(), []string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "--quality") {
		t.Errorf("--help: code %d, output %q", code, stdout.String())
	}
	if code := Run(context.Background(), []string{"-q", "1"}, &stdout, &stderr); code != 2 {
		t.Errorf("bad flag: code %d, want 2", code)
	}
	// Searching needs someone at a terminal, which a test does not have.
	stderr.Reset()
	if code := Run(context.Background(), []string{"--list"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "needs a terminal") {
		t.Errorf("no arguments: code %d, stderr %q", code, stderr.String())
	}
}

func TestRunRequiresFFmpeg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	// The address is unroutable on purpose: the check must fail before any request.
	code := Run(context.Background(), []string{"http://127.0.0.1:1/watch/x"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "ffmpeg is required") {
		t.Errorf("code %d, stderr %q", code, stderr.String())
	}
}

func TestParseArguments(t *testing.T) {
	t.Setenv(baseURLEnv, "")
	cases := []struct {
		args     []string
		url      string
		query    string
		bareWord bool
	}{
		{nil, "", "", false},
		{[]string{"naruto"}, "https://anikototv.to/watch/naruto", "naruto", true},
		{[]string{"dragon", "ball"}, "", "dragon ball", false},
		{[]string{"dragon ball"}, "", "dragon ball", false},
		{[]string{"https://anikototv.to/watch/x/ep-1"}, "https://anikototv.to/watch/x/ep-1", "https://anikototv.to/watch/x/ep-1", false},
	}
	for _, c := range cases {
		opts, err := parseFlags(c.args, io.Discard)
		if err != nil {
			t.Errorf("parseFlags(%v): %v", c.args, err)
			continue
		}
		if opts.url != c.url || opts.query != c.query || opts.bareWord != c.bareWord {
			t.Errorf("parseFlags(%v) = url %q, query %q, bareWord %v", c.args, opts.url, opts.query, opts.bareWord)
		}
		if opts.origin != "https://anikototv.to" {
			t.Errorf("origin = %q", opts.origin)
		}
	}
}

func TestGivenSettings(t *testing.T) {
	t.Setenv(baseURLEnv, "")
	opts, err := parseFlags([]string{"x", "-q", "1080", "--last"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// A flag counts as given even when it repeats the default.
	if g := opts.given; !g.quality || !g.episodes || g.audio || g.format || g.subtitles {
		t.Errorf("given = %+v", g)
	}
	if opts.subtitleFormat != "vtt" {
		t.Errorf("default subtitle format = %q", opts.subtitleFormat)
	}
	for _, args := range [][]string{{"x", "--subtitle-format", ".SRT"}, {"x", "--no-subtitles"}} {
		opts, err := parseFlags(args, io.Discard)
		if err != nil || !opts.given.subtitles {
			t.Errorf("parseFlags(%v): given %+v, err %v", args, opts, err)
		}
	}
}

func TestFormatOptions(t *testing.T) {
	var names []string
	for _, o := range formatOptions() {
		names = append(names, o.Value)
	}
	if got := strings.Join(names, ","); got != "mkv,mov,mp4,avi,webm" {
		t.Errorf("got %s", got)
	}
}

func TestSubtitleFormat(t *testing.T) {
	cases := []struct {
		source, body, want string
	}{
		{"https://cdn.example/subs/eng-2.vtt", "WEBVTT\n\n", "vtt"},
		// Content wins over a misleading name.
		{"https://cdn.example/subs/eng.srt", "\xef\xbb\xbfWEBVTT\n", "vtt"},
		{"https://cdn.example/subs/eng.txt", "[Script Info]\nTitle: x", "ass"},
		{"https://cdn.example/subs/eng.SRT?token=1", "1\n00:00:01,000 --> 00:00:02,000\nHi", "srt"},
		{"https://cdn.example/subs/eng", "who knows", "vtt"},
	}
	for _, c := range cases {
		if got := subtitleFormat(c.source, []byte(c.body)); got != c.want {
			t.Errorf("subtitleFormat(%q) = %q, want %q", c.source, got, c.want)
		}
	}
}

func TestSaveSubtitle(t *testing.T) {
	path, err := ffmpeg.Find()
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	vtt := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:03.500\nHello there.\n")
	dir := t.TempDir()
	files := func() string {
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return strings.Join(names, ",")
	}

	// Converted: only the requested format is left behind.
	a := &app{ffmpeg: path, stderr: io.Discard}
	d := newDisplay(io.Discard, false, 1).begin(1, "Show")
	a.opts.subtitleFormat = "srt"
	if err := a.saveSubtitle(context.Background(), vtt, "https://cdn.example/eng.vtt", filepath.Join(dir, "Show E01.en"), d); err != nil {
		t.Fatal(err)
	}
	if files() != "Show E01.en.srt" {
		t.Fatalf("directory holds %s", files())
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "Show E01.en.srt")); !strings.Contains(string(body), "00:00:01,000 --> 00:00:03,500") {
		t.Errorf("not SubRip:\n%s", body)
	}

	// Already in the requested format: saved untouched.
	a.opts.subtitleFormat = "vtt"
	err = a.saveSubtitle(context.Background(), vtt, "https://cdn.example/eng.vtt", filepath.Join(dir, "Show E02.en"), d)
	if body, _ := os.ReadFile(filepath.Join(dir, "Show E02.en.vtt")); err != nil || !bytes.Equal(body, vtt) {
		t.Errorf("vtt was changed or not written: %v", err)
	}

	// Conversion fails: the original is kept under its own extension, and
	// the episode is told about it.
	a.opts.subtitleFormat = "srt"
	a.ffmpeg = filepath.Join(dir, "no-such-ffmpeg")
	err = a.saveSubtitle(context.Background(), vtt, "https://cdn.example/x.vtt", filepath.Join(dir, "Show E03.en"), d)
	if body, _ := os.ReadFile(filepath.Join(dir, "Show E03.en.vtt")); err != nil || !bytes.Equal(body, vtt) {
		t.Errorf("the original was not kept as .vtt: %v", err)
	}
	if len(d.notes) != 1 || !strings.Contains(d.notes[0], "kept as vtt") {
		t.Errorf("notes = %q", d.notes)
	}
}
