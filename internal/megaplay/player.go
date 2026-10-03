// Package megaplay reads the MegaPlay embed player: it recovers the AES and
// signing parameters from the player script, decrypts the source list, and
// reproduces the player's CDN token.
//
// The player script is only ever treated as text. Nothing here evaluates
// downloaded JavaScript.
package megaplay

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Parameters are the values the player script embeds for decrypting sources
// and signing CDN URLs.
type Parameters struct {
	Key    string
	IV     string
	Secret string
	TTL    time.Duration
}

const jsStringPattern = `"(?:\\.|[^"\\])*"`

var (
	wrapperRe = regexp.MustCompile(`\breturn\s+eval\((` + jsStringPattern + `)\)`)
	objectRe  = regexp.MustCompile(`^\s*let\s+([\w$]+)\s*;`)
	payloadRe = regexp.MustCompile(`\}\)\((` + jsStringPattern + `)\)\s*$`)
	arrayRe   = regexp.MustCompile(`(?:let|const|var)\s+[\w$]+\s*=\s*(\[\s*(?:` + jsStringPattern + `\s*,?\s*)*\])`)
	stringRe  = regexp.MustCompile(jsStringPattern)
	paramsRe  = func() *regexp.Regexp {
		literal := `(` + jsStringPattern + `)`
		separator := `\s*(?:,|;\s*const\s+)\s*`
		return regexp.MustCompile(
			`\bconst\s+[\w$]+\s*=\s*` + literal +
				separator + `[\w$]+\s*=\s*` + literal +
				separator + `[\w$]+\s*=\s*` + literal +
				separator + `[\w$]+\s*=\s*(\d+)\s*;\s*function\s+[\w$]+\(`,
		)
	}()
)

// The decoded wrapper always begins with this initializer, which is what lets
// the XOR key be recovered without running anything.
const wrapperPrefix = "(function(){function "

// ParsePlayer extracts the crypto parameters from the player script.
func ParsePlayer(script string) (*Parameters, error) {
	if !strings.Contains(script, "AES-CBC") {
		unpacked, err := unpackStrings(script)
		if err != nil {
			return nil, err
		}
		script = unpacked
	}
	for _, m := range paramsRe.FindAllStringSubmatchIndex(script, -1) {
		nearby := script[m[1]:min(m[1]+12000, len(script))]
		if !strings.Contains(nearby, "AES-CBC") || !strings.Contains(nearby, "HMAC") {
			continue
		}
		ttl, err := strconv.ParseInt(script[m[8]:m[9]], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("megaplay: token lifetime: %w", err)
		}
		return &Parameters{
			Key:    string(jsString(script[m[2]:m[3]])),
			IV:     string(jsString(script[m[4]:m[5]])),
			Secret: string(jsString(script[m[6]:m[7]])),
			TTL:    time.Duration(ttl) * time.Second,
		}, nil
	}
	return nil, errors.New("megaplay: player crypto parameters not found; the player script has changed")
}

// unpackStrings statically decodes the player's XOR wrapper and string table,
// then inlines every table lookup so the script reads as plain literals.
func unpackStrings(script string) (string, error) {
	wrapperMatch := wrapperRe.FindStringSubmatch(script)
	objectMatch := objectRe.FindStringSubmatch(script)
	if wrapperMatch == nil || objectMatch == nil {
		return "", errors.New("megaplay: unrecognized obfuscated player wrapper")
	}
	wrapper := string(jsString(wrapperMatch[1]))
	payloadMatch := payloadRe.FindStringSubmatch(wrapper)
	if payloadMatch == nil {
		return "", errors.New("megaplay: obfuscated player payload missing")
	}
	payload := decodeURI(jsString(payloadMatch[1]))

	decoded, ok := xorUnwrap(payload)
	if !ok {
		return "", errors.New("megaplay: player XOR wrapper format changed")
	}
	table := stringTable(decoded)
	if table == nil {
		return "", errors.New("megaplay: player crypto string table could not be decoded")
	}

	lookupRe := regexp.MustCompile(regexp.QuoteMeta(objectMatch[1]) + `\.[\w$]+\((\d+)\)`)
	var lookupErr error
	out := lookupRe.ReplaceAllStringFunc(script, func(call string) string {
		index, err := strconv.Atoi(lookupRe.FindStringSubmatch(call)[1])
		if err != nil || index >= len(table) {
			lookupErr = errors.New("megaplay: player string-table index is out of range")
			return call
		}
		quoted, _ := json.Marshal(table[index])
		return string(quoted)
	})
	return out, lookupErr
}

