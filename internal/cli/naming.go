package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/brysonreece/kotori/internal/anikoto"
)

// defaultTemplate names files as "<Series>/<Series> E01 <Title>".
const defaultTemplate = "{series}/{series} E{episode} {title}"

// namingPresets are the file name patterns offered in the wizard.
var namingPresets = []struct{ name, template string }{
	{"Folder per show", defaultTemplate},
	{"Plex / Jellyfin", "{series}/Season 01/{series} - S01E{episode} - {title}"},
	{"No folders", "{series} E{episode} {title}"},
}

// placeholders are the names a template can use, with what each stands for.
var placeholders = []struct{ name, meaning string }{
	{"series", "the show or movie title"},
	{"episode", "the episode number, zero-padded"},
	{"title", "the episode title"},
	{"audio", "sub or dub"},
}

const templateHint = "use {series}, {episode}, {title} and {audio}; a / starts a folder"

var placeholderRe = regexp.MustCompile(`\{[^{}]*\}`)

// validateTemplate checks an output template before anything is downloaded.
func validateTemplate(template string) error {
	if strings.TrimSpace(template) == "" {
		return errors.New("the file name pattern is empty")
	}
	if filepath.IsAbs(template) || strings.HasPrefix(template, "~") {
		return errors.New("the file name pattern must be relative; the folder is set separately")
	}
	for _, part := range strings.Split(filepath.ToSlash(template), "/") {
		if part == ".." {
			return errors.New(`the file name pattern cannot contain ".."`)
		}
	}
	for _, found := range placeholderRe.FindAllString(template, -1) {
		known := false
		for _, p := range placeholders {
			known = known || found == "{"+p.name+"}"
		}
		if !known {
			return fmt.Errorf("unknown placeholder %s: %s", found, templateHint)
		}
	}
	// Without the episode number, every episode would be written to the same file.
	if !strings.Contains(template, "{episode}") {
		return errors.New("the file name pattern needs {episode}, or every episode would overwrite the last")
	}
	return nil
}

// expandTemplate fills in a template for one episode. The result is a
// relative path without an extension. Values are cleaned first, so a title
// can never add a folder of its own.
func expandTemplate(template string, series *anikoto.Series, ep anikoto.Episode, width int, audio string) string {
	values := strings.NewReplacer(
		"{series}", cleanName(series.Title),
		"{episode}", fmt.Sprintf("%0*d", width, ep.Number),
		"{title}", cleanName(ep.Title),
		"{audio}", audio,
	)
	var parts []string
	for _, part := range strings.Split(filepath.ToSlash(template), "/") {
		// An episode without a title would otherwise leave a dangling separator.
		part = strings.TrimRight(whitespace.ReplaceAllString(values.Replace(part), " "), " -_")
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return filepath.Join(parts...)
}

// expandHome replaces a leading ~ with the home directory, for paths typed
// where no shell does it.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

// commonDir is the deepest folder that contains every one of paths.
func commonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	dir := filepath.Dir(paths[0])
	for _, path := range paths[1:] {
		for {
			rel, err := filepath.Rel(dir, path)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return dir
}

// fileSize writes a size the way people read them: megabytes until there
// is a gigabyte to speak of.
func fileSize(n int64) string {
	if n >= 1e9 {
		return fmt.Sprintf("%.2f GB", float64(n)/1e9)
	}
	return megabytes(n)
}

// tally is the outcome of a run, for the closing summary.
type tally struct {
	downloaded, skipped, failed int
	bytes                       int64
	// paths are the video files that are now in place, new or not.
	paths []string
}

// lines words the closing summary: how much was downloaded, and where the
// files are.
func (t tally) lines() []string {
	var parts []string
	switch {
	case t.downloaded == 1:
		parts = append(parts, "Downloaded 1 episode", fileSize(t.bytes))
	case t.downloaded > 1:
		parts = append(parts, fmt.Sprintf("Downloaded %d episodes", t.downloaded), fileSize(t.bytes))
	default:
		parts = append(parts, "Nothing downloaded")
	}
	if t.skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d already downloaded", t.skipped))
	}
	if t.failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", t.failed))
	}
	lines := []string{strings.Join(parts, " · ")}

	// One file is named outright; several are placed by their shared folder.
	where := commonDir(t.paths)
	if len(t.paths) == 1 {
		where = t.paths[0]
	}
	if abs, err := filepath.Abs(where); err == nil && where != "" {
		where = abs
	}
	switch {
	case where == "":
	case t.downloaded > 0:
		lines = append(lines, "Saved to "+where)
	default:
		lines = append(lines, "Files are in "+where)
	}
	return lines
}
