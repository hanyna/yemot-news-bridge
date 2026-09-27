package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestGetJSONUsesETag(t *testing.T) {
	var full, notMod int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			atomic.AddInt32(&notMod, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		atomic.AddInt32(&full, 1)
		w.Write([]byte(`{"items":[{"id":1,"channel":"a","text":"שלום"}]}`))
	}))
	defer srv.Close()
	for i := 0; i < 3; i++ {
		items, err := fetchFeed(srv.Client(), srv.URL+"/api/etagtest", "k")
		if err != nil || len(items) != 1 || items[0].Text != "שלום" {
			t.Fatalf("round %d: %v %+v", i, err, items)
		}
	}
	if full != 1 || notMod != 2 {
		t.Fatalf("full=%d notModified=%d", full, notMod)
	}
}

func TestVideoDirectDownload(t *testing.T) {
	var cdnOK atomic.Bool
	var proxied, cdnHits int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&cdnHits, 1)
		if !cdnOK.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte("CDN-VIDEO"))
	}))
	defer cdn.Close()
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("redirect") == "1" {
			http.Redirect(w, r, cdn.URL+"/v.mp4", http.StatusFound)
			return
		}
		atomic.AddInt32(&proxied, 1)
		w.Write([]byte("PROXY-VIDEO"))
	}))
	defer feed.Close()
	cfg := &config{feedURL: feed.URL + "/api/messages", feedKey: "k"}
	dir := t.TempDir()
	job := &audioJob{key: "a/2", kind: "v", channel: "a", id: 2}

	cdnOK.Store(true)
	p := filepath.Join(dir, "1.mp4")
	if err := downloadMedia(cfg, job, p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "CDN-VIDEO" || proxied != 0 {
		t.Fatalf("direct: %q proxied=%d", b, proxied)
	}

	cdnOK.Store(false) // טלגרם דוחה — נופלים חזרה להורדה דרך השרת
	p = filepath.Join(dir, "2.mp4")
	if err := downloadMedia(cfg, job, p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "PROXY-VIDEO" || proxied != 1 || cdnHits != 2 {
		t.Fatalf("fallback: %q proxied=%d cdn=%d", b, proxied, cdnHits)
	}
}
