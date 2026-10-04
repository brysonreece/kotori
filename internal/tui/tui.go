// Package tui is the interactive front end: a short wizard that finds a
// series and collects whatever download settings were not given as flags.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/brysonreece/kotori/internal/anikoto"
)

// ErrCancelled is returned when the user quits before finishing.
var ErrCancelled = errors.New("cancelled")

// Site is the part of the site the wizard needs.
type Site interface {
	Search(ctx context.Context, query string) ([]anikoto.Result, error)
	Load(ctx context.Context, pageURL string) (*anikoto.Series, error)
}

// Choices are the download settings the wizard can collect.
type Choices struct {
	// Episodes is a selection such as "1,3,5-8"; empty means all.
	Episodes string
	Audio    string
	Quality  int
	Format   string
	// Subtitles is a subtitle format, or "none".
	Subtitles string
	// Path is the folder to save into.
	Path string
	// Naming is the file name pattern.
	Naming string
}

// Option is one entry of a pick list.
type Option struct {
	Value string
	// Label is shown in place of the value, when the value itself is not
	// something to read.
	Label string
	// Note is a short remark shown beside the value.
	Note string
}

// Config says where the wizard starts and which steps it shows.
type Config struct {
	Site Site
	// Series, when set, skips the search.
	Series *anikoto.Series
	// Query, when set, is searched for straight away.
	Query string

	// Each Ask field enables one step. A step that is off keeps its default.
	AskEpisodes  bool
	AskAudio     bool
	AskQuality   bool
	AskFormat    bool
	AskSubtitles bool
	AskPath      bool
	AskNaming    bool
	Defaults     Choices

	Qualities       []int
	Formats         []Option
	SubtitleFormats []Option
	// Namings are the ready-made file name patterns. The wizard adds a
	// choice to write a custom one.
	Namings []Option
	// NamingHint explains what a custom pattern can contain.
	NamingHint string
	// Preview shows the file a pattern would produce for the chosen series
	// and settings, or returns why the pattern cannot be used.
	Preview func(pattern string, series *anikoto.Series, c Choices) (string, error)
}

// Outcome is what the user settled on.
type Outcome struct {
	Series *anikoto.Series
	Choices
}

// Run shows the wizard and returns the outcome. If there is nothing to ask,
// it returns at once without touching the terminal.
func Run(ctx context.Context, cfg Config) (*Outcome, error) {
	m := newModel(ctx, cfg)
	if m.step != stepDone {
		// The wizard draws on stderr, so stdout stays clean for piping.
		lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(os.Stderr))
		final, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(os.Stderr)).Run()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ErrCancelled
			}
			return nil, err
		}
		m = final.(model)
	}
	if m.cancelled || m.step != stepDone {
		return nil, ErrCancelled
	}
	return &Outcome{Series: m.series, Choices: m.choices}, nil
}

type step int

const (
	stepSearch step = iota
	stepResults
	stepEpisodes
	stepAudio
	stepQuality
	stepFormat
	stepSubtitles
	stepPath
	stepNaming
	stepDone
)

var stepNames = map[step]string{
	stepEpisodes:  "Episodes",
	stepAudio:     "Audio",
	stepQuality:   "Quality",
	stepFormat:    "Format",
	stepSubtitles: "Subtitles",
	stepPath:      "Save to",
	stepNaming:    "File names",
}

var audioOptions = []Option{
	{Value: "sub", Note: "original audio with subtitles"},
	{Value: "dub", Note: "English audio"},
}

type searchMsg struct {
	query   string
	results []anikoto.Result
	err     error
}

type loadMsg struct {
	series *anikoto.Series
	err    error
}

type model struct {
	ctx context.Context
	cfg Config

	step step
	// busy describes the request in flight; keys are ignored while it is set.
	busy string
	err  error

	spinner spinner.Model
	search  textinput.Model
	// path and naming are the inputs of the folder step and of a custom
	// file name pattern.
	path, naming textinput.Model
	// customizing is set while a custom pattern is being written.
	customizing bool

	results []anikoto.Result
	// picked is the search result the series was loaded from, if any.
	picked *anikoto.Result
	// cursor and offset are the selection and scroll position of the
	// current list.
	cursor, offset int

	series *anikoto.Series
	// selected holds the indexes of the episodes ticked in the episode list.
	selected map[int]bool
	choices  Choices
	// autoAudio is set when the series only has one audio type, so asking
	// would be pointless.
	autoAudio bool

	cancelled     bool
	width, height int
}

