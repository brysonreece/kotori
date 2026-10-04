// Package cli implements the kotori command.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/pflag"

	"github.com/brysonreece/kotori/internal/anikoto"
	"github.com/brysonreece/kotori/internal/fetch"
	"github.com/brysonreece/kotori/internal/ffmpeg"
	"github.com/brysonreece/kotori/internal/throttle"
	"github.com/brysonreece/kotori/internal/tui"
)

const version = "0.1.0"

var qualities = []int{2160, 1440, 1080, 720, 480, 360}

type options struct {
	// url is the series page to load, or empty to search for one.
	url string
	// query is what to search for when there is no url, or when url turns
	// out not to exist.
	query string
	// bareWord is set when the argument could be a slug or a search term.
	bareWord bool
	// origin is the site address, as scheme and host.
	origin string
	// given records which settings came from flags, so the wizard only asks
	// for the rest.
	given struct{ episodes, audio, quality, format, subtitles, path, naming bool }
	// yes skips the settings questions and uses the defaults.
	yes      bool
	quality  int
	audio    string
	format   string
	sources  []string
	episodes string
	last     bool
	list     bool
	path     string
	// template is the file name pattern, relative to path.
	template       string
	subtitles      bool
	subtitleLang   string
	subtitleFormat string
	concurrency    int
	// jobs is how many episodes are downloaded at once.
	jobs  int
	debug bool
}

type app struct {
	opts options
	// interactive is set when a person is at the terminal to answer questions.
	interactive bool
	// ffmpeg is the path to the ffmpeg binary.
	ffmpeg string
	// limiter paces requests per CDN host across all episodes in flight.
	limiter *throttle.Limiter
	stdout  io.Writer
	stderr  io.Writer
}

// Run executes the command and returns its exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stdout)
	switch {
	case errors.Is(err, pflag.ErrHelp), errors.Is(err, errVersion):
		return 0
	case err != nil:
		fmt.Fprintf(stderr, "kotori: %v\nTry 'kotori --help'.\n", err)
		return 2
	}
	a := &app{opts: *opts, stdout: stdout, stderr: stderr, limiter: throttle.New(), interactive: isTerminal(os.Stdin) && isTerminal(stderr)}
	switch err := a.run(ctx); {
	case errors.Is(err, tui.ErrCancelled):
		return 130
	case err != nil:
		fmt.Fprintf(stderr, "kotori: %v\n", err)
		return 1
	}
	return 0
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && isatty.IsTerminal(f.Fd())
}

const usage = `Download anime from Anikoto.

Usage:
  kotori [flags] [series or search terms]

With no arguments, kotori asks what to search for, then for anything not
given as a flag. The argument can be a series slug such as dragon-ball-gxrfm,
the URL of any of its pages, or words to search for.

Flags:
`

var errVersion = errors.New("version requested")