// xorUnwrap recovers the repeating XOR key from the known wrapper prefix and
// decodes the payload with it.
func xorUnwrap(payload []rune) (string, bool) {
	prefix := []rune(wrapperPrefix)
	if len(payload) < len(prefix) {
		return "", false
	}
	known := make([]rune, len(prefix))
	for i := range prefix {
		known[i] = payload[i] ^ prefix[i]
	}
	for size := 1; size <= len(known)/2; size++ {
		if !periodic(known, size) {
			continue
		}
		candidate := make([]rune, len(payload))
		for i, c := range payload {
			candidate[i] = c ^ known[i%size]
		}
		text := string(candidate)
		if strings.HasSuffix(strings.TrimRight(text, " \t\r\n"), "})") {
			return text, true
		}
	}
	return "", false
}

func periodic(values []rune, size int) bool {
	for i, v := range values {
		if v != values[i%size] {
			return false
		}
	}
	return true
}

// stringTable finds the array holding the player's crypto vocabulary. Entries
// are XORed with a single-character mask, recovered from the entry that
// decodes to "AES-CBC".
func stringTable(decoded string) []string {
	const anchor = "AES-CBC"
	for _, array := range arrayRe.FindAllStringSubmatch(decoded, -1) {
		var entries [][]rune
		for _, literal := range stringRe.FindAllString(array[1], -1) {
			entries = append(entries, jsString(literal))
		}
		for _, entry := range entries {
			if len(entry) != len(anchor) {
				continue
			}
			mask := entry[0] ^ rune(anchor[0])
			if xorMask(entry, mask) != anchor {
				continue
			}
			table := make([]string, len(entries))
			found := map[string]bool{}
			for i, e := range entries {
				table[i] = xorMask(e, mask)
				found[table[i]] = true
			}
			if found["HMAC"] && found["SHA-256"] && found["importKey"] && found["decrypt"] {
				return table
			}
		}
	}
	return nil
}

func xorMask(in []rune, mask rune) string {
	out := make([]rune, len(in))
	for i, c := range in {
		out[i] = c ^ mask
	}
	return string(out)
}

var jsEscapes = map[rune]rune{'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v', '0': 0}

// jsString decodes a double-quoted JavaScript string literal. It returns
// runes rather than a string because the caller XORs individual code points.
func jsString(literal string) []rune {
	body := []rune(literal[1 : len(literal)-1])
	out := make([]rune, 0, len(body))
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' || i+1 >= len(body) {
			out = append(out, c)
			continue
		}
		i++
		e := body[i]
		switch {
		case e == 'x' && isHex(body, i+1, 2):
			out = append(out, hexRune(body[i+1:i+3]))
			i += 2
		case e == 'u' && isHex(body, i+1, 4):
			out = append(out, hexRune(body[i+1:i+5]))
			i += 4
		case e == '\r' && i+1 < len(body) && body[i+1] == '\n':
			i++ // line continuation
		case e == '\n':
			// line continuation
		default:
			if mapped, ok := jsEscapes[e]; ok {
				out = append(out, mapped)
			} else {
				out = append(out, e)
			}
		}
	}
	return out
}

func isHex(s []rune, start, n int) bool {
	if start+n > len(s) {
		return false
	}
	for _, c := range s[start : start+n] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func hexRune(digits []rune) rune {
	v, _ := strconv.ParseUint(string(digits), 16, 32)
	return rune(v)
}

// decodeURI mirrors JavaScript's decodeURI: percent escapes are decoded
// except those standing for reserved URI punctuation.
func decodeURI(in []rune) []rune {
	const reserved = ";/?:@&=+$,#"
	out := make([]rune, 0, len(in))
	for i := 0; i < len(in); {
		b, ok := percentByte(in, i)
		if !ok {
			out = append(out, in[i])
			i++
			continue
		}
		if b < utf8.RuneSelf {
			if strings.IndexByte(reserved, b) >= 0 {
				out = append(out, in[i:i+3]...)
			} else {
				out = append(out, rune(b))
			}
			i += 3
			continue
		}
		// A multi-byte UTF-8 sequence spans several consecutive escapes.
		buf := []byte{b}
		next := i + 3
		for len(buf) < utf8.UTFMax && !utf8.FullRune(buf) {
			nb, ok := percentByte(in, next)
			if !ok {
				break
			}
			buf = append(buf, nb)
			next += 3
		}
		r, size := utf8.DecodeRune(buf)
		if r == utf8.RuneError || size != len(buf) {
			out = append(out, in[i])
			i++
			continue
		}
		out = append(out, r)
		i = next
	}
	return out
}

func percentByte(s []rune, i int) (byte, bool) {
	if s[i] != '%' || !isHex(s, i+1, 2) {
		return 0, false
	}
	return byte(hexRune(s[i+1 : i+3])), true
}
