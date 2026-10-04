package cli

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

var escapes = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// screen replays what was written to a terminal and returns the lines left
// showing, understanding just the cursor movement the display uses.
func screen(t *testing.T, output string) []string {
	t.Helper()
	var lines []string
	row := 0
	for _, part := range strings.SplitAfter(output, "\n") {
		for {
			m := regexp.MustCompile(`\x1b\[(\d+)A\r\x1b\[J`).FindStringSubmatchIndex(part)
			if m == nil {
				break
			}
			up := 0
			for _, c := range part[m[2]:m[3]] {
				up = up*10 + int(c-'0')
			}
			row -= up
			if row < 0 {
				t.Fatalf("cursor moved above the top of the output")
			}
			lines = lines[:row]
			part = part[:m[0]] + part[m[1]:]
		}
		text := escapes.ReplaceAllString(strings.TrimSuffix(part, "\n"), "")
		if strings.HasSuffix(part, "\n") {
			lines = append(lines[:row], text)
			row++
		}
	}
	return lines
}

func TestDisplayLive(t *testing.T) {
	var out bytes.Buffer
	d := newDisplay(&out, true, 5)
	d.width = func() int { return 80 }

	first := d.begin(1, "Bulma and Son Goku")
	first.status("trying hd")
	first.progress(0.5, "hd · 5/10 segments")
	got := screen(t, out.String())
	if len(got) != 2 || !strings.HasPrefix(got[0], "[1 / 5]  Bulma and Son Goku  ") || !strings.HasSuffix(got[0], " 50%") {
		t.Fatalf("in flight:\n%s", strings.Join(got, "\n"))
	}
	if got[1] != "         hd · 5/10 segments" {
		t.Errorf("detail line = %q", got[1])
	}

	first.finish(true, "1080p · 294.1 MB")
	second := d.begin(2, "What the...?! No Balls!")
	second.note("English subtitles failed: 404 Not Found")
	second.progress(0.25, "hd · 1/4 segments")
	second.finish(false, "failed", "hd: segment 3 of 4: 403 Forbidden", "kiwi: no stream")
	d.begin(3, "The Turtle Hermit's Kinto Un")

	want := []string{
		"[1 / 5]  Bulma and Son Goku  ✓ done  1080p · 294.1 MB",
		"[2 / 5]  What the...?! No Balls!  ✗ failed",
		"         English subtitles failed: 404 Not Found",
		"         hd: segment 3 of 4: 403 Forbidden",
		"         kiwi: no stream",
		"[3 / 5]  The Turtle Hermit's Kinto Un  …",
		"         starting",
	}
	if got := screen(t, out.String()); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("screen:\n%s\n\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestDisplaySeveralAtOnce(t *testing.T) {
	var out bytes.Buffer
	d := newDisplay(&out, true, 3)
	d.width = func() int { return 80 }
	show := func() string { return strings.Join(screen(t, out.String()), "\n") }

	one := d.begin(1, "One")
	two := d.begin(2, "Two")
	three := d.begin(3, "Three")
	one.progress(0.5, "hd · half")
	two.progress(0.25, "hd · quarter")

	// The second finishes first. It keeps its place between the others.
	two.finish(true, "540p · 70.0 MB")
	three.status("trying hd")
	got := strings.Split(show(), "\n")
	if len(got) != 5 || !strings.HasPrefix(got[0], "[1 / 3]  One  ") || !strings.HasSuffix(got[0], " 50%") ||
		got[1] != "         hd · half" ||
		got[2] != "[2 / 3]  Two  ✓ done  540p · 70.0 MB" ||
		got[3] != "[3 / 3]  Three  …" || got[4] != "         trying hd" {
		t.Fatalf("with the middle one finished:\n%s", show())
	}

	// Later updates to the others leave the finished line where it is.
	three.finish(false, "failed", "hd: nope")
	one.progress(1, "converting to mkv")
	one.finish(true, "540p · 78.1 MB")
	want := "[1 / 3]  One  ✓ done  540p · 78.1 MB\n" +
		"[2 / 3]  Two  ✓ done  540p · 70.0 MB\n" +
		"[3 / 3]  Three  ✗ failed\n" +
		"         hd: nope"
	if show() != want {
		t.Errorf("screen:\n%s\n\nwant:\n%s", show(), want)
	}
	// With nothing in flight the cursor is shown again.
	if !strings.HasSuffix(out.String(), "\x1b[?25h") {
		t.Error("the cursor was left hidden")
	}
}

func TestDisplayKeepsEpisodeOrder(t *testing.T) {
	var out bytes.Buffer
	d := newDisplay(&out, true, 4)
	d.width = func() int { return 80 }

	// Four downloads start at once and reach the display in any order.
	for _, index := range []int{1, 4, 3, 2} {
		d.begin(index, fmt.Sprintf("Episode %d", index))
	}
	var got []string
	for _, line := range screen(t, out.String()) {
		if strings.HasPrefix(line, "[") {
			got = append(got, line[:7])
		}
	}
	if strings.Join(got, " ") != "[1 / 4] [2 / 4] [3 / 4] [4 / 4]" {
		t.Errorf("rows are in the order %v", got)
	}
}

func TestDisplayNeverWraps(t *testing.T) {
	var out bytes.Buffer
	d := newDisplay(&out, true, 152)
	d.width = func() int { return 50 }
	long := strings.Repeat("A Very Long Episode Title ", 6)

	r := d.begin(7, long)
	r.progress(0.42, strings.Repeat("detail ", 20))
	r.finish(true, "1080p · 294.1 MB")

	// A wrapped line would make the next redraw start one row too low.
	for _, line := range strings.Split(escapes.ReplaceAllString(out.String(), ""), "\n") {
		if width := lipgloss.Width(strings.TrimPrefix(line, "\r")); width >= 50 {
			t.Errorf("line is %d columns wide on a 50 column terminal: %q", width, line)
		}
	}
	got := screen(t, out.String())
	if len(got) != 1 || !strings.HasPrefix(got[0], "[  7 / 152]  A Very") {
		t.Errorf("screen:\n%s", strings.Join(got, "\n"))
	}
}

func TestDisplayPlain(t *testing.T) {
	var out bytes.Buffer
	d := newDisplay(&out, false, 2)
	long := strings.TrimSpace(strings.Repeat("A Very Long Episode Title ", 6))

	first := d.begin(1, long)
	second := d.begin(2, "Second")
	first.status("trying hd")
	first.progress(0.5, "hd · 5/10 segments")
	if out.Len() != 0 {
		t.Fatalf("progress was written off a terminal: %q", out.String())
	}
	// Episodes are printed as they finish, whichever order that is.
	second.finish(false, "failed", "hd: nope")
	first.finish(true, "already downloaded")

	// One line per episode, with nothing cut short and no escape codes.
	want := "[2 / 2]  Second  ✗ failed\n" +
		"         hd: nope\n" +
		"[1 / 2]  " + long + "  ✓ done  already downloaded\n"
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}