func parseFlags(args []string, stdout io.Writer) (*options, error) {
	var (
		opts        options
		baseURL     string
		sources     string
		noSubtitles bool
		showVersion bool
	)
	fs := pflag.NewFlagSet("kotori", pflag.ContinueOnError)
	fs.SortFlags = false
	fs.SetOutput(io.Discard)
	fs.IntVarP(&opts.quality, "quality", "q", 1080, "preferred resolution: 2160, 1440, 1080, 720, 480 or 360")
	fs.StringVarP(&opts.audio, "audio", "a", "dub", "audio type: sub or dub")
	fs.StringVarP(&opts.format, "format", "f", "mp4", "output format: "+strings.Join(ffmpeg.Formats(), ", "))
	fs.StringVarP(&sources, "source", "s", strings.Join(anikoto.Sources, ","), "comma-separated servers to try")
	fs.StringVarP(&opts.episodes, "episodes", "e", "", "episodes to download, e.g. 1,3,5-8 (default all)")
	fs.BoolVar(&opts.last, "last", false, "download only the latest episode")
	fs.BoolVar(&opts.list, "list", false, "list episodes and exit")
	fs.StringVarP(&opts.path, "path", "p", ".", "directory to save into")
	fs.StringVarP(&opts.template, "output", "o", defaultTemplate, "file name pattern, without the extension; "+templateHint)
	fs.StringVarP(&baseURL, "base-url", "b", "", "site address to use instead of the one in the URL (default "+defaultBaseURL+", or $"+baseURLEnv+")")
	fs.BoolVar(&noSubtitles, "no-subtitles", false, "skip subtitle downloads")
	fs.StringVar(&opts.subtitleFormat, "subtitle-format", "vtt", "subtitle format: "+strings.Join(ffmpeg.SubtitleFormats(), ", "))
	fs.StringVarP(&opts.subtitleLang, "subtitle-lang", "l", "English", "subtitle language, matched against track labels")
	fs.IntVarP(&opts.jobs, "jobs", "j", 4, "episodes to download at once")
	fs.IntVarP(&opts.concurrency, "concurrency", "c", 8, "segments to download at once, per episode")
	fs.BoolVarP(&opts.yes, "yes", "y", false, "don't ask for settings; use the defaults for anything not given")
	fs.BoolVar(&opts.debug, "debug", false, "print every request and skipped server")
	fs.BoolVarP(&showVersion, "version", "v", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			fmt.Fprint(stdout, usage+fs.FlagUsages())
		}
		return nil, err
	}
	if showVersion {
		fmt.Fprintln(stdout, "kotori", version)
		return nil, errVersion
	}
	// The flag wins over the environment, which wins over the built-in default.
	explicitBase := true
	if baseURL == "" {
		baseURL = os.Getenv(baseURLEnv)
	}
	if baseURL == "" {
		baseURL, explicitBase = defaultBaseURL, false
	}
	origin, err := parseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	opts.origin = origin.Scheme + "://" + origin.Host

	// One argument is a slug or URL. A single plain word might instead be a
	// search term, which is found out when it is looked up. Several words
	// are always a search.
	opts.query = strings.TrimSpace(strings.Join(fs.Args(), " "))
	if fs.NArg() == 1 && opts.query != "" && !strings.ContainsAny(opts.query, " \t") {
		if opts.url, err = watchURL(opts.query, baseURL, explicitBase); err != nil {
			return nil, err
		}
		opts.bareWord = !strings.Contains(opts.query, "/")
	}
	opts.given.episodes = fs.Changed("episodes") || opts.last
	opts.given.audio = fs.Changed("audio")
	opts.given.quality = fs.Changed("quality")
	opts.given.format = fs.Changed("format")
	opts.given.subtitles = fs.Changed("subtitle-format") || noSubtitles
	opts.given.path = fs.Changed("path")
	opts.given.naming = fs.Changed("output")
	if err := validateTemplate(opts.template); err != nil {
		return nil, err
	}
	opts.subtitles = !noSubtitles
	opts.audio = strings.ToLower(opts.audio)
	opts.format = strings.TrimPrefix(strings.ToLower(opts.format), ".")

	if !slices.Contains(qualities, opts.quality) {
		return nil, fmt.Errorf("invalid quality %d", opts.quality)
	}
	if opts.audio != "sub" && opts.audio != "dub" {
		return nil, fmt.Errorf("invalid audio type %q: use sub or dub", opts.audio)
	}
	opts.subtitleFormat = strings.TrimPrefix(strings.ToLower(opts.subtitleFormat), ".")
	if !ffmpeg.SupportedSubtitle(opts.subtitleFormat) {
		return nil, fmt.Errorf("invalid subtitle format %q: choose from %s", opts.subtitleFormat, strings.Join(ffmpeg.SubtitleFormats(), ", "))
	}
	if !ffmpeg.Supported(opts.format) {
		return nil, fmt.Errorf("invalid format %q: choose from %s", opts.format, strings.Join(ffmpeg.Formats(), ", "))
	}
	for _, source := range strings.Split(sources, ",") {
		source = strings.ToLower(strings.TrimSpace(source))
		if !slices.Contains(anikoto.Sources, source) {
			return nil, fmt.Errorf("invalid source %q: choose from %s", source, strings.Join(anikoto.Sources, ", "))
		}
		opts.sources = append(opts.sources, source)
	}
	if opts.concurrency < 1 {
		return nil, errors.New("concurrency must be at least 1")
	}
	if opts.jobs < 1 {
		return nil, errors.New("jobs must be at least 1")
	}
	return &opts, nil
}

