package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/brysonreece/kotori/internal/anikoto"
)

type fakeSite struct {
	results []anikoto.Result
	series  map[string]*anikoto.Series
}

func (f *fakeSite) Search(_ context.Context, query string) ([]anikoto.Result, error) {
	if query == "boom" {
		return nil, errors.New("site is down")
	}
	if query == "nothing" {
		return nil, nil
	}
	return f.results, nil
}

func (f *fakeSite) Load(_ context.Context, pageURL string) (*anikoto.Series, error) {
	if s, ok := f.series[pageURL]; ok {
		return s, nil
	}
	return nil, errors.New("no such series")
}

func episodes(n int) []anikoto.Episode {
	eps := make([]anikoto.Episode, n)
	for i := range eps {
		eps[i].Number = i + 1
	}
	return eps
}

var (
	show  = &anikoto.Series{Title: "My Show", Episodes: episodes(12)}
	movie = &anikoto.Series{Title: "My Movie", Episodes: episodes(1)}
)

func config() Config {
	return Config{
		Site: &fakeSite{
			results: []anikoto.Result{
				{Title: "My Show", URL: "u/show", Kind: "TV", Sub: 12, Dub: 12},
				{Title: "My Movie", URL: "u/movie", Kind: "Movie", Sub: 1},
			},
			series: map[string]*anikoto.Series{"u/show": show, "u/movie": movie},
		},
		AskEpisodes: true, AskAudio: true, AskQuality: true, AskFormat: true,
		Defaults:  Choices{Audio: "dub", Quality: 1080, Format: "mp4"},
		Qualities: []int{2160, 1080, 720},
		Formats:   []Option{{Value: "mkv"}, {Value: "mp4"}, {Value: "avi", Note: "re-encoded, slow"}},
	}
}

// drive feeds keys and messages to the model. A string is typed one rune at
// a time, and the names below press the matching key. Requests are answered
// by running the command the model would have started.
func drive(t *testing.T, m model, inputs ...any) model {
	t.Helper()
	keys := map[string]tea.KeyType{"enter": tea.KeyEnter, "esc": tea.KeyEsc, "up": tea.KeyUp, "down": tea.KeyDown, "ctrl+c": tea.KeyCtrlC}
	send := func(msg tea.Msg) {
		next, _ := m.Update(msg)
		m = next.(model)
	}
	for _, in := range inputs {
		switch v := in.(type) {
		case string:
			if key, ok := keys[v]; ok {
				send(tea.KeyMsg{Type: key})
				break
			}
			for _, r := range v {
				send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			}
		case tea.Msg:
			send(v)
		}
		// Answer a request the way the runtime would.
		if strings.HasPrefix(m.busy, "Searching") {
			send(m.searchCmd(strings.TrimSpace(m.search.Value()))())
		} else if strings.HasPrefix(m.busy, "Loading") {
			send(m.loadCmd(m.picked.URL)())
		}
	}
	return m
}

func TestFullFlow(t *testing.T) {
	m := newModel(context.Background(), config())
	if m.step != stepSearch {
		t.Fatalf("starts at step %d, want search", m.step)
	}
	m = drive(t, m, "my show", "enter")
	if m.step != stepResults || len(m.results) != 2 {
		t.Fatalf("after searching: step %d with %d results", m.step, len(m.results))
	}
	m = drive(t, m, "enter")
	if m.step != stepEpisodes || m.series != show {
		t.Fatalf("after picking: step %d, series %+v", m.step, m.series)
	}

	// Confirming with nothing ticked is refused and explained.
	m = drive(t, m, "enter")
	if m.step != stepEpisodes || m.err == nil {
		t.Fatalf("empty selection: step %d, err %v", m.step, m.err)
	}
	// Five presses of space tick episodes 1 to 5, moving down each time.
	m = drive(t, m, "     ")
	if m.cursor != 5 || !strings.Contains(m.View(), "6 of 12 · 5 selected") {
		t.Fatalf("after ticking: cursor %d\n%s", m.cursor, m.View())
	}
	m = drive(t, m, "enter")
	if m.step != stepAudio || m.cursor != 1 {
		t.Fatalf("audio step: step %d, cursor %d (want the dub default)", m.step, m.cursor)
	}
	m = drive(t, m, "up", "enter")
	if m.step != stepQuality || m.cursor != 1 {
		t.Fatalf("quality step: step %d, cursor %d (want the 1080 default)", m.step, m.cursor)
	}
	m = drive(t, m, "down", "enter", "up", "enter")

	want := Choices{Episodes: "1-5", Audio: "sub", Quality: 720, Format: "mkv"}
	if m.step != stepDone || m.choices != want {
		t.Fatalf("finished at step %d with %+v, want %+v", m.step, m.choices, want)
	}
	for _, text := range []string{"My Show", "1-5", "sub", "up to 720p", "mkv"} {
		if !strings.Contains(m.View(), text) {
			t.Errorf("summary is missing %q:\n%s", text, m.View())
		}
	}
}

