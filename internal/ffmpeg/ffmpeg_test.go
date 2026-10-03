package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sampleStream makes a short MPEG-TS file with H.264 video and AAC audio,
// the same shape as a downloaded stream.
func sampleStream(t *testing.T, ffmpeg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "episode.download")
	generate := exec.Command(ffmpeg, "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac",
		"-f", "mpegts", path)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("could not generate a test stream: %v: %s", err, output)
	}
	return path
}

func TestConvert(t *testing.T) {
	ffmpeg, err := Find()
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	src := sampleStream(t, ffmpeg)
	wantVideo := map[string]string{"mp4": "h264", "mov": "h264", "mkv": "h264", "avi": "mpeg4", "webm": "vp9"}

	for _, name := range Formats() {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "episode."+name)
			if err := Convert(context.Background(), ffmpeg, src, dst, name); err != nil {
				t.Fatal(err)
			}
			probe, err := exec.Command(ffmpeg, "-hide_banner", "-i", dst, "-f", "null", "-").CombinedOutput()
			if err != nil {
				t.Fatalf("the output does not decode: %v: %s", err, probe)
			}
			for _, stream := range []string{"Video: " + wantVideo[name], "Audio: "} {
				if !strings.Contains(string(probe), stream) {
					t.Errorf("output is missing %q:\n%s", stream, probe)
				}
			}
			if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
				t.Error("temporary file was left behind")
			}
		})
	}
}

func TestConvertFailureLeavesNoOutput(t *testing.T) {
	ffmpeg, err := Find()
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "garbage.download")
	if err := os.WriteFile(src, []byte("not a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.mp4")
	if err := Convert(context.Background(), ffmpeg, src, dst, "mp4"); err == nil {
		t.Fatal("expected an error")
	}
	for _, path := range []string{dst, dst + ".part"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", path)
		}
	}
	if err := Convert(context.Background(), ffmpeg, src, dst, "flv"); err == nil {
		t.Error("expected an error for an unsupported format")
	}
}

func TestFormats(t *testing.T) {
	if got := strings.Join(Formats(), ","); got != "avi,mkv,mov,mp4,webm" {
		t.Errorf("Formats() = %s", got)
	}
	if !Supported("mkv") || Supported("ts") {
		t.Error("Supported is wrong")
	}
	if Reencodes("mp4") || !Reencodes("avi") {
		t.Error("Reencodes is wrong")
	}
}