func (a *app) run(ctx context.Context) error {
	if !a.opts.list {
		// Checked before any network work, so a missing ffmpeg never costs a download.
		path, err := ffmpeg.Find()
		if err != nil {
			return err
		}
		a.ffmpeg = path
	}
	client := fetch.New()
	if a.opts.debug {
		client.Debugf = a.debugf
		started := time.Now()
		a.limiter.Logf = func(format string, args ...any) {
			a.debugf("%5.1fs "+format, append([]any{time.Since(started).Seconds()}, args...)...)
		}
	}
	site := anikoto.New(client, a.opts.origin)
	series, err := a.series(ctx, site, client)
	if err != nil {
		return err
	}
	episodes, err := selectEpisodes(series.Episodes, a.opts.episodes, a.opts.last)
	if err != nil {
		return err
	}
	width := numberWidth(len(series.Episodes))

	if a.opts.list {
		fmt.Fprintf(a.stdout, "%s\n", series.Title)
		for _, ep := range episodes {
			fmt.Fprintf(a.stdout, "%*d  %s\n", width, ep.Number, ep.Title)
		}
		return nil
	}

	// Request logging would break up the live progress lines, so debug
	// output gets the plain display.
	d := newDisplay(a.stderr, a.interactive && !a.opts.debug, len(episodes))
	fmt.Fprintf(a.stderr, "\nDownloading %d of %d episodes\n\n", len(episodes), len(series.Episodes))
	// Episodes are downloaded several at a time. Each CDN host has its own
	// rate limit and episodes are spread across hosts, so episodes on
	// different hosts do not slow each other down.
	var (
		t    tally
		mu   sync.Mutex
		wg   sync.WaitGroup
		next = make(chan int)
	)
	for range min(a.opts.jobs, len(episodes)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				ep := episodes[i]
				title := ep.Title
				if title == "" {
					title = fmt.Sprintf("Episode %d", ep.Number)
				}
				r := d.begin(i+1, title)
				done, err := a.episode(ctx, site, client, series, ep, width, r)

				mu.Lock()
				switch {
				case ctx.Err() != nil:
					r.finish(false, "interrupted")
				case err != nil:
					t.failed++
					r.finish(false, "failed", strings.Split(err.Error(), "\n")...)
				default:
					if done.skipped {
						t.skipped++
					} else {
						t.downloaded++
						t.bytes += done.size
					}
					t.paths = append(t.paths, done.path)
					r.finish(true, done.summary)
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for i := range episodes {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()

	a.report(t)
	switch {
	case ctx.Err() != nil:
		return errors.New("interrupted")
	case t.failed > 0:
		return fmt.Errorf("%d of %d episodes failed", t.failed, len(episodes))
	}
	return nil
}

// report prints the closing summary of a run.
func (a *app) report(t tally) {
	fmt.Fprintln(a.stderr)
	for _, line := range t.lines() {
		fmt.Fprintln(a.stderr, line)
	}
}

// series settles which series to download and with what settings. It loads
// the series named on the command line, then hands over to the wizard for
// whatever is still open: the search, if there is no series yet, and any
// setting that was not given as a flag.
func (a *app) series(ctx context.Context, site *anikoto.Site, client *fetch.Client) (*anikoto.Series, error) {
	var series *anikoto.Series
	query := a.opts.query
	if a.opts.url != "" {
		var err error
		series, err = site.Load(ctx, a.opts.url)
		switch {
		case err == nil:
			query = ""
		case !a.opts.bareWord || !notFound(err):
			return nil, err
		case !a.interactive:
			return nil, fmt.Errorf("no series has the slug %q; run kotori in a terminal to search for it", a.opts.query)
		}
	}
	if series == nil && !a.interactive {
		return nil, errors.New("searching needs a terminal; pass a series slug or URL instead")
	}

	ask := a.interactive && !a.opts.yes && !a.opts.list
	cfg := tui.Config{
		Site:         site,
		Series:       series,
		Query:        query,
		AskEpisodes:  ask && !a.opts.given.episodes,
		AskAudio:     ask && !a.opts.given.audio,
		AskQuality:   ask && !a.opts.given.quality,
		AskFormat:    ask && !a.opts.given.format,
		AskSubtitles: ask && !a.opts.given.subtitles,
		AskPath:      ask && !a.opts.given.path,
		AskNaming:    ask && !a.opts.given.naming,
		Defaults: tui.Choices{
			Path:      a.opts.path,
			Naming:    a.opts.template,
			Episodes:  a.opts.episodes,
			Audio:     a.opts.audio,
			Quality:   a.opts.quality,
			Format:    a.opts.format,
			Subtitles: a.opts.subtitleFormat,
		},
		Qualities:       qualities,
		Formats:         formatOptions(),
		SubtitleFormats: subtitleOptions(),
		NamingHint:      templateHint,
		Preview:         previewTemplate,
	}
	for _, preset := range namingPresets {
		cfg.Namings = append(cfg.Namings, tui.Option{Label: preset.name, Value: preset.template})
	}
	if !a.opts.subtitles {
		cfg.Defaults.Subtitles = noSubtitles
	}
	// Request logging would draw over the wizard.
	debugf := client.Debugf
	client.Debugf = nil
	outcome, err := tui.Run(ctx, cfg)
	client.Debugf = debugf
	if err != nil {
		return nil, err
	}
	a.opts.episodes = outcome.Episodes
	a.opts.audio = outcome.Audio
	a.opts.quality = outcome.Quality
	a.opts.format = outcome.Format
	a.opts.path = outcome.Path
	a.opts.template = outcome.Naming
	if a.opts.subtitles = outcome.Subtitles != noSubtitles; a.opts.subtitles {
		a.opts.subtitleFormat = outcome.Subtitles
	}
	return outcome.Series, nil
}

// notFound reports whether loading a page failed because it is not there,
// as opposed to the site being unreachable.
func notFound(err error) bool {
	var status *fetch.StatusError
	return errors.Is(err, anikoto.ErrNotSeries) || errors.As(err, &status) && status.Code == http.StatusNotFound
}

// formatOptions lists the output formats for the wizard, with the ones
// that keep the original quality first.
func formatOptions() []tui.Option {
	var copied, reencoded []tui.Option
	for _, name := range ffmpeg.Formats() {
		if ffmpeg.Reencodes(name) {
			reencoded = append(reencoded, tui.Option{Value: name, Note: "re-encoded, slow"})
		} else {
			copied = append(copied, tui.Option{Value: name})
		}
	}
	return append(copied, reencoded...)
}

// previewTemplate shows what a file name pattern produces for the first
// episode that would be downloaded, or why the pattern cannot be used.
func previewTemplate(template string, series *anikoto.Series, c tui.Choices) (string, error) {
	if err := validateTemplate(template); err != nil {
		return "", err
	}
	episodes, err := selectEpisodes(series.Episodes, c.Episodes, false)
	if err != nil || len(episodes) == 0 {
		episodes = series.Episodes
	}
	name := expandTemplate(template, series, episodes[0], numberWidth(len(series.Episodes)), c.Audio)
	return name + "." + c.Format, nil
}

// noSubtitles is the wizard's choice for skipping subtitles.
const noSubtitles = "none"

// subtitleOptions lists the subtitle formats for the wizard, plus the choice
// to go without.
func subtitleOptions() []tui.Option {
	notes := map[string]string{"vtt": "WebVTT, as the site provides them", "srt": "SubRip", "ass": "Advanced SubStation Alpha"}
	var options []tui.Option
	for _, name := range ffmpeg.SubtitleFormats() {
		options = append(options, tui.Option{Value: name, Note: notes[name]})
	}
	return append(options, tui.Option{Value: noSubtitles, Note: "don't download subtitles"})
}

func (a *app) logf(format string, args ...any) {
	fmt.Fprintf(a.stderr, format+"\n", args...)
}

func (a *app) debugf(format string, args ...any) {
	if a.opts.debug {
		fmt.Fprintf(a.stderr, "debug: "+format+"\n", args...)
	}
}