func TestMovieSkipsEpisodesAndSingleAudio(t *testing.T) {
	m := drive(t, newModel(context.Background(), config()), "my", "enter", "down", "enter")
	if m.step != stepQuality {
		t.Fatalf("step %d, want quality: one episode and sub only leave nothing else to ask", m.step)
	}
	if m.choices.Audio != "sub" || !m.autoAudio {
		t.Errorf("audio %q, auto %v; want the only available type", m.choices.Audio, m.autoAudio)
	}
	if !strings.Contains(m.View(), "only one available") {
		t.Errorf("view does not explain the audio choice:\n%s", m.View())
	}
}

func TestKnownSeriesSkipsSearchAndGivenSettings(t *testing.T) {
	cfg := config()
	cfg.Series = show
	cfg.AskEpisodes, cfg.AskQuality = false, false
	cfg.Defaults.Episodes, cfg.Defaults.Quality = "1-5", 720

	m := newModel(context.Background(), cfg)
	if m.step != stepAudio {
		t.Fatalf("starts at step %d, want audio", m.step)
	}
	m = drive(t, m, "enter", "enter")
	want := Choices{Episodes: "1-5", Audio: "dub", Quality: 720, Format: "mp4"}
	if m.step != stepDone || m.choices != want {
		t.Fatalf("finished at step %d with %+v, want %+v", m.step, m.choices, want)
	}
}

func TestNothingToAsk(t *testing.T) {
	cfg := config()
	cfg.Series = show
	cfg.AskEpisodes, cfg.AskAudio, cfg.AskQuality, cfg.AskFormat = false, false, false, false

	outcome, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Series != show || outcome.Choices != cfg.Defaults {
		t.Errorf("got %+v", outcome)
	}
}

func TestInitialQuerySearchesStraightAway(t *testing.T) {
	cfg := config()
	cfg.Query = "my show"
	m := newModel(context.Background(), cfg)
	if m.busy == "" || m.Init() == nil {
		t.Fatal("a given query should start a search")
	}
	m = drive(t, m, m.searchCmd(cfg.Query)())
	if m.step != stepResults {
		t.Fatalf("step %d, want results", m.step)
	}
}

func TestSearchProblemsStayOnSearch(t *testing.T) {
	for query, want := range map[string]string{"nothing": "nothing found", "boom": "site is down"} {
		m := drive(t, newModel(context.Background(), config()), query, "enter")
		if m.step != stepSearch || m.err == nil || !strings.Contains(m.View(), want) {
			t.Errorf("%q: step %d, err %v", query, m.step, m.err)
		}
	}
}

func TestBackAndCancel(t *testing.T) {
	m := drive(t, newModel(context.Background(), config()), "my", "enter", "enter", "down", "down", " ", "enter")
	if m.step != stepAudio {
		t.Fatalf("step %d, want audio", m.step)
	}
	m = drive(t, m, "esc")
	if m.step != stepEpisodes || m.cursor != 2 || !m.selected[2] {
		t.Fatalf("esc went to step %d at row %d, want episodes at the ticked row", m.step, m.cursor)
	}
	m = drive(t, m, "esc")
	if m.step != stepResults {
		t.Fatalf("esc went to step %d, want results", m.step)
	}
	m = drive(t, m, "esc")
	if m.step != stepSearch {
		t.Fatalf("esc went to step %d, want search", m.step)
	}

	m = drive(t, m, "ctrl+c")
	if !m.cancelled || m.View() != "" {
		t.Error("ctrl+c should cancel and clear the view")
	}
}

