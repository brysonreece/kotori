package megaplay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/brysonreece/kotori/internal/aescbc"
)

var cdnPathRe = regexp.MustCompile(`(?i)/([a-f0-9]{32})/([a-f0-9]{32})/`)

// DecryptSource decrypts the "enc" field of a getSourcesNew response and
// returns the JSON it contains.
func (p *Parameters) DecryptSource(encoded string) ([]byte, error) {
	encoded = strings.NewReplacer("+", "-", "/", "_", "=", "").Replace(strings.TrimSpace(encoded))
	ciphertext, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("megaplay: encrypted source: %w", err)
	}
	// The player's TextEncoder helper truncates or zero-pads to the AES sizes.
	plain, err := aescbc.Decrypt(fixedBytes(p.Key, 32), fixedBytes(p.IV, 16), ciphertext)
	if err != nil {
		return nil, fmt.Errorf("megaplay: encrypted source: %w", err)
	}
	return plain, nil
}

// SignedURL appends the token the player would add to a CDN URL. URLs that
// already carry a token are returned unchanged.
func (p *Parameters) SignedURL(rawURL string, now time.Time) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Query().Has("token") {
		return rawURL, nil
	}
	path := cdnPathRe.FindStringSubmatch(u.Path)
	if path == nil {
		return "", errors.New("megaplay: unexpected CDN path; cannot reproduce the player's token")
	}
	expires := now.Add(p.TTL).Unix()
	message := fmt.Sprintf("%d|%s/%s", expires, strings.ToLower(path[1]), strings.ToLower(path[2]))
	mac := hmac.New(sha256.New, []byte(p.Secret))
	mac.Write([]byte(message))
	token := base64.RawURLEncoding.EncodeToString([]byte(message)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	separator := "?"
	if u.RawQuery != "" {
		separator = "&"
	}
	return rawURL + separator + "token=" + url.QueryEscape(token), nil
}

// SourceFile pulls the stream URL out of a sources value, which the service
// returns as a bare string, an object with a "file" key, or a list of those.
func SourceFile(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("megaplay: sources: %w", err)
	}
	if list, ok := v.([]any); ok {
		for _, item := range list {
			if file := fileOf(item); file != "" {
				return file, nil
			}
		}
		return "", errors.New("megaplay: no playable source in response")
	}
	if file := fileOf(v); file != "" {
		return file, nil
	}
	return "", errors.New("megaplay: no playable source in response")
}

func fileOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		file, _ := t["file"].(string)
		return file
	}
	return ""
}

func fixedBytes(s string, n int) []byte {
	b := make([]byte, n)
	copy(b, s)
	return b
}
