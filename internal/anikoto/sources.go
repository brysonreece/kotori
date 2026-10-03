package anikoto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/brysonreece/kotori/internal/megaplay"
)

const (
	megaplayOrigin = "https://megaplay.buzz"
	vidtubeOrigin  = "https://vidtube.site"
	kiwiMapper     = "https://mapper.nekostream.site"
	kiwiReferer    = "https://kwik.cx2.mewcdn.online"
)

var (
	dataIDRe  = regexp.MustCompile(`\bdata-id="(\d+)"`)
	epIDRe    = regexp.MustCompile(`\bdata-ep-id="(\d+)"`)
	typeRe    = regexp.MustCompile(`\btype:\s*'([^']+)'`)
	domain2Re = regexp.MustCompile(`\bdomain2_url:\s*'([^']+)'`)
)

// errNotApplicable means an embed page is not the kind an extractor handles,
// or carries the other audio type.
var errNotApplicable = errors.New("not applicable")

type embedPage struct {
	url  *url.URL
	html string
}

type extractor func(ctx context.Context, s *Site, page *embedPage, audio string) (*Stream, error)

// Resolve follows a server to its embedded player and extracts the stream.
func (s *Site) Resolve(ctx context.Context, server Server, audio string) (*Stream, error) {
	embed, err := s.embedURL(ctx, server.LinkID)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Get(ctx, embed, s.headers(s.origin+"/"))
	if err != nil {
		return nil, err
	}
	page := &embedPage{url: resp.URL, html: string(resp.Body)}

	// MegaPlay and VidTube embeds look alike, so let the host decide which
	// backend is asked first.
	extractors := []extractor{megaplaySource, vidtubeSource, savedSource}
	if strings.Contains(page.url.Host, "vidtub") {
		extractors = []extractor{vidtubeSource, savedSource, megaplaySource}
	}
	var errs []error
	for _, extract := range extractors {
		stream, err := extract(ctx, s, page, audio)
		if errors.Is(err, errNotApplicable) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		return stream, nil
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("anikoto: unsupported player at %s", page.url.Host)
	}
	return nil, errors.Join(errs...)
}

// sourcesResponse is the shape shared by the getSourcesNew endpoints.
type sourcesResponse struct {
	Sources json.RawMessage `json:"sources"`
	Enc     string          `json:"enc"`
	Tracks  []Track         `json:"tracks"`
}

func megaplaySource(ctx context.Context, s *Site, page *embedPage, _ string) (*Stream, error) {
	id := dataIDRe.FindStringSubmatch(page.html)
	if id == nil {
		return nil, errNotApplicable
	}
	var resp sourcesResponse
	endpoint := megaplayOrigin + "/stream/getSourcesNew?id=" + id[1]
	if err := s.http.GetJSON(ctx, endpoint, s.headers(""), &resp); err != nil {
		return nil, err
	}
	stream := &Stream{Referer: megaplayOrigin + "/", Tracks: resp.Tracks}
	if resp.Enc == "" {
		file, err := megaplay.SourceFile(resp.Sources)
		if err != nil {
			return nil, err
		}
		stream.URL = file
		return stream, nil
	}

	// Encrypted responses need the key material from the player script.
	params, err := s.playerParameters(ctx, page)
	if err != nil {
		return nil, err
	}
	plain, err := params.DecryptSource(resp.Enc)
	if err != nil {
		return nil, err
	}
	file, err := megaplay.SourceFile(plain)
	if err != nil {
		return nil, err
	}
	if stream.URL, err = params.SignedURL(file, time.Now()); err != nil {
		return nil, err
	}
	return stream, nil
}

func (s *Site) playerParameters(ctx context.Context, page *embedPage) (*megaplay.Parameters, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page.html))
	if err != nil {
		return nil, err
	}
	var src string
	doc.Find("script[src]").EachWithBreak(func(_ int, script *goquery.Selection) bool {
		if candidate := script.AttrOr("src", ""); strings.Contains(candidate, "e1-player") {
			src = candidate
			return false
		}
		return true
	})
	if src == "" {
		return nil, errors.New("anikoto: unsupported embed layout: MegaPlay player script missing")
	}
	scriptURL, err := page.url.Parse(src)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Get(ctx, scriptURL.String(), s.headers(""))
	if err != nil {
		return nil, err
	}
	return megaplay.ParsePlayer(string(resp.Body))
}

