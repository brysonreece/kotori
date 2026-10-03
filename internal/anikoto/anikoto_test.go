package anikoto

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brysonreece/kotori/internal/fetch"
)

// fakeSite serves the pages and AJAX endpoints a watch URL leads to, ending
// in a player that uses the save_data backend.
func fakeSite(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/watch/my-show/ep-1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><h1 class="title d-title"> My Show </h1>
			<script>fetch("%s/anime/getinfo/42")</script></html>`, server.URL)
	})
	mux.HandleFunc("/ajax/episode/list/42", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"result": `<ul>
			<li data-html="true" title="Pilot"><a data-ids="ids-1" data-mal="7" data-timestamp="99"></a></li>
			<li data-html="true" title="Second"><a data-ids="ids-2" data-mal="7" data-timestamp="100"></a></li>
		</ul>`})
	})
	mux.HandleFunc("/ajax/server/list", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("servers") != "ids-1" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]string{"result": `
			<div class="type" data-type="sub"><ul><li data-link-id="L1">Vidplay - 1</li></ul></div>
			<div class="type" data-type="dub"><ul><li data-link-id="L2">HD</li></ul></div>`})
	})
	mux.HandleFunc("/ajax/server", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("get") != "L1" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"result": map[string]string{"url": server.URL + "/embed"}})
	})
	mux.HandleFunc("/embed", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != server.URL+"/" {
			http.Error(w, "missing referer", http.StatusForbidden)
			return
		}
		fmt.Fprintf(w, `<div id="player" data-ep-id="55"></div>
			<script>var settings = { type: 'sub', domain2_url: '%s', };</script>`, server.URL)
	})
	mux.HandleFunc("/save_data.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "55-sub" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"data": map[string]any{
			"sources": []map[string]string{{"url": "https://cdn.example/master.m3u8"}},
			"tracks":  []map[string]string{{"file": "https://cdn.example/en.vtt", "label": "English", "kind": "captions"}},
		}})
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestLoadServersAndResolve(t *testing.T) {
	server := fakeSite(t)
	ctx := context.Background()
	site := New(fetch.New())

	series, err := site.Load(ctx, server.URL+"/watch/my-show/ep-1")
	if err != nil {
		t.Fatal(err)
	}
	if series.Title != "My Show" || len(series.Episodes) != 2 {
		t.Fatalf("unexpected series %+v", series)
	}
	want := Episode{Number: 1, Title: "Pilot", IDs: "ids-1", MAL: "7", Timestamp: "99"}
	if series.Episodes[0] != want {
		t.Fatalf("got %+v, want %+v", series.Episodes[0], want)
	}

	servers, err := site.Servers(ctx, series.Episodes[0])
	if err != nil {
		t.Fatal(err)
	}
	wantServers := []Server{
		{Audio: "sub", Name: "vidplay", LinkID: "L1"},
		{Audio: "dub", Name: "hd", LinkID: "L2"},
	}
	if len(servers) != 2 || servers[0] != wantServers[0] || servers[1] != wantServers[1] {
		t.Fatalf("got %+v, want %+v", servers, wantServers)
	}

	stream, err := site.Resolve(ctx, servers[0], "sub")
	if err != nil {
		t.Fatal(err)
	}
	if stream.URL != "https://cdn.example/master.m3u8" || stream.Referer != server.URL {
		t.Errorf("unexpected stream %+v", stream)
	}
	if len(stream.Tracks) != 1 || stream.Tracks[0].Label != "English" {
		t.Errorf("unexpected tracks %+v", stream.Tracks)
	}

	// The embed only carries the sub stream, so asking for dub finds nothing.
	if _, err := site.Resolve(ctx, servers[0], "dub"); err == nil {
		t.Error("expected an error when the audio type does not match")
	}
}

func TestLoadRejectsOtherPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><h1>Home</h1></html>")
	}))
	defer server.Close()
	if _, err := New(fetch.New()).Load(context.Background(), server.URL); err == nil {
		t.Fatal("expected an error")
	}
}
