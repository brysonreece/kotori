// Package hls downloads HTTP Live Streaming playlists to a single file.
package hls

import (
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Variant is one rendition listed in a master playlist.
type Variant struct {
	URI       string
	Bandwidth int
	Height    int
}

// Key describes the encryption applied to the segments that follow it.
type Key struct {
	Method string
	URI    string
	IV     []byte
}

// Segment is one media segment, with its URI resolved to an absolute URL.
type Segment struct {
	URI string
	Key *Key
	Seq int
}

// Playlist is a parsed master or media playlist.
type Playlist struct {
	Variants []Variant
	Segments []Segment
	// Map is the fMP4 initialization segment, if the playlist has one.
	Map string
	// AlternateAudio reports audio delivered as a separate rendition.
	AlternateAudio bool
}

var attrRe = regexp.MustCompile(`([A-Z0-9-]+)=("[^"]*"|[^,]*)`)

// Parse parses playlist text, resolving every URI against base.
func Parse(text string, base *url.URL) (*Playlist, error) {
	if !strings.HasPrefix(strings.TrimSpace(text), "#EXTM3U") {
		return nil, errors.New("hls: not an HLS playlist")
	}
	resolve := func(ref string) string {
		u, err := base.Parse(ref)
		if err != nil {
			return ref
		}
		return u.String()
	}

	var (
		p       Playlist
		pending *Variant
		key     *Key
		seq     int
	)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		tag, value, _ := strings.Cut(line, ":")
		switch {
		case line == "":
		case tag == "#EXT-X-STREAM-INF":
			attrs := parseAttrs(value)
			bandwidth, _ := strconv.Atoi(attrs["BANDWIDTH"])
			pending = &Variant{Bandwidth: bandwidth, Height: resolutionHeight(attrs["RESOLUTION"])}
		case tag == "#EXT-X-MEDIA":
			attrs := parseAttrs(value)
			if attrs["TYPE"] == "AUDIO" && attrs["URI"] != "" {
				p.AlternateAudio = true
			}
		case tag == "#EXT-X-MEDIA-SEQUENCE":
			seq, _ = strconv.Atoi(value)
		case tag == "#EXT-X-KEY":
			attrs := parseAttrs(value)
			if attrs["METHOD"] == "NONE" {
				key = nil
				break
			}
			key = &Key{Method: attrs["METHOD"], URI: resolve(attrs["URI"])}
			if iv := attrs["IV"]; iv != "" {
				key.IV, _ = hex.DecodeString(strings.TrimPrefix(strings.ToLower(iv), "0x"))
			}
		case tag == "#EXT-X-MAP":
			p.Map = resolve(parseAttrs(value)["URI"])
		case strings.HasPrefix(line, "#"):
		case pending != nil:
			pending.URI = resolve(line)
			p.Variants = append(p.Variants, *pending)
			pending = nil
		default:
			p.Segments = append(p.Segments, Segment{URI: resolve(line), Key: key, Seq: seq})
			seq++
		}
	}
	return &p, nil
}

func parseAttrs(s string) map[string]string {
	attrs := map[string]string{}
	for _, m := range attrRe.FindAllStringSubmatch(s, -1) {
		attrs[m[1]] = strings.Trim(m[2], `"`)
	}
	return attrs
}

func resolutionHeight(resolution string) int {
	_, h, ok := strings.Cut(strings.ToLower(resolution), "x")
	if !ok {
		return 0
	}
	height, _ := strconv.Atoi(h)
	return height
}

// SelectVariant picks the best rendition no taller than quality. If every
// rendition is taller it picks the smallest, and if none declares a
// resolution it picks the highest bitrate.
func SelectVariant(variants []Variant, quality int) Variant {
	var best Variant
	found := false
	for _, v := range variants {
		if v.Height == 0 || v.Height > quality {
			continue
		}
		if !found || v.Height > best.Height || v.Height == best.Height && v.Bandwidth > best.Bandwidth {
			best, found = v, true
		}
	}
	if found {
		return best
	}
	for _, v := range variants {
		if v.Height == 0 {
			continue
		}
		if !found || v.Height < best.Height {
			best, found = v, true
		}
	}
	if found {
		return best
	}
	for _, v := range variants {
		if v.Bandwidth >= best.Bandwidth {
			best = v
		}
	}
	return best
}