func newModel(ctx context.Context, cfg Config) model {
	search := textinput.New()
	search.Placeholder = "show or movie title"
	search.Prompt = "› "
	search.CharLimit = 100
	search.SetValue(cfg.Query)

	input := func(value string) textinput.Model {
		in := textinput.New()
		in.Prompt = "› "
		in.CharLimit = 300
		in.SetValue(value)
		return in
	}

	m := model{
		path:     input(cfg.Defaults.Path),
		naming:   input(cfg.Defaults.Naming),
		ctx:      ctx,
		cfg:      cfg,
		spinner:  spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		search:   search,
		series:   cfg.Series,
		selected: map[int]bool{},
		choices:  cfg.Defaults,
		height:   24,
	}
	switch {
	case cfg.Series != nil:
		m = m.enter(m.next(stepResults))
	case strings.TrimSpace(cfg.Query) != "":
		m.busy = "Searching"
	default:
		m = m.enter(stepSearch)
	}
	return m
}

func (m model) Init() tea.Cmd {
	if m.busy != "" {
		return tea.Batch(m.spinner.Tick, m.searchCmd(m.cfg.Query))
	}
	return textinput.Blink
}

func (m model) searchCmd(query string) tea.Cmd {
	return func() tea.Msg {
		results, err := m.cfg.Site.Search(m.ctx, query)
		return searchMsg{query: query, results: results, err: err}
	}
}

func (m model) loadCmd(pageURL string) tea.Cmd {
	return func() tea.Msg {
		series, err := m.cfg.Site.Load(m.ctx, pageURL)
		return loadMsg{series: series, err: err}
	}
}

// asks reports whether a settings step should be shown.
func (m model) asks(s step) bool {
	switch s {
	case stepEpisodes:
		// A movie or one-off has nothing to choose between.
		return m.cfg.AskEpisodes && m.series != nil && len(m.series.Episodes) > 1
	case stepAudio:
		return m.cfg.AskAudio && !m.autoAudio
	case stepQuality:
		return m.cfg.AskQuality
	case stepFormat:
		return m.cfg.AskFormat
	case stepSubtitles:
		return m.cfg.AskSubtitles
	case stepPath:
		return m.cfg.AskPath
	case stepNaming:
		return m.cfg.AskNaming
	}
	return false
}

func (m model) next(from step) step {
	for s := from + 1; s < stepDone; s++ {
		if m.asks(s) {
			return s
		}
	}
	return stepDone
}

// previous finds the step that esc returns to: the settings step before
// this one, or the search results if that is how the series was chosen.
func (m model) previous(from step) (step, bool) {
	for s := from - 1; s >= stepEpisodes; s-- {
		if m.asks(s) {
			return s, true
		}
	}
	if m.picked != nil {
		return stepResults, true
	}
	return from, false
}

// options lists the entries of a pick-list step, and which one is current.
func (m model) options(s step) ([]Option, string) {
	switch s {
	case stepAudio:
		return audioOptions, m.choices.Audio
	case stepQuality:
		opts := make([]Option, len(m.cfg.Qualities))
		for i, q := range m.cfg.Qualities {
			opts[i] = Option{Value: strconv.Itoa(q)}
		}
		return opts, strconv.Itoa(m.choices.Quality)
	case stepFormat:
		return m.cfg.Formats, m.choices.Format
	case stepSubtitles:
		return m.cfg.SubtitleFormats, m.choices.Subtitles
	}
	return nil, ""
}

