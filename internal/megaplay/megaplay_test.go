package megaplay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/brysonreece/kotori/internal/aescbc"
)

// jsQuote renders s as a JavaScript string literal using only escapes that
// real JS understands.
func jsQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r > 0x7e:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func xorString(s, key string) string {
	out := []rune(s)
	k := []rune(key)
	for i := range out {
		out[i] ^= k[i%len(k)]
	}
	return string(out)
}

// obfuscatedPlayer builds a script shaped like the real player: a string
// table hidden behind a single-character mask, inside an XOR-wrapped
// initializer, with the crypto constants referenced only by table index.
func obfuscatedPlayer(table []string) string {
	var entries []string
	for _, s := range table {
		entries = append(entries, jsQuote(xorString(s, "\x05")))
	}
	inner := `(function(){function a(){var t=[` + strings.Join(entries, ",") + `];return t}return a})`
	payload := strings.ReplaceAll(xorString(inner, "k3y"), "%", "%25")
	wrapper := `(function(p){return p})(` + jsQuote(payload) + `)`
	return `let Q;Q={g(){return eval(` + jsQuote(wrapper) + `)}};` +
		`const k=Q.s(5),i=Q.s(6),h=Q.s(7),t=300;function d(){return [Q.s(0),Q.s(1),Q.s(2)]}`
}

func TestParsePlayerObfuscated(t *testing.T) {
	table := []string{
		"AES-CBC", "HMAC", "SHA-256", "importKey", "decrypt",
		"0123456789abcdef0123456789abcdef", "fedcba9876543210", "s3cret",
	}
	params, err := ParsePlayer(obfuscatedPlayer(table))
	if err != nil {
		t.Fatal(err)
	}
	want := Parameters{Key: table[5], IV: table[6], Secret: "s3cret", TTL: 300 * time.Second}
	if *params != want {
		t.Fatalf("got %+v, want %+v", *params, want)
	}
}

func TestParsePlayerPlain(t *testing.T) {
	script := `const a="key";const b="iv";const c="sec\x21",d=60;function f(){x("AES-CBC");y("HMAC")}`
	params, err := ParsePlayer(script)
	if err != nil {
		t.Fatal(err)
	}
	want := Parameters{Key: "key", IV: "iv", Secret: "sec!", TTL: time.Minute}
	if *params != want {
		t.Fatalf("got %+v, want %+v", *params, want)
	}
}

func TestParsePlayerRejectsUnknownScript(t *testing.T) {
	if _, err := ParsePlayer(`console.log("hello")`); err == nil {
		t.Fatal("expected an error for an unrecognized script")
	}
}

func TestJSString(t *testing.T) {
	cases := map[string]string{
		`"plain"`:     "plain",
		`"a\x41Bc"`:   "aABc",
		`"q\"\\\n\t"`: "q\"\\\n\t",
		`"\xzz"`:      "xzz",
		"\"a\\\nb\"":  "ab",
	}
	for literal, want := range cases {
		if got := string(jsString(literal)); got != want {
			t.Errorf("jsString(%s) = %q, want %q", literal, got, want)
		}
	}
}

func TestDecodeURI(t *testing.T) {
	cases := map[string]string{
		"a%20b":     "a b",
		"a%2Fb%3fc": "a%2Fb%3fc", // reserved punctuation stays escaped
		"%E2%82%AC": "€",
		"100%":      "100%",
		"%zz":       "%zz",
	}
	for in, want := range cases {
		if got := string(decodeURI([]rune(in))); got != want {
			t.Errorf("decodeURI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecryptSource(t *testing.T) {
	p := &Parameters{Key: "short-key", IV: "an-iv-longer-than-sixteen-bytes"}
	plain := []byte(`[{"file":"https://cdn.example/a/master.m3u8"}]`)
	ciphertext, err := aescbc.Encrypt(fixedBytes(p.Key, 32), fixedBytes(p.IV, 16), plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.DecryptSource(base64.RawURLEncoding.EncodeToString(ciphertext))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Fatalf("got %s, want %s", got, plain)
	}
	file, err := SourceFile(got)
	if err != nil || file != "https://cdn.example/a/master.m3u8" {
		t.Fatalf("SourceFile = %q, %v", file, err)
	}
}

func TestSourceFile(t *testing.T) {
	cases := map[string]string{
		`"https://x/a.m3u8"`:                           "https://x/a.m3u8",
		`{"file":"https://x/b.m3u8"}`:                  "https://x/b.m3u8",
		`[{"type":"hls"},{"file":"https://x/c.m3u8"}]`: "https://x/c.m3u8",
	}
	for raw, want := range cases {
		got, err := SourceFile([]byte(raw))
		if err != nil || got != want {
			t.Errorf("SourceFile(%s) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := SourceFile([]byte(`{"nope":1}`)); err == nil {
		t.Error("expected an error when no file is present")
	}
}

func TestSignedURL(t *testing.T) {
	p := &Parameters{Secret: "s3cret", TTL: 5 * time.Minute}
	now := time.Unix(1_700_000_000, 0)
	a, b := strings.Repeat("A1", 16), strings.Repeat("b2", 16)
	raw := "https://cdn.example/" + a + "/" + b + "/master.m3u8"

	signed, err := p.SignedURL(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	token, ok := strings.CutPrefix(signed, raw+"?token=")
	if !ok {
		t.Fatalf("unexpected signed URL %q", signed)
	}
	encodedMessage, encodedSig, _ := strings.Cut(token, ".")
	message, _ := base64.RawURLEncoding.DecodeString(encodedMessage)
	wantMessage := fmt.Sprintf("%d|%s/%s", now.Unix()+300, strings.ToLower(a), b)
	if string(message) != wantMessage {
		t.Fatalf("message = %q, want %q", message, wantMessage)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(message)
	if encodedSig != base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatal("signature does not verify")
	}

	if again, _ := p.SignedURL(signed, now); again != signed {
		t.Errorf("already-signed URL was changed: %q", again)
	}
	if withQuery, _ := p.SignedURL(raw+"?v=1", now); !strings.Contains(withQuery, "?v=1&token=") {
		t.Errorf("existing query not preserved: %q", withQuery)
	}
	if _, err := p.SignedURL("https://cdn.example/short/master.m3u8", now); err == nil {
		t.Error("expected an error for an unrecognized CDN path")
	}
}
