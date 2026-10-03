// Package ffmpeg converts downloaded streams into their final container.
package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
)

// format describes how ffmpeg writes one output container.
type format struct {
	// muxer is ffmpeg's name for the container.
	muxer string
	// codec holds the codec arguments. Containers that can hold the stream's
	// H.264 and AAC copy it untouched; the rest have to re-encode.
	codec    []string
	reencode bool
}

var copyStreams = []string{"-c", "copy"}

var formats = map[string]format{
	"mp4": {muxer: "mp4", codec: append(copyStreams, "-movflags", "+faststart")},
	"mov": {muxer: "mov", codec: append(copyStreams, "-movflags", "+faststart")},
	"mkv": {muxer: "matroska", codec: copyStreams},
	"avi": {
		muxer:    "avi",
		codec:    []string{"-c:v", "mpeg4", "-vtag", "XVID", "-q:v", "3", "-c:a", "libmp3lame", "-q:a", "2"},
		reencode: true,
	},
	"webm": {
		muxer:    "webm",
		codec:    []string{"-c:v", "libvpx-vp9", "-crf", "32", "-b:v", "0", "-c:a", "libopus"},
		reencode: true,
	},
}

// Formats lists the supported output formats, which double as file
// extensions.
func Formats() []string {
	names := make([]string, 0, len(formats))
	for name := range formats {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Supported reports whether name is an output format Convert can write.
func Supported(name string) bool {
	_, ok := formats[name]
	return ok
}

// Reencodes reports whether converting to name re-encodes the video, which
// is far slower than copying it and loses some quality.
func Reencodes(name string) bool {
	return formats[name].reencode
}

// Find locates the ffmpeg binary.
func Find() (string, error) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", errors.New("ffmpeg is required but was not found on your PATH; install it from https://ffmpeg.org or with your package manager")
	}
	return path, nil
}

// Convert writes src to dst in the named format. dst only appears once the
// conversion has finished.
func Convert(ctx context.Context, ffmpeg, src, dst, name string) error {
	f, ok := formats[name]
	if !ok {
		return fmt.Errorf("ffmpeg: unsupported format %q", name)
	}
	tmp := dst + ".part"
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", src}
	args = append(args, f.codec...)
	args = append(args, "-f", f.muxer, tmp)
	if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("ffmpeg: %w: %s", err, output)
	}
	return os.Rename(tmp, dst)
}
