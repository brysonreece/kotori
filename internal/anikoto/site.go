// Package anikoto scrapes the Anikoto site: series pages, episode lists, the
// servers offered for an episode, and the stream behind each server.
package anikoto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/brysonreece/kotori/internal/fetch"
)

// Sources lists the server names the site is known to offer.
var Sources = []string{"megaplay", "vidstream", "kiwi", "vidcloud", "vidplay", "hd"}

// Site is a session against one Anikoto domain.
type Site struct {
	http *fetch.Client
	// origin is the scheme and host the series page was finally served from.
	// The site moves between domains, so it is learned from the first request.
	origin string
}

// Series is a show and its episodes.
type Series struct {
	Title    string
	Episodes []Episode
}

// Episode is one entry of a series' episode list.
type Episode struct {
	Number    int
	Title     string
	IDs       string
	MAL       string
	Timestamp string
}

// Server is one playback option for an episode.
type Server struct {
	// Audio is "sub" or "dub".
	Audio  string
	Name   string
	LinkID string
}

// Track is a subtitle or caption track offered alongside a stream.
type Track struct {
	File  string `json:"file"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// Stream is a resolved HLS manifest and what is needed to fetch it.
type Stream struct {
	URL     string
	Referer string
	Tracks  []Track
}

// New returns a Site that makes its requests through c.
func New(c *fetch.Client) *Site {
	return &Site{http: c}
}

func (s *Site) headers(referer string) map[string]string {
	h := map[string]string{"X-Requested-With": "XMLHttpRequest"}
	if referer != "" {
		h["Referer"] = referer
	}
	return h
}

// Load fetches a watch page and the episode list of the series it belongs to.
func (s *Site) Load(ctx context.Context, pageURL string) (*Series, error) {
	resp, err := s.http.Get(ctx, pageURL, s.headers(""))
	if err != nil {
		return nil, err
	}
	s.origin = resp.URL.Scheme + "://" + resp.URL.Host

	idRe := regexp.MustCompile(regexp.QuoteMeta(s.origin) + `/anime/getinfo/(\d+)`)
	id := idRe.FindSubmatch(resp.Body)
	if id == nil {
		return nil, errors.New("anikoto: that page is not a series; check the slug or URL")
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.Body))
	if err != nil {
		return nil, err
	}
	series := &Series{Title: strings.TrimSpace(doc.Find("h1.title.d-title").First().Text())}
	if series.Title == "" {
		return nil, errors.New("anikoto: no series title on that page")
	}

	list, err := s.fragment(ctx, s.origin+"/ajax/episode/list/"+string(id[1])+"?vrf=")
	if err != nil {
		return nil, err
	}
	list.Find(`li[data-html="true"]`).Each(func(i int, li *goquery.Selection) {
		a := li.Find("a").First()
		series.Episodes = append(series.Episodes, Episode{
			Number:    i + 1,
			Title:     strings.TrimSpace(li.AttrOr("title", "")),
			IDs:       a.AttrOr("data-ids", ""),
			MAL:       a.AttrOr("data-mal", ""),
			Timestamp: a.AttrOr("data-timestamp", ""),
		})
	})
	if len(series.Episodes) == 0 {
		return nil, errors.New("anikoto: the episode list is empty")
	}
	return series, nil
}

// Servers lists the playback options for an episode.
func (s *Site) Servers(ctx context.Context, ep Episode) ([]Server, error) {
	list, err := s.fragment(ctx, s.origin+"/ajax/server/list?"+url.Values{"servers": {ep.IDs}}.Encode())
	if err != nil {
		return nil, err
	}
	var servers []Server
	list.Find("div.type").Each(func(_ int, group *goquery.Selection) {
		audio := strings.ToLower(group.AttrOr("data-type", ""))
		group.Find("li").Each(func(_ int, li *goquery.Selection) {
			// Labels look like "Vidplay - 2"; the part before the dash names the server.
			name, _, _ := strings.Cut(strings.ToLower(li.Text()), "-")
			servers = append(servers, Server{
				Audio:  audio,
				Name:   strings.TrimSpace(name),
				LinkID: li.AttrOr("data-link-id", ""),
			})
		})
	})
	return servers, nil
}

// fragment fetches an AJAX endpoint that returns HTML inside a JSON envelope.
func (s *Site) fragment(ctx context.Context, rawURL string) (*goquery.Document, error) {
	var envelope struct {
		Result string `json:"result"`
	}
	if err := s.http.GetJSON(ctx, rawURL, s.headers(""), &envelope); err != nil {
		return nil, err
	}
	return goquery.NewDocumentFromReader(strings.NewReader(envelope.Result))
}

// embedURL exchanges a server's link ID for the URL of its embedded player.
func (s *Site) embedURL(ctx context.Context, linkID string) (string, error) {
	var envelope struct {
		Result struct {
			URL string `json:"url"`
		} `json:"result"`
	}
	endpoint := s.origin + "/ajax/server?" + url.Values{"get": {linkID}}.Encode()
	if err := s.http.GetJSON(ctx, endpoint, s.headers(""), &envelope); err != nil {
		return "", err
	}
	if envelope.Result.URL == "" {
		return "", fmt.Errorf("anikoto: server %q returned no player URL", linkID)
	}
	return envelope.Result.URL, nil
}
