package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brysonreece/kotori/internal/anikoto"
	"github.com/brysonreece/kotori/internal/fetch"
	"github.com/brysonreece/kotori/internal/ffmpeg"
	"github.com/brysonreece/kotori/internal/hls"
)

// result is what became of one episode.
type result struct {
	// summary is a few words for the display, such as "1080p · 294.1 MB".
	summary string
	// path is the video file, and size its length in bytes.
	path string
	size int64
	// skipped is set when the file was already there.
	skipped bool
}

// base is the path an episode's files share, before their extensions: the
// output folder joined with the file name pattern filled in.
func (a *app) base(series *anikoto.Series, ep anikoto.Episode, width int) string {
	return filepath.Join(expandHome(a.opts.path), expandTemplate(a.opts.template, series, ep, width, a.opts.audio))
}

// episode downloads one episode from the first server that works.
func (a *app) episode(ctx context.Context, site *anikoto.Site, client *fetch.Client, series *anikoto.Series, ep anikoto.Episode, width int, r *row) (result, error) {
	label := fmt.Sprintf("E%0*d", width, ep.Number)
	base := a.base(series, ep, width)

	if info, err := os.Stat(a.output(base)); err == nil {
		return result{summary: "already downloaded", path: a.output(base), size: info.Size(), skipped: true}, nil
	}
	if _, err := os.Stat(intermediate(base)); err == nil {
		// An earlier run downloaded this episode but did not finish converting it.
		return a.convert(ctx, base, r, 0)
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return result{}, err
	}

	r.status("finding servers")
	servers, err := site.Servers(ctx, ep)
	if err != nil {
		return result{}, err
	}
	var (
		errs    []error
		summary result
	)
	try := func(name string, resolve func() (*anikoto.Stream, error)) bool {
		r.status("trying " + name)
		stream, err := resolve()
		if err == nil {
			summary, err = a.download(ctx, client, stream, base, name, r)
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
			return summary, nil
		}
		if ctx.Err() != nil {
			return result{}, ctx.Err()
		}
	}
	if slices.Contains(a.opts.sources, "kiwi") {
		if try("kiwi", func() (*anikoto.Stream, error) { return site.Kiwi(ctx, ep, a.opts.quality, a.opts.audio) }) {
			return summary, nil
		}
	}
	if len(errs) == 0 {
		return result{}, fmt.Errorf("no %s server matched the requested sources", a.opts.audio)
	}
	return result{}, errors.Join(errs...)
}

// download saves a stream's subtitles and video next to each other at base.
func (a *app) download(ctx context.Context, client *fetch.Client, stream *anikoto.Stream, base, server string, r *row) (result, error) {
	headers := map[string]string{
		"Referer": stream.Referer,
		"Origin":  strings.TrimRight(stream.Referer, "/"),
	}
	if a.opts.subtitles {
		a.subtitles(ctx, client, stream, headers, base, r)
	}

	// Hundreds of segment requests would bury everything else in the debug
	// output, so the downloader gets a client that does not log them.
	quiet := *client
	quiet.Debugf = nil
	downloader := &hls.Downloader{
		Client:      &quiet,
		Limiter:     a.limiter,
		Headers:     headers,
		Quality:     a.opts.quality,
		Concurrency: a.opts.concurrency,
	}
	a.debugf("%s: stream is %s", filepath.Base(base), stream.URL)

	// The line under the bar is refreshed on a timer as well as on progress.
	// While a rate limit holds the download up, no segments finish, so
	// nothing else would say so until the hold was nearly over.
	var (
		mu     sync.Mutex
		latest hls.Progress
		speed  meter
	)
	show := func() {
		mu.Lock()
		p := latest
		e := estimate{pace: downloader.Pace(), hold: downloader.Paused()}
		if e.hold > 0 {
			speed.hold()
		}
		e.segments, e.bytes, e.measured = speed.rates()
		mu.Unlock()
		if p.Total == 0 {
			r.progress(0, server+" · starting")
			return
		}
		r.progress(float64(p.Done)/float64(p.Total), progressText(server, p, e))
	}
	downloader.Progress = func(p hls.Progress) {
		mu.Lock()
		latest = p
		speed.add(time.Now(), p)
		mu.Unlock()
		show()
	}
	show()
	finished := make(chan struct{})
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				show()
			case <-finished:
				return
			}
		}
	}()
	fetched, err := downloader.Download(ctx, stream.URL, intermediate(base))
	close(finished)
	if err != nil {
		return result{}, err
	}
	if fetched.Dropped > 0 {
		a.debugf("dropped %d filler segments", fetched.Dropped)
	}
	return a.convert(ctx, base, r, fetched.Height)
}

