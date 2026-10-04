package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

const barWidth = 24

// display reports the progress of a run of downloads, one line per episode:
//
//	[1 / 5]  Bulma and Son Goku  ✓ done  1080p · 294.1 MB
//	[2 / 5]  What the...?! No Balls!  ████████░░░░░░░░  52%
//	         hd · 166/318 segments · 153.2 MB · 6.1 MB/s · 0:25 left
//
// On a terminal the episodes in flight are redrawn in place, each with a
// progress bar and a line of detail beneath it. Elsewhere, each episode is
// printed once, when it finishes.
type display struct {
	mu    sync.Mutex
	w     io.Writer
	live  bool
	total int
	// width returns the terminal's width in columns.
	width func() int

	bar              progress.Model
	faint, good, bad lipgloss.Style

	// rows are the episodes in the live area, in episode order. A
	// finished episode stays there until every one above it has finished
	// too, so that lines never have to move once they scroll away.
	rows []*row
	// drawn is how many lines of the live area are on screen.
	drawn int
}

// row is one episode's place in the display.
type row struct {
	d     *display
	index int
	title string
	// fraction is how far the download has got, or negative before it starts.
	fraction float64
	text     string
	// notes are remarks to leave under the episode once it has finished.
	notes []string
	// final holds the lines to show once the episode has finished.
	final []string
}

func newDisplay(w io.Writer, live bool, total int) *display {
	r := lipgloss.NewRenderer(w)
	d := &display{
		w:     w,
		live:  live,
		total: total,
		width: func() int { return 80 },
		bar: progress.New(
			progress.WithDefaultGradient(),
			progress.WithWidth(barWidth),
			progress.WithoutPercentage(),
			progress.WithColorProfile(r.ColorProfile()),
		),
		faint: r.NewStyle().Faint(true),
		good:  r.NewStyle().Foreground(lipgloss.Color("2")),
		bad:   r.NewStyle().Foreground(lipgloss.Color("1")),
	}
	if f, ok := w.(*os.File); ok && live {
		d.width = func() int {
			if width, _, err := term.GetSize(f.Fd()); err == nil && width > 0 {
				return width
			}
			return 80
		}
	}
	return d
}

// begin starts reporting on an episode and returns its row.
func (d *display) begin(index int, title string) *row {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := &row{d: d, index: index, title: title, fraction: -1, text: "starting"}
	if d.live {
		if len(d.rows) == 0 {
			fmt.Fprint(d.w, "\033[?25l") // hide the cursor while lines are redrawn
		}
		// Episodes start from several goroutines at once, so they do not
		// necessarily get here in order. Keep the rows in episode order.
		at := sort.Search(len(d.rows), func(i int) bool { return d.rows[i].index > index })
		d.rows = slices.Insert(d.rows, at, r)
		d.redraw()
	}
	return r
}

// status changes the line of detail under the episode.
func (r *row) status(text string) {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	r.text = text
	r.d.redraw()
}

// progress moves the bar, and changes the line of detail with it.
func (r *row) progress(fraction float64, text string) {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	r.fraction, r.text = min(max(fraction, 0), 1), text
	r.d.redraw()
}

// note records a remark to show under the episode when it finishes.
func (r *row) note(text string) {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	r.notes = append(r.notes, text)
}

// finish replaces the progress bar with the outcome, and leaves any notes
// and details beneath it.
func (r *row) finish(ok bool, summary string, details ...string) {
	d := r.d
	d.mu.Lock()
	defer d.mu.Unlock()
	outcome := d.good.Render("✓ done")
	if !ok {
		outcome = d.bad.Render("✗ " + summary)
	} else if summary != "" {
		outcome += "  " + d.faint.Render(summary)
	}
	r.final = []string{r.headline(outcome)}
	for _, text := range append(r.notes, details...) {
		r.final = append(r.final, r.detail(text))
	}
	if !d.live {
		io.WriteString(d.w, strings.Join(r.final, "\n")+"\n")
		return
	}
	d.redraw()
}

// redraw repaints the live area, then lets go of the finished episodes at
// its top: their lines stay on screen but are no longer redrawn. It does
// nothing off a terminal.
func (d *display) redraw() {
	if !d.live {
		return
	}
	var b strings.Builder
	if d.drawn > 0 {
		// Back to the top of the live area, then clear to the end of the screen.
		fmt.Fprintf(&b, "\033[%dA\r\033[J", d.drawn)
	}
	settled := true
	d.drawn = 0
	keep := d.rows[:0]
	for _, r := range d.rows {
		lines := r.final
		if lines == nil {
			tail := d.faint.Render("…")
			if r.fraction >= 0 {
				tail = fmt.Sprintf("%s %3.0f%%", d.bar.ViewAs(r.fraction), r.fraction*100)
			}
			lines = []string{r.headline(tail), r.detail(r.text)}
			settled = false
		}
		b.WriteString(strings.Join(lines, "\n") + "\n")
		if !settled {
			keep = append(keep, r)
			d.drawn += len(lines)
		}
	}
	d.rows = keep
	if len(d.rows) == 0 {
		b.WriteString("\033[?25h")
	}
	io.WriteString(d.w, b.String())
}

// prefix is the episode counter that starts each headline.
func (r *row) prefix() string {
	return fmt.Sprintf("[%*d / %d]  ", len(fmt.Sprint(r.d.total)), r.index, r.d.total)
}

// headline is the episode's own line: counter, title, then tail. The title
// gives way when the terminal is too narrow for all three, because a line
// that wrapped would throw off the redraw.
func (r *row) headline(tail string) string {
	prefix := r.prefix()
	if !r.d.live {
		return prefix + r.title + "  " + tail
	}
	width := r.d.width()
	room := max(width-1-len(prefix)-2-lipgloss.Width(tail), 8)
	line := prefix + ansi.Truncate(r.title, room, "…") + "  " + tail
	return ansi.Truncate(line, width-1, "…")
}

// detail is a line of supporting text, indented to sit under the title.
func (r *row) detail(text string) string {
	indent := strings.Repeat(" ", len(r.prefix()))
	if r.d.live {
		text = ansi.Truncate(text, max(r.d.width()-1-len(indent), 8), "…")
	}
	return indent + r.d.faint.Render(text)
}
