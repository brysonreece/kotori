package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brysonreece/kotori/internal/anikoto"
	"github.com/brysonreece/kotori/internal/tui"
)

var dragonBall = &anikoto.Series{
	Title: "Dragon Ball",
	Episodes: []anikoto.Episode{
		{Number: 1, Title: "Bulma and Son Goku"},
		{Number: 2, Title: "What the...?! No Balls!"},
		{Number: 3},
	},
}

func TestValidateTemplate(t *testing.T) {
	for _, preset := range namingPresets {
		if err := validateTemplate(preset.template); err != nil {
			t.Errorf("preset %q: %v", preset.name, err)
		}
	}
	bad := map[string]string{
		"":                      "empty",
		"   ":                   "empty",
		"{series} {title}":      "needs {episode}",
		"{series}/{ep}":         "unknown placeholder {ep}",
		"/Anime/{episode}":      "must be relative",
		"~/Anime/{episode}":     "must be relative",
		"../{series}/{episode}": `".."`,
		"{series}/../{episode}": `".."`,
		"{Series} E{episode}":   "unknown placeholder {Series}",
	}
	for template, want := range bad {
		err := validateTemplate(template)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("validateTemplate(%q) = %v, want an error about %s", template, err, want)
		}
	}
}

func TestExpandTemplate(t *testing.T) {
	eps := dragonBall.Episodes
	cases := []struct {
		template string
		ep       anikoto.Episode
		want     string
	}{
		{defaultTemplate, eps[0], "Dragon Ball/Dragon Ball E01 Bulma and Son Goku"},
		// Characters a file name cannot hold are replaced, never turned into folders.
		{defaultTemplate, eps[1], "Dragon Ball/Dragon Ball E02 What the..._! No Balls!"},
		{namingPresets[1].template, eps[0], "Dragon Ball/Season 01/Dragon Ball - S01E01 - Bulma and Son Goku"},
		// An episode without a title leaves no dangling separator.
		{namingPresets[1].template, eps[2], "Dragon Ball/Season 01/Dragon Ball - S01E03"},
		{defaultTemplate, eps[2], "Dragon Ball/Dragon Ball E03"},
		{"{audio}/{series}/{episode}", eps[0], "sub/Dragon Ball/01"},
		{"{series}//{episode} ", eps[0], "Dragon Ball/01"},
	}
	for _, c := range cases {
		got := filepath.ToSlash(expandTemplate(c.template, dragonBall, c.ep, 2, "sub"))
		if got != c.want {
			t.Errorf("expandTemplate(%q) = %q, want %q", c.template, got, c.want)
		}
	}

	slashes := &anikoto.Series{Title: "Fate/Zero"}
	if got := filepath.ToSlash(expandTemplate("{series}/{episode}", slashes, anikoto.Episode{Number: 7}, 3, "sub")); got != "Fate_Zero/007" {
		t.Errorf("got %q, want Fate_Zero/007", got)
	}
}

func TestBaseUsesPathAndTemplate(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	a := &app{}
	a.opts.path, a.opts.template, a.opts.audio = "~/Anime", "{series}/{audio}/{episode}", "dub"
	want := filepath.Join(home, "Anime", "Dragon Ball", "dub", "01")
	if got := a.base(dragonBall, dragonBall.Episodes[0], 2); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := expandHome("~user/x"); got != "~user/x" {
		t.Errorf("expandHome changed another user's home: %q", got)
	}
}

func TestPreviewTemplate(t *testing.T) {
	// The example is the first episode that would actually be downloaded.
	c := tui.Choices{Episodes: "2-3", Audio: "sub", Format: "mkv"}
	got, err := previewTemplate(defaultTemplate, dragonBall, c)
	if err != nil || filepath.ToSlash(got) != "Dragon Ball/Dragon Ball E02 What the..._! No Balls!.mkv" {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := previewTemplate("{series}", dragonBall, c); err == nil {
		t.Error("expected an error for a pattern without {episode}")
	}
}

func TestOutputFlag(t *testing.T) {
	t.Setenv(baseURLEnv, "")
	opts, err := parseFlags([]string{"x"}, io.Discard)
	if err != nil || opts.template != defaultTemplate || opts.given.naming || opts.given.path {
		t.Fatalf("defaults: %+v, %v", opts, err)
	}
	opts, err = parseFlags([]string{"x", "-o", "{series} - {episode}", "-p", "/tmp/a"}, io.Discard)
	if err != nil || opts.template != "{series} - {episode}" || !opts.given.naming || !opts.given.path {
		t.Fatalf("given: %+v, %v", opts, err)
	}
	if _, err := parseFlags([]string{"x", "-o", "{series}"}, io.Discard); err == nil {
		t.Error("a pattern without {episode} should be refused")
	}
}

func TestCommonDir(t *testing.T) {
	cases := []struct {
		paths []string
		want  string
	}{
		{nil, ""},
		{[]string{"/a/Show/E01.mkv"}, "/a/Show"},
		{[]string{"/a/Show/E01.mkv", "/a/Show/E02.mkv"}, "/a/Show"},
		{[]string{"/a/Show/Season 01/E01.mkv", "/a/Show/Season 02/E01.mkv"}, "/a/Show"},
		{[]string{"/a/Show/E01.mkv", "/a/Showdown/E01.mkv"}, "/a"},
		{[]string{"/a/x.mkv", "/b/y.mkv"}, "/"},
	}
	for _, c := range cases {
		if got := filepath.ToSlash(commonDir(c.paths)); got != c.want {
			t.Errorf("commonDir(%v) = %q, want %q", c.paths, got, c.want)
		}
	}
}

func TestTallyLines(t *testing.T) {
	cases := []struct {
		tally tally
		want  []string
	}{
		{
			tally{downloaded: 5, bytes: 1_420_000_000, paths: []string{"/a/Show/E01.mkv", "/a/Show/E02.mkv"}},
			[]string{"Downloaded 5 episodes · 1.42 GB", "Saved to /a/Show"},
		},
		{
			// A single file is named outright.
			tally{downloaded: 1, bytes: 294_100_000, paths: []string{"/a/Show/E01.mkv"}},
			[]string{"Downloaded 1 episode · 294.1 MB", "Saved to /a/Show/E01.mkv"},
		},
		{
			tally{downloaded: 2, skipped: 3, failed: 1, bytes: 500_000_000, paths: []string{"/a/Show/E01.mkv", "/a/Show/E02.mkv"}},
			[]string{"Downloaded 2 episodes · 500.0 MB · 3 already downloaded · 1 failed", "Saved to /a/Show"},
		},
		{
			tally{skipped: 2, paths: []string{"/a/Show/E01.mkv", "/a/Show/E02.mkv"}},
			[]string{"Nothing downloaded · 2 already downloaded", "Files are in /a/Show"},
		},
		{
			tally{failed: 2},
			[]string{"Nothing downloaded · 2 failed"},
		},
	}
	for _, c := range cases {
		got := c.tally.lines()
		for i := range got {
			got[i] = filepath.ToSlash(got[i])
		}
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
	}
}