func TestResultsScroll(t *testing.T) {
	cfg := config()
	site := cfg.Site.(*fakeSite)
	site.results = nil
	for i := 0; i < 30; i++ {
		site.results = append(site.results, anikoto.Result{Title: "Show", URL: "u"})
	}
	m := drive(t, newModel(context.Background(), cfg), tea.WindowSizeMsg{Width: 80, Height: 14}, "x", "enter")
	if got := m.visible(); got != 6 {
		t.Fatalf("%d visible results, want 6", got)
	}
	at := func(step string, cursor, offset int) {
		t.Helper()
		if m.cursor != cursor || m.offset != offset {
			t.Errorf("%s: cursor %d, offset %d; want %d and %d", step, m.cursor, m.offset, cursor, offset)
		}
		// The position shown is the highlighted row, not the rows on screen.
		if want := fmt.Sprintf("  %d of 30", cursor+1); !strings.Contains(m.View(), want) {
			t.Errorf("%s: view does not show %q:\n%s", step, want, m.View())
		}
	}
	at("start", 0, 0)
	m = drive(t, m, "down", "down")
	at("two down", 2, 0)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	at("page down", 8, 3)
	m = drive(t, m, "up")
	at("up inside the window", 7, 3)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	at("end", 29, 24)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	at("page down at the end", 29, 24)
	m = drive(t, m, "down")
	at("wrapping down", 0, 0)
	m = drive(t, m, "up")
	at("wrapping up", 29, 24)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyPgUp}, tea.KeyMsg{Type: tea.KeyHome})
	at("home", 0, 0)
}

func TestEpisodeSelection(t *testing.T) {
	cfg := config()
	cfg.Series = show
	start := func() model { return newModel(context.Background(), cfg) }
	if m := start(); m.step != stepEpisodes {
		t.Fatalf("starts at step %d, want episodes", m.step)
	}

	// Everything ticked is recorded as "all", not as a list.
	m := drive(t, start(), "a")
	if len(m.selected) != 12 || !strings.Contains(m.View(), "12 selected") {
		t.Fatalf("a ticked %d of 12", len(m.selected))
	}
	if done := drive(t, m, "enter"); done.step != stepAudio || done.choices.Episodes != "" {
		t.Errorf("all: step %d, episodes %q", done.step, done.choices.Episodes)
	}

	// A second a unticks everything again.
	if m := drive(t, start(), "a", "a"); len(m.selected) != 0 {
		t.Errorf("a twice left %d ticked", len(m.selected))
	}
	// With some ticked, a fills in the rest.
	if m := drive(t, start(), " ", "a"); len(m.selected) != 12 {
		t.Errorf("a after one tick left %d ticked", len(m.selected))
	}

	// Scattered ticks, including an untick, become a compact selection.
	m = drive(t, start(), "   ", "down", " ", tea.KeyMsg{Type: tea.KeyEnd}, " ", "up", " ", "up", " ", "enter")
	if m.choices.Episodes != "1-3,5,12" {
		t.Errorf("got %q, want 1-3,5,12", m.choices.Episodes)
	}
	if !strings.Contains(m.View(), "1-3,5,12") {
		t.Errorf("summary does not show the selection:\n%s", m.View())
	}
}

func TestEpisodeListScrolls(t *testing.T) {
	cfg := config()
	cfg.Series = &anikoto.Series{Title: "Long Show", Episodes: episodes(291)}
	m := drive(t, newModel(context.Background(), cfg), tea.WindowSizeMsg{Width: 80, Height: 18}, tea.KeyMsg{Type: tea.KeyEnd})
	if m.cursor != 290 || m.offset != 281 {
		t.Errorf("cursor %d, offset %d; want 290 and 281", m.cursor, m.offset)
	}
	view := m.View()
	if !strings.Contains(view, "291 of 291 · 0 selected") || strings.Count(view, "○") != 10 {
		t.Errorf("unexpected view:\n%s", view)
	}
}

func TestSubtitlesStep(t *testing.T) {
	cfg := config()
	cfg.Series = movie
	cfg.AskAudio, cfg.AskQuality, cfg.AskFormat = false, false, false
	cfg.AskSubtitles = true
	cfg.Defaults.Subtitles = "srt"
	cfg.SubtitleFormats = []Option{{Value: "vtt"}, {Value: "srt"}, {Value: "ass"}, {Value: "none", Note: "don't download subtitles"}}

	m := newModel(context.Background(), cfg)
	if m.step != stepSubtitles || m.cursor != 1 {
		t.Fatalf("step %d, cursor %d; want the subtitles step on the srt default", m.step, m.cursor)
	}
	if !strings.Contains(m.View(), "Subtitles") || !strings.Contains(m.View(), "don't download subtitles") {
		t.Errorf("unexpected view:\n%s", m.View())
	}
	m = drive(t, m, "down", "down", "enter")
	if m.step != stepDone || m.choices.Subtitles != "none" {
		t.Errorf("step %d, subtitles %q", m.step, m.choices.Subtitles)
	}
	if !regexp.MustCompile(`Subtitles\s+none`).MatchString(m.View()) {
		t.Errorf("summary does not show the choice:\n%s", m.View())
	}
}

