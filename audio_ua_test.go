package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// פרק פודקאסט יורד בזהות של הגשר (Buzzsprout חוסם דפדפן משרת ב-403),
// ואם דווקא היא נחסמת — ניסיון אחד בזהות של דפדפן.
func TestPodcastDownloadUserAgent(t *testing.T) {
	for _, blockPodcastUA := range []bool{false, true} {
		var seen []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ua := r.Header.Get("User-Agent")
			seen = append(seen, ua)
			if strings.HasPrefix(ua, "Mozilla/") != blockPodcastUA {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Write([]byte("mp3-bytes"))
		}))
		cfg := &config{}
		path := filepath.Join(t.TempDir(), "in")
		err := downloadMediaVia(cfg, &audioJob{key: "pod:3/2:x", kind: "p", src: srv.URL + "/e.mp3"}, path, false)
		srv.Close()
		if err != nil {
			t.Fatalf("blockPodcastUA=%v: %v (seen %q)", blockPodcastUA, err, seen)
		}
		if seen[0] != podcastUA {
			t.Fatalf("first attempt UA = %q, want %q", seen[0], podcastUA)
		}
		if want := map[bool]int{false: 1, true: 2}[blockPodcastUA]; len(seen) != want {
			t.Fatalf("blockPodcastUA=%v: %d requests, want %d", blockPodcastUA, len(seen), want)
		}
	}
}