func megabytes(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
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
func (a *app) convert(ctx context.Context, base string, r *row, height int) (result, error) {
	if ffmpeg.Reencodes(a.opts.format) {
		r.progress(1, fmt.Sprintf("re-encoding to %s, this can take a while", a.opts.format))
	} else {
		r.progress(1, "converting to "+a.opts.format)
	}
	out := a.output(base)
	if err := ffmpeg.Convert(ctx, a.ffmpeg, intermediate(base), out, a.opts.format); err != nil {
		return result{}, err
	}
	os.Remove(intermediate(base))

	done := result{path: out}
	var summary []string
	if height > 0 {
		summary = append(summary, fmt.Sprintf("%dp", height))
	}
	if info, err := os.Stat(out); err == nil {
		done.size = info.Size()
		summary = append(summary, megabytes(done.size))
	}
	done.summary = strings.Join(summary, " · ")
	return done, nil
}

// subtitles saves every track whose label matches the requested language. A
// missing subtitle never fails the episode; it is noted under it instead.
func (a *app) subtitles(ctx context.Context, client *fetch.Client, stream *anikoto.Stream, headers map[string]string, base string, r *row) {
	for _, track := range stream.Tracks {
		if track.File == "" || !strings.Contains(strings.ToLower(track.Label), strings.ToLower(a.opts.subtitleLang)) {
			continue
		}
		stem := base + "." + languageCode(track.Label)
		if _, err := os.Stat(stem + "." + a.opts.subtitleFormat); err == nil {
			continue
		}
		resp, err := client.Get(ctx, track.File, headers)
		if err == nil {
			err = a.saveSubtitle(ctx, resp.Body, track.File, stem, r)
		}
		if err != nil {
			r.note(fmt.Sprintf("%s subtitles failed: %v", track.Label, err))
		}
	}
}

// saveSubtitle writes a downloaded subtitle file to stem plus the requested
// format's extension, converting it if the site served another format. If
// the conversion fails, the original is kept under its own extension.
func (a *app) saveSubtitle(ctx context.Context, body []byte, source, stem string, r *row) error {
	have, want := subtitleFormat(source, body), a.opts.subtitleFormat
	if have == want {
		return os.WriteFile(stem+"."+want, body, 0o644)
	}
	original := stem + "." + have
	if err := os.WriteFile(original, body, 0o644); err != nil {
		return err
	}
	if err := ffmpeg.ConvertSubtitle(ctx, a.ffmpeg, original, stem+"."+want, want); err != nil {
		r.note(fmt.Sprintf("subtitles could not be converted to %s, kept as %s", want, have))
		return nil
	}
	return os.Remove(original)
}

// subtitleFormat works out which format a downloaded subtitle file is in,
// from its content where that is unambiguous and otherwise from its URL.
func subtitleFormat(source string, body []byte) string {
	body = bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	switch {
	case bytes.HasPrefix(body, []byte("WEBVTT")):
		return "vtt"
	case bytes.HasPrefix(body, []byte("[Script Info]")):
		return "ass"
	}
	if u, err := url.Parse(source); err == nil {
		if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(u.Path)), "."); ffmpeg.SupportedSubtitle(ext) {
			return ext
		}
	}
	return "vtt"
}