func pathAndNaming() Config {
	cfg := config()
	cfg.Series = show
	cfg.AskEpisodes, cfg.AskAudio, cfg.AskQuality, cfg.AskFormat = false, false, false, false
	cfg.AskPath, cfg.AskNaming = true, true
	cfg.Defaults.Path, cfg.Defaults.Naming = ".", "{series}/{episode}"
	cfg.Namings = []Option{
		{Label: "Folder per show", Value: "{series}/{episode}"},
		{Label: "No folders", Value: "{series} {episode}"},
	}
	cfg.NamingHint = "use {series} and {episode}"
	cfg.Preview = func(pattern string, series *anikoto.Series, c Choices) (string, error) {
		if !strings.Contains(pattern, "{episode}") {
			return "", errors.New("needs {episode}")
		}
		r := strings.NewReplacer("{series}", series.Title, "{episode}", "01")
		return r.Replace(pattern) + "." + c.Format, nil
	}
	return cfg
}

func TestPathStep(t *testing.T) {
	m := newModel(context.Background(), pathAndNaming())
	if m.step != stepPath || m.path.Value() != "." {
		t.Fatalf("step %d with %q, want the folder step holding the default", m.step, m.path.Value())
	}
	// Letters that move the cursor in a list are just text here.
	m.path.SetValue("")
	m = drive(t, m, "~/Anime/jkg", "enter")
	if m.step != stepNaming || m.choices.Path != "~/Anime/jkg" {
		t.Fatalf("step %d, path %q", m.step, m.choices.Path)
	}

	// Clearing the folder falls back to the default.
	m = drive(t, m, "esc")
	m.path.SetValue("  ")
	if m = drive(t, m, "enter"); m.choices.Path != "." {
		t.Errorf("empty path became %q, want the default", m.choices.Path)
	}
}

func TestNamingPresets(t *testing.T) {
	m := drive(t, newModel(context.Background(), pathAndNaming()), "enter")
	if m.step != stepNaming || m.cursor != 0 {
		t.Fatalf("step %d, cursor %d; want the file names step on the default", m.step, m.cursor)
	}
	// Each ready-made pattern is shown by what it produces.
	for _, text := range []string{"Folder per show", "My Show/01.mp4", "No folders", "My Show 01.mp4", "Custom"} {
		if !strings.Contains(m.View(), text) {
			t.Errorf("view is missing %q:\n%s", text, m.View())
		}
	}
	m = drive(t, m, "down", "enter")
	if m.step != stepDone || m.choices.Naming != "{series} {episode}" {
		t.Fatalf("step %d, naming %q", m.step, m.choices.Naming)
	}
	if !strings.Contains(m.View(), "My Show 01.mp4") {
		t.Errorf("summary does not show the file name:\n%s", m.View())
	}
}

func TestCustomNaming(t *testing.T) {
	m := drive(t, newModel(context.Background(), pathAndNaming()), "enter", "up", "enter")
	if !m.customizing || m.naming.Value() != "{series}/{episode}" {
		t.Fatalf("customizing %v with %q; want the current pattern ready to edit", m.customizing, m.naming.Value())
	}
	if !strings.Contains(m.View(), "use {series} and {episode}") || !strings.Contains(m.View(), "My Show/01.mp4") {
		t.Errorf("view lacks the hint or the preview:\n%s", m.View())
	}

	// A pattern that cannot be used is refused and explained.
	m.naming.SetValue("{series}")
	m = drive(t, m, "enter")
	if m.step != stepNaming || m.err == nil || !strings.Contains(m.View(), "needs {episode}") {
		t.Fatalf("bad pattern: step %d, err %v", m.step, m.err)
	}
	// Typing clears the complaint, and the preview follows the text.
	m = drive(t, m, " - {episode}")
	if m.err != nil || !strings.Contains(m.View(), "My Show - 01.mp4") {
		t.Errorf("after typing: err %v\n%s", m.err, m.View())
	}

	// esc leaves the editor for the list, not the whole step.
	if back := drive(t, m, "esc"); back.step != stepNaming || back.customizing {
		t.Errorf("esc: step %d, customizing %v", back.step, back.customizing)
	}
	m = drive(t, m, "enter")
	if m.step != stepDone || m.choices.Naming != "{series} - {episode}" {
		t.Errorf("step %d, naming %q", m.step, m.choices.Naming)
	}
}

func TestCustomPatternIsRemembered(t *testing.T) {
	cfg := pathAndNaming()
	cfg.AskPath = false
	cfg.Defaults.Naming = "{series} - {episode}"
	if m := newModel(context.Background(), cfg); m.cursor != 2 {
		t.Errorf("cursor %d; a pattern that is not ready-made should start on Custom", m.cursor)
	}
}