func vidtubeSource(ctx context.Context, s *Site, page *embedPage, audio string) (*Stream, error) {
	id := dataIDRe.FindStringSubmatch(page.html)
	kind := typeRe.FindStringSubmatch(page.html)
	if id == nil || kind == nil || !strings.EqualFold(kind[1], audio) {
		return nil, errNotApplicable
	}
	// The player sends each parameter twice; the endpoint is matched to that.
	query := "id=" + id[1] + "&type=" + url.QueryEscape(kind[1])
	var resp sourcesResponse
	endpoint := vidtubeOrigin + "/stream/getSourcesNew?" + query + "&" + query
	if err := s.http.GetJSON(ctx, endpoint, s.headers(vidtubeOrigin+"/"), &resp); err != nil {
		return nil, err
	}
	file, err := megaplay.SourceFile(resp.Sources)
	if err != nil {
		return nil, err
	}
	return &Stream{URL: file, Referer: vidtubeOrigin + "/", Tracks: resp.Tracks}, nil
}

func savedSource(ctx context.Context, s *Site, page *embedPage, audio string) (*Stream, error) {
	id := epIDRe.FindStringSubmatch(page.html)
	kind := typeRe.FindStringSubmatch(page.html)
	domain := domain2Re.FindStringSubmatch(page.html)
	if id == nil || kind == nil || domain == nil || !strings.EqualFold(kind[1], audio) {
		return nil, errNotApplicable
	}
	var resp struct {
		Data struct {
			Sources []struct {
				URL string `json:"url"`
			} `json:"sources"`
			Tracks []Track `json:"tracks"`
		} `json:"data"`
	}
	endpoint := strings.TrimRight(domain[1], "/") + "/save_data.php?id=" + id[1] + "-" + url.QueryEscape(kind[1])
	if err := s.http.GetJSON(ctx, endpoint, s.headers(s.origin), &resp); err != nil {
		return nil, err
	}
	if len(resp.Data.Sources) == 0 || resp.Data.Sources[0].URL == "" {
		return nil, errors.New("anikoto: no playable source in response")
	}
	return &Stream{URL: resp.Data.Sources[0].URL, Referer: s.origin, Tracks: resp.Data.Tracks}, nil
}

// Kiwi resolves an episode through the Kiwi mapper, which is keyed by
// MyAnimeList ID instead of by server.
func (s *Site) Kiwi(ctx context.Context, ep Episode, quality int, audio string) (*Stream, error) {
	var streams map[string]json.RawMessage
	endpoint := fmt.Sprintf("%s/api/mal/%s/%d/%s", kiwiMapper, ep.MAL, ep.Number, ep.Timestamp)
	headers := s.headers(s.origin)
	headers["Origin"] = s.origin
	if err := s.http.GetJSON(ctx, endpoint, headers, &streams); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(streams))
	for name := range streams {
		names = append(names, name)
	}
	sort.Strings(names)
	want := fmt.Sprint(quality)
	for _, name := range names {
		if !strings.Contains(name, "Stream") || !strings.Contains(name, want) {
			continue
		}
		var byAudio map[string]struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(streams[name], &byAudio) != nil || byAudio[audio].URL == "" {
			continue
		}
		embed, err := s.embedURL(ctx, byAudio[audio].URL)
		if err != nil {
			return nil, err
		}
		// The manifest URL rides in the fragment, base64 encoded.
		_, fragment, ok := strings.Cut(embed, "#")
		if !ok {
			continue
		}
		fragment = strings.NewReplacer("+", "-", "/", "_", "=", "").Replace(fragment)
		manifest, err := base64.RawURLEncoding.DecodeString(fragment)
		if err != nil {
			return nil, fmt.Errorf("anikoto: kiwi manifest: %w", err)
		}
		return &Stream{URL: string(manifest), Referer: kiwiReferer}, nil
	}
	return nil, fmt.Errorf("anikoto: kiwi has no %sp %s stream for episode %d", want, audio, ep.Number)
}