// enter moves to a step and puts the cursor and focus where they belong.
func (m model) enter(s step) model {
	m.step, m.err = s, nil
	m.search.Blur()
	m.path.Blur()
	m.naming.Blur()
	m.customizing = false
	m.cursor, m.offset = 0, 0
	switch s {
	case stepSearch:
		m.search.Focus()
		m.search.CursorEnd()
	case stepResults:
		for i := range m.results {
			if m.picked != nil && m.results[i] == *m.picked {
				m.cursor = i
			}
		}
	case stepPath:
		m.path.Focus()
		m.path.CursorEnd()
	case stepNaming:
		// A pattern that is not one of the ready-made ones is a custom one.
		m.cursor = len(m.cfg.Namings)
		for i, o := range m.cfg.Namings {
			if o.Value == m.choices.Naming {
				m.cursor = i
			}
		}
	case stepEpisodes:
		// Coming back to the list, start at the first ticked episode.
		for i := len(m.series.Episodes) - 1; i >= 0; i-- {
			if m.selected[i] {
				m.cursor = i
			}
		}
	default:
		opts, current := m.options(s)
		for i, o := range opts {
			if o.Value == current {
				m.cursor = i
			}
		}
	}
	return m.scroll()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Some terminals report no size; keep the default rather than
		// shrinking the list to nothing.
		if msg.Width > 0 && msg.Height > 0 {
			m.width, m.height = msg.Width, msg.Height
		}
		return m.scroll(), nil

	case spinner.TickMsg:
		if m.busy == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case searchMsg:
		m.busy = ""
		switch {
		case msg.err != nil:
			m = m.enter(stepSearch)
			m.err = msg.err
		case len(msg.results) == 0:
			m = m.enter(stepSearch)
			m.err = fmt.Errorf("nothing found for %q", msg.query)
		default:
			m.results, m.picked = msg.results, nil
			m = m.enter(stepResults)
		}
		return m, textinput.Blink

	case loadMsg:
		m.busy = ""
		if msg.err != nil {
			m.picked = nil
			m.err = msg.err
			return m, nil
		}
		m.series, m.selected = msg.series, map[int]bool{}
		m.choices.Audio, m.autoAudio = m.cfg.Defaults.Audio, false
		if m.cfg.AskAudio && (m.picked.Sub > 0) != (m.picked.Dub > 0) {
			m.autoAudio = true
			m.choices.Audio = "sub"
			if m.picked.Dub > 0 {
				m.choices.Audio = "dub"
			}
		}
		return m.advance()

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.cancelled = true
			return m, tea.Quit
		}
		if m.busy != "" {
			return m, nil
		}
		return m.key(msg)
	}

	// Anything else, such as a cursor blink, belongs to the focused input.
	var cmd tea.Cmd
	switch {
	case m.step == stepSearch:
		m.search, cmd = m.search.Update(msg)
	case m.step == stepPath:
		m.path, cmd = m.path.Update(msg)
	case m.customizing:
		m.naming, cmd = m.naming.Update(msg)
	}
	return m, cmd
}

