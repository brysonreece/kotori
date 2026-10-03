package hls

import (
	"bytes"
	"encoding/base64"
	"strings"

	"github.com/brysonreece/kotori/internal/aescbc"
)

// Some CDNs list segments under a proxy host where the final path component
// is the real segment URL, AES-CBC encrypted and base64url encoded. The key
// and IV are fixed values used by the site's web player.
var (
	obfuscatedHosts = []string{"mt.nekostream.site", "vidtub.kotocdn.site"}
	segmentURLKey   = append([]byte("i?LMTAx0Q6,:}50U"), make([]byte, 16)...)
	segmentURLIV    = []byte("W0;27ToaUpl_P%'c")
)

// revealSegments replaces obfuscated segment URLs with the real ones. Entries
// that do not decrypt to a URL are filler the player skips, so they are
// dropped; the second result counts them.
func revealSegments(segments []Segment) ([]Segment, int) {
	out := make([]Segment, 0, len(segments))
	dropped := 0
	for _, seg := range segments {
		if !isObfuscated(seg.URI) {
			out = append(out, seg)
			continue
		}
		real, ok := revealSegmentURL(seg.URI)
		if !ok {
			dropped++
			continue
		}
		seg.URI = real
		out = append(out, seg)
	}
	return out, dropped
}

func isObfuscated(uri string) bool {
	for _, host := range obfuscatedHosts {
		if strings.Contains(uri, host) {
			return true
		}
	}
	return false
}

func revealSegmentURL(uri string) (string, bool) {
	name := uri[strings.LastIndexByte(uri, '/')+1:]
	name, _, _ = strings.Cut(name, "?")
	name = strings.NewReplacer("+", "-", "/", "_", "=", "").Replace(name)
	ciphertext, err := base64.RawURLEncoding.DecodeString(name)
	if err != nil {
		return "", false
	}
	plain, err := aescbc.Decrypt(segmentURLKey, segmentURLIV, ciphertext)
	if err != nil || !bytes.HasPrefix(plain, []byte("http")) {
		return "", false
	}
	return string(plain), true
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// stripPNG removes the fake PNG image some servers prepend to each segment.
// The media data starts right after the IEND chunk and its 4-byte CRC.
func stripPNG(data []byte) []byte {
	if !bytes.HasPrefix(data, pngSignature) {
		return data
	}
	end := bytes.Index(data, []byte("IEND"))
	if end == -1 || end+8 > len(data) {
		return data
	}
	return data[end+8:]
}
