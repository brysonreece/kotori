package hls

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/brysonreece/kotori/internal/aescbc"
	"github.com/brysonreece/kotori/internal/fetch"
)

const segmentAttempts = 8

// retryBackoff is the base delay between attempts at a failed segment.
var retryBackoff = 500 * time.Millisecond

// maxRateLimitPause caps how long one rate-limit response holds things up.
const maxRateLimitPause = time.Minute

// Downloader fetches every segment of a stream and joins them into one file.
type Downloader struct {
	Client *fetch.Client
	// Headers are sent with every playlist, key and segment request.
	Headers map[string]string
	// Quality is the preferred vertical resolution, e.g. 1080.
	Quality     int
	Concurrency int
	// Progress, when set, is called after each finished segment.
	Progress func(Progress)
	// Logf, when set, receives notices such as rate-limit pauses.
	Logf func(format string, args ...any)

	gate gate
}

// gate lets one worker's rate-limit response pause every worker, so the
// others do not keep hitting a server that has asked for a break.
type gate struct {
	mu    sync.Mutex
	until time.Time
}

func (g *gate) pause(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := time.Now().Add(d); until.After(g.until) {
		g.until = until
	}
}

func (g *gate) wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		remaining := time.Until(g.until)
		g.mu.Unlock()
		if remaining <= 0 {
			return ctx.Err()
		}
		select {
		case <-time.After(remaining):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Progress reports how far a download has got.
type Progress struct {
	Done, Total int
	Bytes       int64
}

// Result describes a finished download.
type Result struct {
	// Height is the chosen rendition's height, or 0 when unknown.
	Height int
	// Dropped counts filler segments removed from the playlist.
	Dropped int
}

// Download saves the stream at manifestURL to dest as the raw joined
// segments: MPEG-TS, or fragmented MP4 when the playlist has an init segment.
// Finished segments are kept in dest+".parts" until the file is assembled, so
// an interrupted download resumes where it stopped.
func (d *Downloader) Download(ctx context.Context, manifestURL, dest string) (*Result, error) {
	media, height, err := d.mediaPlaylist(ctx, manifestURL)
	if err != nil {
		return nil, err
	}
	segments, dropped := revealSegments(media.Segments)
	if len(segments) == 0 {
		return nil, errors.New("hls: playlist has no downloadable segments")
	}
	keys, err := d.fetchKeys(ctx, segments)
	if err != nil {
		return nil, err
	}

	parts := dest + ".parts"
	if err := prepareParts(parts, fmt.Sprintf("%d/%d", height, len(segments))); err != nil {
		return nil, err
	}
	partPath := func(i int) string { return filepath.Join(parts, fmt.Sprintf("%06d.seg", i)) }

	files := make([]string, 0, len(segments)+1)
	if media.Map != "" {
		initPath := filepath.Join(parts, "init.seg")
		if _, err := d.segment(ctx, Segment{URI: media.Map}, nil, initPath); err != nil {
			return nil, fmt.Errorf("hls: init segment: %w", err)
		}
		files = append(files, initPath)
	}

	if err := d.fetchSegments(ctx, segments, keys, partPath); err != nil {
		return nil, err
	}
	for i := range segments {
		files = append(files, partPath(i))
	}

	if err := concat(dest, files); err != nil {
		return nil, err
	}
	os.RemoveAll(parts)
	return &Result{Height: height, Dropped: dropped}, nil
}

// mediaPlaylist fetches the manifest and, if it is a master playlist, the
// rendition chosen from it.
func (d *Downloader) mediaPlaylist(ctx context.Context, manifestURL string) (*Playlist, int, error) {
	playlist, err := d.playlist(ctx, manifestURL)
	if err != nil {
		return nil, 0, err
	}
	if len(playlist.Variants) == 0 {
		return playlist, 0, nil
	}
	if playlist.AlternateAudio {
		return nil, 0, errors.New("hls: streams with separate audio renditions are not supported yet")
	}
	variant := SelectVariant(playlist.Variants, d.Quality)
	media, err := d.playlist(ctx, variant.URI)
	if err != nil {
		return nil, 0, err
	}
	return media, variant.Height, nil
}

func (d *Downloader) playlist(ctx context.Context, rawURL string) (*Playlist, error) {
	resp, err := d.Client.Get(ctx, rawURL, d.Headers)
	if err != nil {
		return nil, err
	}
	return Parse(string(resp.Body), resp.URL)
}

func (d *Downloader) fetchKeys(ctx context.Context, segments []Segment) (map[string][]byte, error) {
	keys := map[string][]byte{}
	for _, seg := range segments {
		if seg.Key == nil {
			continue
		}
		if seg.Key.Method != "AES-128" {
			return nil, fmt.Errorf("hls: unsupported segment encryption %q", seg.Key.Method)
		}
		if _, ok := keys[seg.Key.URI]; ok {
			continue
		}
		resp, err := d.Client.Get(ctx, seg.Key.URI, d.Headers)
		if err != nil {
			return nil, fmt.Errorf("hls: segment key: %w", err)
		}
		keys[seg.Key.URI] = resp.Body
	}
	return keys, nil
}

func (d *Downloader) fetchSegments(ctx context.Context, segments []Segment, keys map[string][]byte, partPath func(int) string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		progress = Progress{Total: len(segments)}
	)
	jobs := make(chan int)
	for range max(d.Concurrency, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				n, err := d.segment(ctx, segments[i], keys, partPath(i))
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("hls: segment %d of %d: %w", i+1, len(segments), err)
						cancel()
					}
				} else {
					progress.Done++
					progress.Bytes += n
					if d.Progress != nil {
						d.Progress(progress)
					}
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for i := range segments {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// segment downloads one segment to path and returns its size. A segment
// already on disk from an earlier run is left alone.
func (d *Downloader) segment(ctx context.Context, seg Segment, keys map[string][]byte, path string) (int64, error) {
	if info, err := os.Stat(path); err == nil {
		return info.Size(), nil
	}

	var data []byte
	var err error
	for attempt := 1; ; attempt++ {
		if err := d.gate.wait(ctx); err != nil {
			return 0, err
		}
		var resp *fetch.Response
		resp, err = d.Client.Get(ctx, seg.URI, d.Headers)
		if err == nil {
			data = resp.Body
			break
		}
		if attempt == segmentAttempts || ctx.Err() != nil {
			return 0, err
		}
		var status *fetch.StatusError
		if errors.As(err, &status) && status.Code == http.StatusTooManyRequests {
			// Honor Retry-After, otherwise back off exponentially.
			pause := min(max(status.RetryAfter, retryBackoff<<attempt), maxRateLimitPause)
			d.gate.pause(pause)
			if d.Logf != nil {
				d.Logf("rate limited, pausing for %s", pause.Round(time.Millisecond))
			}
			continue
		}
		select {
		case <-time.After(time.Duration(attempt) * retryBackoff):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}

	data = stripPNG(data)
	if seg.Key != nil {
		iv := seg.Key.IV
		if len(iv) == 0 {
			iv = make([]byte, 16)
			binary.BigEndian.PutUint64(iv[8:], uint64(seg.Seq))
		}
		if data, err = aescbc.Decrypt(keys[seg.Key.URI], iv, data); err != nil {
			return 0, err
		}
	}

	// Write then rename, so a part file on disk is always complete.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return 0, err
	}
	return int64(len(data)), os.Rename(tmp, path)
}

// prepareParts creates the parts directory, discarding leftovers from a
// download of a different rendition.
func prepareParts(dir, meta string) error {
	metaPath := filepath.Join(dir, "meta")
	if existing, err := os.ReadFile(metaPath); err != nil || string(existing) != meta {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(metaPath, []byte(meta), 0o644)
}

func concat(out string, files []string) error {
	tmp := out + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	for _, name := range files {
		part, err := os.Open(name)
		if err == nil {
			_, err = io.Copy(f, part)
			part.Close()
		}
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}