func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.step {
	case stepSearch:
		if msg.Type != tea.KeyEnter {
			m.search, cmd = m.search.Update(msg)
			return m, cmd
		}
		query := strings.TrimSpace(m.search.Value())
		if query == "" {
			return m, nil
		}
		m.busy, m.err = "Searching", nil
		return m, tea.Batch(m.spinner.Tick, m.searchCmd(query))

	case stepResults:
		if moved, ok := m.navigate(msg.String()); ok {
			return moved, nil
		}
		switch msg.String() {
		case "esc", "/":
			return m.enter(stepSearch), textinput.Blink
		case "enter":
			picked := m.results[m.cursor]
			m.picked, m.err = &picked, nil
			m.busy = "Loading " + picked.Title
			return m, tea.Batch(m.spinner.Tick, m.loadCmd(picked.URL))
		}
		return m, nil

	case stepEpisodes:
		count := len(m.series.Episodes)
		if moved, ok := m.navigate(msg.String()); ok {
			return moved, nil
		}
		m.err = nil
		switch msg.String() {
		case " ":
			// Ticking moves on to the next row, so a run of episodes is
			// one key held down.
			m.toggle(m.cursor)
			m.cursor = min(m.cursor+1, count-1)
			m = m.scroll()
		case "a":
			all := len(m.selected) == count
			for i := 0; i < count; i++ {
				if m.selected[i] == all {
					m.toggle(i)
				}
			}
		case "esc":
			return m.back()
		case "enter":
			if len(m.selected) == 0 {
				m.err = errors.New("nothing selected: press space to tick episodes, or a for all of them")
				return m, nil
			}
			m.choices.Episodes = episodeSpec(m.selected, count)
			return m.advance()
		}
		return m, nil
	}

	switch m.step {
	case stepPath:
		switch msg.Type {
		case tea.KeyEsc:
			return m.back()
		case tea.KeyEnter:
			// An empty answer means the default folder.
			if m.choices.Path = strings.TrimSpace(m.path.Value()); m.choices.Path == "" {
				m.choices.Path = m.cfg.Defaults.Path
			}
			return m.advance()
		}
		m.path, cmd = m.path.Update(msg)
		return m, cmd

	case stepNaming:
		if m.customizing {
			switch msg.Type {
			case tea.KeyEsc:
				m.customizing, m.err = false, nil
				m.naming.Blur()
				return m, nil
			case tea.KeyEnter:
				pattern := strings.TrimSpace(m.naming.Value())
				if _, err := m.preview(pattern); err != nil {
					m.err = err
					return m, nil
				}
				m.choices.Naming = pattern
				return m.advance()
			}
			m.err = nil
			m.naming, cmd = m.naming.Update(msg)
			return m, cmd
		}
		if moved, ok := m.navigate(msg.String()); ok {
			return moved, nil
		}
		switch msg.String() {
		case "esc":
			return m.back()
		case "enter":
			if m.cursor < len(m.cfg.Namings) {
				m.choices.Naming = m.cfg.Namings[m.cursor].Value
				return m.advance()
			}
			m.customizing = true
			m.naming.SetValue(m.choices.Naming)
			m.naming.Focus()
			m.naming.CursorEnd()
			return m, textinput.Blink
		}
		return m, nil
	}

	if moved, ok := m.navigate(msg.String()); ok {
		return moved, nil
	}
	opts, _ := m.options(m.step)
	switch msg.String() {
	case "esc":
		return m.back()
	case "enter":
		value := opts[m.cursor].Value
		switch m.step {
		case stepAudio:
			m.choices.Audio = value
		case stepQuality:
			m.choices.Quality, _ = strconv.Atoi(value)
		case stepFormat:
			m.choices.Format = value
		case stepSubtitles:
			m.choices.Subtitles = value
		}
		return m.advance()
	}
	return m, nil
}

func (m model) advance() (tea.Model, tea.Cmd) {
	m = m.enter(m.next(max(m.step, stepResults)))
	if m.step == stepDone {
		return m, tea.Quit
	}
	return m, textinput.Blink
}

func (m model) back() (tea.Model, tea.Cmd) {
	if s, ok := m.previous(m.step); ok {
		m = m.enter(s)
	}
	return m, textinput.Blink
}

// listLen is the number of rows in the current step's list.
func (m model) listLen() int {
	switch m.step {
	case stepResults:
		return len(m.results)
	case stepEpisodes:
		return len(m.series.Episodes)
	case stepNaming:
		return len(m.cfg.Namings) + 1 // the ready-made patterns, then custom
	}
	opts, _ := m.options(m.step)
	return len(opts)
}

// preview shows the file a pattern would produce, or why it cannot be used.
func (m model) preview(pattern string) (string, error) {
	if m.cfg.Preview == nil {
		return pattern, nil
	}
	return m.cfg.Preview(pattern, m.series, m.choices)
}

// navigate handles the keys that move through a list. Single steps wrap
// around; page and end jumps stop at the edges. ok is false for other keys.
func (m model) navigate(key string) (moved model, ok bool) {
	count := m.listLen()
	if count == 0 {
		return m, false
	}
	switch key {
	case "up", "k":
		m.cursor = (m.cursor - 1 + count) % count
	case "down", "j":
		m.cursor = (m.cursor + 1) % count
	case "pgup", "ctrl+u":
		m.cursor = max(m.cursor-m.visible(), 0)
	case "pgdown", "ctrl+d":
		m.cursor = min(m.cursor+m.visible(), count-1)
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = count - 1
	default:
		return m, false
	}
	return m.scroll(), true
}

