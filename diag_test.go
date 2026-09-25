//go:build diag

package main

// בדיקה חד-פעמית (רק בענף diag-vision): הרצה "יבשה" של סבב אחד של הגשר מול
// הנתונים האמיתיים — קריאות מימות המשיח אמיתיות, כתיבות מדומות (לא נוגעים בקו).
// מדווח דרך הערות (annotations) של GitHub — בלי לחשוף מפתחות.

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var noteCount int

func note(title, msg string) {
	r := strings.NewReplacer("%", "%25", "\r", "", "\n", "%0A")
	for len(msg) > 0 && noteCount < 9 {
		chunk := msg
		if len(chunk) > 3800 {
			chunk = chunk[:3800]
		}
		msg = msg[len(chunk):]
		noteCount++
		fmt.Printf("::notice title=%s-%d::%s\n", title, noteCount, r.Replace(chunk))
	}
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *safeBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestDiagDryRun(t *testing.T) {
	real := yemotBase
	var writes safeBuf
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
			fmt.Fprintf(&writes, "%s %s %s\n", time.Now().Format("15:04:05"), method, what)
			w.Write([]byte(`{"responseStatus":"OK","message":"File upload expected"}`))
		}
	}))
	defer fake.Close()
	yemotBase = fake.URL + "/"

	var logs safeBuf
	log.SetOutput(&logs)
	log.SetFlags(log.Ltime)

	cfg := config{
		feedURL:       envOr("TGPOPUP_URL", "https://telegram-popup.onrender.com/api/messages"),
		feedKey:       strings.TrimSpace(os.Getenv("TGPOPUP_KEY")),
		ext:           "1",
		channelExts:   true,
		audio:         true,
		audioMax:      20 * 60,
		voice:         "Elik_2100",
		rate:          "0",
		publicList:    "800",
		lineNumber:    "0772263731",
		callback:      true,
		adminList:     "606",
		exclude:       parseExclude("SamariaUpdates"),
		podcasts:      parsePodcasts("חושבים בקול של הקול היהודי | https://www.spreaker.com/show/4524009/episodes/feed"),
		podcastExt:    "3",
		podcastKeep:   10,
		client:        &http.Client{Timeout: 30 * time.Second},
		feedClient:    &http.Client{Timeout: 90 * time.Second},
	}
	cfg.y = &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	cfg.vision = newVisionClient(os.Getenv("GEMINI_API_KEY"), "", "on")
	cfg.loc, _ = time.LoadLocation("Asia/Jerusalem")
	st := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}, special: map[string]bool{}}
	st.ensureMaps()
	st.aw.start(&cfg)

	{
		now := time.Now().In(cfg.loc)
		items, err := fetchFeed(cfg.feedClient, cfg.feedURL, cfg.feedKey)
		var b strings.Builder
		fmt.Fprintf(&b, "fetch err=%v items=%d now=%d\n", err, len(items), now.Unix())
		clean := prepareClean(items)
		fmt.Fprintf(&b, "clean=%d\n", len(clean))
		txt, _, _ := cfg.y.read("1", archiveIndex)
		if i := strings.Index(txt, "\ne "); i > 0 {
			fmt.Fprintf(&b, "HEAD: %s\n", txt[:i])
		}
		a, err := loadArchive(&cfg, "1", "", true, now)
		fmt.Fprintf(&b, "load err=%v next=%d last=%d cutoff=%d entries=%d fresh=%v files=%d\n", err, a.next, a.last, a.cutoff, len(a.entries), a.fresh, len(a.files))
		n := 0
		for _, it := range clean {
			if it.TS < a.last-3*3600 {
				continue
			}
			_, inIdx := a.entries[itemKey(it)]
			n++
			if n > 40 {
				break
			}
			fmt.Fprintf(&b, "%s ts=%d has=%v inIdx=%v <cutoff=%v media=%v text=%.40q\n", itemKey(it), it.TS, a.has(it, now), inIdx, it.TS < a.cutoff, it.MediaOnly, it.Text)
		}
		note("has", b.String())
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 3; i++ {
			start := time.Now()
			err := syncOnce(&cfg, st)
			log.Printf("=== DIAG cycle %d took %v err=%v", i, time.Since(start).Round(time.Second), err)
			time.Sleep(20 * time.Second)
		}
	}()
	select {
	case <-done:
	case <-time.After(6 * time.Minute):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		stack := string(buf[:n])
		// רק ה-goroutines שבקוד שלנו
		var keep []string
		for _, g := range strings.Split(stack, "\n\n") {
			if strings.Contains(g, "yemot-news-bridge") && !strings.Contains(g, "testing.tRunner") || strings.Contains(g, "syncOnce") {
				keep = append(keep, g)
			}
		}
		note("stack", strings.Join(keep, "\n\n"))
	}
	time.Sleep(5 * time.Second)
	l := logs.String()
	// מסננים שורות "טריות" הרבות
	var lines []string
	for _, s := range strings.Split(l, "\n") {
		if strings.Contains(s, "טריות: ערוץ") {
			continue
		}
		lines = append(lines, s)
	}
	note("log", strings.Join(lines, "\n"))
	w := writes.String()
	if len(w) > 3000 {
		w = w[:3000]
	}
	note("writes", w)
}
