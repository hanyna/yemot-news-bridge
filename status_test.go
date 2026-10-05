package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGitHub struct {
	mu       sync.Mutex
	issues   []map[string]any
	bodies   map[int]string
	comments map[int][]string
	created  int
	deny     bool
}

func (g *fakeGitHub) handler(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer TOK" || g.deny {
		w.WriteHeader(403)
		io.WriteString(w, `{"message":"Resource not accessible by integration"}`)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	p := r.URL.Path
	switch {
	case r.Method == "GET" && p == "/repos/o/r/issues":
		json.NewEncoder(w).Encode(g.issues)
	case r.Method == "POST" && p == "/repos/o/r/issues":
		g.created++
		n := 100 + g.created
		g.issues = append(g.issues, map[string]any{"number": n, "title": in["title"]})
		json.NewEncoder(w).Encode(map[string]any{"number": n})
	case r.Method == "PATCH" && strings.HasPrefix(p, "/repos/o/r/issues/"):
		var n int
		_, _ = fmtSscanf(strings.TrimPrefix(p, "/repos/o/r/issues/"), &n)
		g.bodies[n] = in["body"].(string)
		io.WriteString(w, `{}`)
	case r.Method == "POST" && strings.HasSuffix(p, "/comments"):
		var n int
		_, _ = fmtSscanf(strings.TrimSuffix(strings.TrimPrefix(p, "/repos/o/r/issues/"), "/comments"), &n)
		g.comments[n] = append(g.comments[n], in["body"].(string))
		io.WriteString(w, `{}`)
	default:
		w.WriteHeader(404)
	}
}

func fmtSscanf(s string, n *int) (int, error) {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("nan")
		}
		v = v*10 + int(c-'0')
	}
	*n = v
	return 1, nil
}

func testStatus(t *testing.T, g *fakeGitHub) (*statusReport, *strings.Builder) {
	srv := httptest.NewServer(http.HandlerFunc(g.handler))
	old := statusAPI
	statusAPI = srv.URL
	t.Cleanup(func() { srv.Close(); statusAPI = old })
	t.Setenv("GITHUB_TOKEN", "TOK")
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("GITHUB_RUN_ID", "777")
	t.Setenv("STATUS", "")
	r := newStatusReport(time.FixedZone("IL", 3*3600))
	if r == nil {
		t.Fatal("nil report")
	}
	var out strings.Builder
	r.out = &out
	return r, &out
}

func TestStatusReport(t *testing.T) {
	g := &fakeGitHub{bodies: map[int]string{}, comments: map[int][]string{}}
	r, out := testStatus(t, g)

	r.Write([]byte("2026/10/05 03:00:00 סבב: 3 הודעות חדשות עלו לשלוחה 1\n"))
	r.Write([]byte("2026/10/05 03:00:01 העלאה ל-ivr2:/1/10001.tts נכשלה: HTTP 500\n"))
	r.Write([]byte("2026/10/05 03:00:21 העלאה ל-ivr2:/1/10003.tts נכשלה: HTTP 500\n"))
	r.Write([]byte("2026/10/05 03:00:41 קריאה מטלגרם נחסמה (elisha_yered) TOK\n"))
	if !strings.Contains(out.String(), "3 הודעות חדשות") {
		t.Fatal("log lines must still be written out")
	}
	r.cycle(nil, 0) // הדחיפה הראשונה מיד
	if g.created != 1 || len(g.bodies) != 1 {
		t.Fatalf("created %d bodies %v", g.created, g.bodies)
	}
	b := g.bodies[101]
	for _, want := range []string{"<!-- line-status ", `"run":"777"`, `"problems":3`, "| 2 |", "נחסמה", "/actions/runs/777"} {
		if !strings.Contains(b, want) {
			t.Errorf("body missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "TOK") || strings.Contains(b, "הודעות חדשות") {
		t.Fatalf("token leaked or normal line counted:\n%s", b)
	}
	// לא דוחפים שוב לפני 10 דקות
	r.cycle(errors.New("x"), 1)
	if len(g.comments) != 0 {
		t.Fatal("no comment before finish")
	}
	r.finish("הסתיימה כרגיל")
	if len(g.comments[101]) != 1 || !strings.Contains(g.comments[101][0], "סיכום הפעלה — הסתיימה כרגיל") || !strings.Contains(g.bodies[101], `"failed_cycles":1`) {
		t.Fatalf("finish: %v | %s", g.comments, g.bodies[101])
	}

	// הפעלה חדשה מוצאת את אותו Issue ולא פותחת חדש
	r2, _ := testStatus(t, g)
	r2.cycle(nil, 0)
	if g.created != 1 {
		t.Fatalf("second run opened another issue (%d)", g.created)
	}
}

func TestStatusNoPermission(t *testing.T) {
	g := &fakeGitHub{bodies: map[int]string{}, comments: map[int][]string{}, deny: true}
	r, out := testStatus(t, g)
	r.cycle(nil, 0)
	r.lastPush = time.Time{}
	r.cycle(nil, 0)
	if !r.disabled || strings.Count(out.String(), "אין הרשאה") != 1 {
		t.Fatalf("disabled=%v out=%q", r.disabled, out.String())
	}
}

func TestStatusOff(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	if newStatusReport(time.UTC) != nil {
		t.Fatal("no token → off")
	}
	t.Setenv("GITHUB_TOKEN", "x")
	t.Setenv("STATUS", "off")
	if newStatusReport(time.UTC) != nil {
		t.Fatal("STATUS=off")
	}
	var r *statusReport
	r.cycle(nil, 0) // nil בטוח
	r.finish("x")
}

func TestStatusKindsCapped(t *testing.T) {
	g := &fakeGitHub{bodies: map[int]string{}, comments: map[int][]string{}}
	r, _ := testStatus(t, g)
	for i := 0; i < statusMaxKinds+10; i++ {
		r.note("שגיאה מסוג " + strings.Repeat("א", i+1))
	}
	if len(r.problems) != statusMaxKinds || r.total != statusMaxKinds+10 {
		t.Fatalf("kinds %d total %d", len(r.problems), r.total)
	}
}
