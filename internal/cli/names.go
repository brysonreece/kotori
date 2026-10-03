package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/brysonreece/kotori/internal/anikoto"
)

var (
	unsafeChars = regexp.MustCompile(`[\\/*?:"<>|]`)
	whitespace  = regexp.MustCompile(`\s+`)
)

// cleanName makes a title safe to use as a file or directory name.
func cleanName(name string) string {
	name = unsafeChars.ReplaceAllString(name, "_")
	return strings.TrimSpace(whitespace.ReplaceAllString(name, " "))
}

// numberWidth is how many digits episode numbers are padded to, so files
// sort correctly: at least two, more for long-running series.
func numberWidth(total int) int {
	return max(2, len(strconv.Itoa(total)))
}

// selectEpisodes narrows the episode list to a spec such as "1,3,5-8".
// An empty spec selects everything; last keeps only the final episode.
func selectEpisodes(all []anikoto.Episode, spec string, last bool) ([]anikoto.Episode, error) {
	selected := all
	if strings.TrimSpace(spec) != "" {
		numbers, err := parseEpisodeSpec(spec, len(all))
		if err != nil {
			return nil, err
		}
		selected = make([]anikoto.Episode, 0, len(numbers))
		for _, n := range numbers {
			selected = append(selected, all[n-1])
		}
	}
	if last && len(selected) > 0 {
		selected = selected[len(selected)-1:]
	}
	return selected, nil
}

// parseEpisodeSpec expands a list of numbers and ranges into sorted, unique
// episode numbers between 1 and total.
func parseEpisodeSpec(spec string, total int) ([]int, error) {
	seen := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		from, to, isRange := strings.Cut(strings.TrimSpace(part), "-")
		if !isRange {
			to = from
		}
		start, err1 := strconv.Atoi(strings.TrimSpace(from))
		end, err2 := strconv.Atoi(strings.TrimSpace(to))
		if err1 != nil || err2 != nil || start > end {
			return nil, fmt.Errorf("invalid episode selection %q: use numbers and ranges like 1,3,5-8", part)
		}
		if start < 1 || end > total {
			return nil, fmt.Errorf("episode selection %q is outside 1-%d", part, total)
		}
		for n := start; n <= end; n++ {
			seen[n] = true
		}
	}
	numbers := make([]int, 0, len(seen))
	for n := range seen {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	return numbers, nil
}

var languageCodes = map[string]string{
	"arabic": "ar", "bengali": "bn", "bulgarian": "bg", "chinese": "zh", "croatian": "hr",
	"czech": "cs", "danish": "da", "dutch": "nl", "english": "en", "filipino": "fil",
	"finnish": "fi", "french": "fr", "german": "de", "greek": "el", "hebrew": "he",
	"hindi": "hi", "hungarian": "hu", "indonesian": "id", "italian": "it", "japanese": "ja",
	"korean": "ko", "malay": "ms", "norwegian": "no", "persian": "fa", "polish": "pl",
	"portuguese": "pt", "romanian": "ro", "russian": "ru", "serbian": "sr", "spanish": "es",
	"swedish": "sv", "tamil": "ta", "thai": "th", "turkish": "tr", "ukrainian": "uk",
	"urdu": "ur", "vietnamese": "vi",
}

// languageCode turns a track label such as "English (CC)" into a short code
// for the subtitle file name. Unknown languages fall back to the label's
// first word.
func languageCode(label string) string {
	fields := strings.Fields(strings.ToLower(label))
	if len(fields) == 0 {
		return "und"
	}
	if code, ok := languageCodes[fields[0]]; ok {
		return code
	}
	if name := cleanName(fields[0]); name != "" {
		return name
	}
	return "und"
}
