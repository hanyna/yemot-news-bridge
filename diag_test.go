//go:build diag

package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type sbuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *sbuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *sbuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestDiagDry(t *testing.T) {
	real := yemotBase
	var writes sbuf
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/")
		switch method {
		case "GetIVR2Dir", "GetTextFile":
			body, _ := io.ReadAll(r.Body)
			req, _ := http.NewRequest(http.MethodPost, real+method, bytes.NewReader(body))
			req.Header = r.Header.Clone()
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		default:
			_ = r.ParseMultipartForm(64 << 20)
			what := r.FormValue("what") + r.FormValue("path")
			c := r.FormValue("contents")
			if len([]rune(c)) > 90 {
				c = string([]rune(c)[:90])
			}
			fmt.Fprintf(&writes, "%s %s %q\n", method, what, c)
			w.Write([]byte(`{"responseStatus":"OK","message":"File upload expected"}`))
		}
	}))
	defer fake.Close()
	yemotBase = fake.URL + "/"
	var logs sbuf
	log.SetOutput(&logs)
	log.SetFlags(log.Ltime)
	cfg := config{
		feedURL: envOr("TGPOPUP_URL", "https://telegram-popup.onrender.com/api/messages"), feedKey: strings.TrimSpace(os.Getenv("TGPOPUP_KEY")),
		ext: "1", channelExts: true, audio: false, voice: "Elik_2100", rate: "0", publicList: "800", lineNumber: "0772263731",
		callback: true, adminList: "606", adminListen: "4", exclude: parseExclude("SamariaUpdates"),
		podcasts:   parsePodcasts("חושבים בקול של הקול היהודי | https://www.spreaker.com/show/4524009/episodes/feed"),
		podcastExt: "3", podcastKeep: 10,
		client: &http.Client{Timeout: 30 * time.Second}, feedClient: &http.Client{Timeout: 90 * time.Second},
	}
	cfg.y = &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	cfg.loc, _ = time.LoadLocation("Asia/Jerusalem")
	st := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}, special: map[string]bool{}}
	st.ensureMaps()
	for i := 1; i <= 2; i++ {
		err := syncOnce(&cfg, st)
		log.Printf("=== cycle %d err=%v", i, err)
	}
	var lines []string
	for _, s := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(s, "טריות: ערוץ") {
			lines = append(lines, s)
		}
	}
	out := "LOG:\n" + strings.Join(lines, "\n") + "\nWRITES:\n" + writes.String()
	r := strings.NewReplacer("%", "%25", "\r", "", "\n", "%0A")
	for i := 0; len(out) > 0 && i < 6; i++ {
		c := out
		if len(c) > 3800 {
			c = c[:3800]
		}
		out = out[len(c):]
		fmt.Printf("::notice title=dry-%d::%s\n", i, r.Replace(c))
	}
}