// toggle ticks or unticks an episode. Unticked episodes are removed so the
// size of the set is the number ticked.
func (m model) toggle(i int) {
	if m.selected[i] {
		delete(m.selected, i)
	} else {
		m.selected[i] = true
	}
}

// episodeSpec writes the ticked episodes as a selection such as "1-3,5",
// or as the empty string when every episode is ticked.
func episodeSpec(selected map[int]bool, count int) string {
	if len(selected) == count {
		return ""
	}
	var parts []string
	for i := 0; i < count; i++ {
		if !selected[i] {
			continue
		}
		start := i
		for i+1 < count && selected[i+1] {
			i++
		}
		if start == i {
			parts = append(parts, strconv.Itoa(start+1))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", start+1, i+1))
		}
	}
	return strings.Join(parts, ",")
}

// visible is how many list rows fit on screen beside the rest of the UI.
func (m model) visible() int {
	return max(3, min(m.listLen(), m.height-8))
}

// scroll keeps the cursor inside the visible window of the list.
func (m model) scroll() model {
	visible := m.visible()
	m.offset = max(min(m.offset, m.cursor), m.cursor-visible+1)
	m.offset = max(0, min(m.offset, m.listLen()-visible))
	return m
}

var (
	accent  = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	faint   = lipgloss.NewStyle().Faint(true)
	success = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	failure = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

func (m model) View() string {
	if m.cancelled {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.summary())
	if m.step == stepDone {
		return b.String()
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}

	help := "enter select · esc back · ctrl+c quit"
	switch {
	case m.busy != "":
		fmt.Fprintf(&b, "%s %s…\n", m.spinner.View(), m.busy)
		help = "ctrl+c quit"
	case m.step == stepSearch:
		fmt.Fprintf(&b, "%s\n%s\n", accent.Render("Search"), m.search.View())
		help = "enter search · ctrl+c quit"
	case m.step == stepResults:
		fmt.Fprintf(&b, "%s %s\n", accent.Render("Results"), faint.Render(fmt.Sprintf("for %q", m.search.Value())))
		b.WriteString(m.resultsView())
		help = "↑/↓ move · enter select · esc search again · ctrl+c quit"
	case m.step == stepEpisodes:
		b.WriteString(accent.Render("Episodes") + "\n")
		b.WriteString(m.episodesView())
		help = "↑/↓ move · space tick · a all/none · enter confirm · esc back · ctrl+c quit"
	case m.step == stepPath:
		fmt.Fprintf(&b, "%s %s\n%s\n", accent.Render("Save to"), faint.Render("the folder downloads go in"), m.path.View())
		help = "enter confirm · esc back · ctrl+c quit"
	case m.step == stepNaming && m.customizing:
		fmt.Fprintf(&b, "%s %s\n%s\n", accent.Render("File names"), faint.Render(m.cfg.NamingHint), m.naming.View())
		if example, err := m.preview(strings.TrimSpace(m.naming.Value())); err == nil {
			b.WriteString(faint.Render("  "+m.fit(example, 4)) + "\n")
		}
		help = "enter confirm · esc back to the list · ctrl+c quit"
	case m.step == stepNaming:
		b.WriteString(accent.Render("File names") + "\n")
		// Labels are padded to one width so the examples line up.
		width := len("Custom")
		for _, o := range m.cfg.Namings {
			width = max(width, len([]rune(o.Label)))
		}
		for i, o := range m.cfg.Namings {
			example, _ := m.preview(o.Value)
			b.WriteString(row(i == m.cursor, fmt.Sprintf("%-*s", width, o.Label), m.fit(example, width+6)))
		}
		b.WriteString(row(m.cursor == len(m.cfg.Namings), fmt.Sprintf("%-*s", width, "Custom"), "write your own pattern"))
		help = "↑/↓ move · " + help
	default:
		title := accent.Render(stepNames[m.step])
		if m.step == stepQuality {
			title += " " + faint.Render("best available up to")
		}
		b.WriteString(title + "\n")
		opts, _ := m.options(m.step)
		for i, o := range opts {
			label := o.Value
			if m.step == stepQuality {
				label += "p"
			}
			b.WriteString(row(i == m.cursor, label, o.Note))
		}
		help = "↑/↓ move · " + help
	}
	if m.err != nil {
		b.WriteString(failure.Render(m.err.Error()) + "\n")
	}
	b.WriteString("\n" + faint.Render(help) + "\n")
	return b.String()
}

// summary lists what has been settled so far. It is also what stays on
// screen once the wizard has finished.
func (m model) summary() string {
	if m.series == nil || m.step <= stepResults || m.busy != "" {
		return ""
	}
	line := func(name, value string) string {
		return fmt.Sprintf("%s %-9s %s\n", success.Render("✓"), name, value)
	}
	var b strings.Builder
	b.WriteString(line("Show", m.series.Title))
	for s := stepEpisodes; s < m.step; s++ {
		switch {
		case s == stepAudio && m.autoAudio:
			b.WriteString(line("Audio", m.choices.Audio+faint.Render(" (the only one available)")))
		case !m.asks(s):
		case s == stepEpisodes && m.choices.Episodes == "":
			b.WriteString(line("Episodes ", fmt.Sprintf("all %d", len(m.series.Episodes))))
		case s == stepEpisodes:
			b.WriteString(line("Episodes ", m.choices.Episodes))
		case s == stepAudio:
			b.WriteString(line("Audio ", m.choices.Audio))
		case s == stepQuality:
			b.WriteString(line("Quality ", fmt.Sprintf("up to %dp", m.choices.Quality)))
		case s == stepFormat:
			b.WriteString(line("Format ", m.choices.Format))
		case s == stepSubtitles:
			b.WriteString(line("Subtitles ", m.choices.Subtitles))
		case s == stepPath:
			b.WriteString(line("Save to ", m.choices.Path))
		case s == stepNaming:
			example, _ := m.preview(m.choices.Naming)
			b.WriteString(line("Names ", example))
		}
	}
	return b.String()
}

func (m model) resultsView() string {
	var b strings.Builder
	end := min(m.offset+m.visible(), len(m.results))
	for i := m.offset; i < end; i++ {
		r := m.results[i]
		var meta []string
		if r.Kind != "" {
			meta = append(meta, r.Kind)
		}
		if r.Sub > 0 {
			meta = append(meta, fmt.Sprintf("%d sub", r.Sub))
		}
		if r.Dub > 0 {
			meta = append(meta, fmt.Sprintf("%d dub", r.Dub))
		}
		note := strings.Join(meta, " · ")
		// Leave room for the cursor, the note and a little padding.
		b.WriteString(row(i == m.cursor, m.fit(r.Title, len([]rune(note))+6), note))
	}
	b.WriteString(faint.Render(fmt.Sprintf("  %d of %d", m.cursor+1, len(m.results))) + "\n")
	return b.String()
}

func (m model) episodesView() string {
	var b strings.Builder
	eps := m.series.Episodes
	width := len(strconv.Itoa(len(eps)))
	end := min(m.offset+m.visible(), len(eps))
	for i := m.offset; i < end; i++ {
		box := "○"
		if m.selected[i] {
			box = success.Render("●")
		}
		label := fmt.Sprintf("%*d  %s", width, eps[i].Number, m.fit(eps[i].Title, width+8))
		if i == m.cursor {
			fmt.Fprintf(&b, "%s %s %s\n", accent.Render("›"), box, accent.Render(label))
		} else {
			fmt.Fprintf(&b, "  %s %s\n", box, label)
		}
	}
	b.WriteString(faint.Render(fmt.Sprintf("  %d of %d · %d selected", m.cursor+1, len(eps), len(m.selected))) + "\n")
	return b.String()
}

// fit shortens text so that it and reserved more columns fit the terminal.
func (m model) fit(text string, reserved int) string {
	room := m.width - reserved
	if runes := []rune(text); m.width > 0 && room > 10 && len(runes) > room {
		return string(runes[:room-1]) + "…"
	}
	return text
}

func row(selected bool, label, note string) string {
	if note != "" {
		note = "  " + faint.Render(note)
	}
	if selected {
		return accent.Render("› "+label) + note + "\n"
	}
	return "  " + label + note + "\n"
}
