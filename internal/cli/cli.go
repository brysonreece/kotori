// Package cli implements the kotori command.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/brysonreece/kotori/internal/anikoto"
	"github.com/brysonreece/kotori/internal/fetch"
	"github.com/brysonreece/kotori/internal/ffmpeg"
	"github.com/brysonreece/kotori/internal/hls"
)

const version = "0.1.0"

var qualities = []int{2160, 1440, 1080, 720, 480, 360}

type options struct {
	url          string
	quality      int
	audio        string
	format       string
	sources      []string
	episodes     string
	last         bool
	list         bool
	path         string
	subtitles    bool
	subtitleLang string
	concurrency  int
	debug        bool
}

type app struct {
	opts options
	// ffmpeg is the path to the ffmpeg binary.
	ffmpeg string
	stdout io.Writer
	stderr io.Writer
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
	a := &app{opts: *opts, stdout: stdout, stderr: stderr}
	if err := a.run(ctx); err != nil {
		fmt.Fprintf(stderr, "kotori: %v\n", err)
		return 1
	}
	return 0
}

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
	fs.StringVarP(&baseURL, "base-url", "b", "", "site address to use instead of the one in the URL (default "+defaultBaseURL+", or $"+baseURLEnv+")")
	fs.BoolVar(&noSubtitles, "no-subtitles", false, "skip subtitle downloads")
	fs.StringVarP(&opts.subtitleLang, "subtitle-lang", "l", "English", "subtitle language, matched against track labels")
	fs.IntVarP(&opts.concurrency, "concurrency", "c", 8, "segments to download at once")
	fs.BoolVar(&opts.debug, "debug", false, "print every request and skipped server")
	fs.BoolVarP(&showVersion, "version", "v", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			fmt.Fprintf(stdout, "Download anime from Anikoto.\n\nUsage:\n  kotori [flags] <series>\n\nThe series is the slug from its address on the site, such as dragon-ball-gxrfm,\nor the full URL of any of its pages.\n\nFlags:\n%s", fs.FlagUsages())
		}
		return nil, err
	}
	if showVersion {
		fmt.Fprintln(stdout, "kotori", version)
		return nil, errVersion
	}
	if fs.NArg() != 1 {
		return nil, errors.New("expected exactly one series, such as dragon-ball-gxrfm")
	}
	// The flag wins over the environment, which wins over the built-in default.
	explicitBase := true
	if baseURL == "" {
		baseURL = os.Getenv(baseURLEnv)
	}
	if baseURL == "" {
		baseURL, explicitBase = defaultBaseURL, false
	}
	var err error
	if opts.url, err = watchURL(fs.Arg(0), baseURL, explicitBase); err != nil {
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
	}
	site := anikoto.New(client)
	series, err := site.Load(ctx, a.opts.url)
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

	a.logf("%s: downloading %d of %d episodes", series.Title, len(episodes), len(series.Episodes))
	failed := 0
	for _, ep := range episodes {
		err := a.episode(ctx, site, client, series, ep, width)
		if ctx.Err() != nil {
			return errors.New("interrupted")
		}
		if err != nil {
			failed++
			a.logf("E%0*d failed: %v", width, ep.Number, err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d episodes failed", failed, len(episodes))
	}
	return nil
}

// episode downloads one episode from the first server that works.
func (a *app) episode(ctx context.Context, site *anikoto.Site, client *fetch.Client, series *anikoto.Series, ep anikoto.Episode, width int) error {
	label := fmt.Sprintf("E%0*d", width, ep.Number)
	title := cleanName(series.Title)
	dir := filepath.Join(a.opts.path, title)
	base := filepath.Join(dir, strings.TrimSpace(fmt.Sprintf("%s %s %s", title, label, cleanName(ep.Title))))

	if _, err := os.Stat(a.output(base)); err == nil {
		a.logf("%s already downloaded: %s", label, a.output(base))
		return nil
	}
	if _, err := os.Stat(intermediate(base)); err == nil {
		// An earlier run downloaded this episode but did not finish converting it.
		return a.convert(ctx, base, label, 0)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	servers, err := site.Servers(ctx, ep)
	if err != nil {
		return err
	}
	var errs []error
	try := func(name string, resolve func() (*anikoto.Stream, error)) bool {
		stream, err := resolve()
		if err == nil {
			err = a.download(ctx, client, stream, base, label)
		}
		if err != nil {
			a.debugf("%s via %s: %v", label, name, err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		return err == nil
	}

	for _, server := range servers {
		if server.Audio != a.opts.audio || !slices.Contains(a.opts.sources, server.Name) {
			a.debugf("%s: skipping %s server %q", label, server.Audio, server.Name)
			continue
		}
		if try(server.Name, func() (*anikoto.Stream, error) { return site.Resolve(ctx, server, a.opts.audio) }) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if slices.Contains(a.opts.sources, "kiwi") {
		if try("kiwi", func() (*anikoto.Stream, error) { return site.Kiwi(ctx, ep, a.opts.quality, a.opts.audio) }) {
			return nil
		}
	}
	if len(errs) == 0 {
		return fmt.Errorf("no %s server matched the requested sources", a.opts.audio)
	}
	return errors.Join(errs...)
}

// download saves a stream's subtitles and video next to each other at base.
func (a *app) download(ctx context.Context, client *fetch.Client, stream *anikoto.Stream, base, label string) error {
	headers := map[string]string{
		"Referer": stream.Referer,
		"Origin":  strings.TrimRight(stream.Referer, "/"),
	}
	if a.opts.subtitles {
		a.subtitles(ctx, client, stream, headers, base, label)
	}

	d := &hls.Downloader{
		Client:      client,
		Headers:     headers,
		Quality:     a.opts.quality,
		Concurrency: a.opts.concurrency,
		Progress: func(p hls.Progress) {
			fmt.Fprintf(a.stderr, "\r\033[K%s  %d/%d segments  %.1f MB", label, p.Done, p.Total, float64(p.Bytes)/1e6)
		},
		Logf: func(format string, args ...any) {
			if a.opts.debug {
				fmt.Fprint(a.stderr, "\r\033[K")
				a.debugf(label+": "+format, args...)
			}
		},
	}
	// Request logging would interleave with the progress line.
	debugf := client.Debugf
	client.Debugf = nil
	result, err := d.Download(ctx, stream.URL, intermediate(base))
	client.Debugf = debugf
	fmt.Fprint(a.stderr, "\r\033[K")
	if err != nil {
		return err
	}
	if result.Dropped > 0 {
		a.debugf("%s: dropped %d filler segments", label, result.Dropped)
	}

	return a.convert(ctx, base, label, result.Height)
}

// intermediate is where an episode's raw stream sits between download and
// conversion.
func intermediate(base string) string {
	return base + ".download"
}

func (a *app) output(base string) string {
	return base + "." + a.opts.format
}

// convert turns a downloaded stream into the requested format. The raw
// stream is kept if conversion fails, so the next run can pick it up.
func (a *app) convert(ctx context.Context, base, label string, height int) error {
	if ffmpeg.Reencodes(a.opts.format) {
		a.logf("%s: re-encoding to %s, this can take a while", label, a.opts.format)
	}
	out := a.output(base)
	if err := ffmpeg.Convert(ctx, a.ffmpeg, intermediate(base), out, a.opts.format); err != nil {
		return err
	}
	os.Remove(intermediate(base))
	if height > 0 {
		a.logf("%s saved (%dp): %s", label, height, out)
	} else {
		a.logf("%s saved: %s", label, out)
	}
	return nil
}

// subtitles saves every track whose label matches the requested language. A
// missing subtitle never fails the episode.
func (a *app) subtitles(ctx context.Context, client *fetch.Client, stream *anikoto.Stream, headers map[string]string, base, label string) {
	for _, track := range stream.Tracks {
		if track.File == "" || !strings.Contains(strings.ToLower(track.Label), strings.ToLower(a.opts.subtitleLang)) {
			continue
		}
		path := base + "." + languageCode(track.Label) + ".vtt"
		if _, err := os.Stat(path); err == nil {
			continue
		}
		resp, err := client.Get(ctx, track.File, headers)
		if err == nil {
			err = os.WriteFile(path, resp.Body, 0o644)
		}
		if err != nil {
			a.logf("%s: %s subtitles failed: %v", label, track.Label, err)
			continue
		}
		a.logf("%s subtitles saved: %s", label, path)
	}
}

func (a *app) logf(format string, args ...any) {
	fmt.Fprintf(a.stderr, format+"\n", args...)
}

func (a *app) debugf(format string, args ...any) {
	if a.opts.debug {
		fmt.Fprintf(a.stderr, "debug: "+format+"\n", args...)
	}
}
