package main

// דוח מצב הקו ב-GitHub: הגשר מעדכן כל כמה דקות Issue קבוע בריפו ("מצב הקו —
// דוח אוטומטי"), עם הבעיות שהופיעו בלוג. בסוף כל הפעלה נוספת תגובה עם סיכום
// ההפעלה. הבדיקה היומית (משימה מתוזמנת של Claude) קוראת את ה-Issue בלי הרשאות
// מיוחדות: אם הוא לא עודכן זמן רב — הקו תקוע; אחרת — רואים בדיוק מה נכשל.
//
// צריך ב-bridge.yml: permissions → issues: write, ו-GITHUB_TOKEN בסביבה.
// STATUS=off מכבה. תקלה בדיווח לא משפיעה על הקו.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	statusTitle    = "מצב הקו — דוח אוטומטי"
	statusEvery    = 10 * time.Minute // כל כמה זמן ה-Issue מתעדכן
	statusMaxKinds = 25               // כמה סוגי בעיות שונים נשמרים
)

// statusAPI: כתובת ה-API של GitHub (בטסטים — שרת מזויף).
var statusAPI = "https://api.github.com"

// reProblem: שורות לוג שהן בעיה (עברית ואנגלית).
// (\berror\b — לא "password_error_goto" שבהגדרות שלוחה; "חוסמים" לא — מופיע בשורת הפתיחה הרגילה.)
var reProblem = regexp.MustCompile(`(?i)נכשל|לא נקראה|שגיאה|חריגה|לא התקבל|חסומ|נחסמ|לא תקין|לא הצלחתי|\bpanic\b|\berror\b|\bfailed\b|\bfatal\b|HTTP [45]\d\d`)

// reNum: מספרים בשורה — כדי ששורות שונות רק במספר ייספרו כאותה בעיה.
var reNum = regexp.MustCompile(`\d+`)

// מדדי איכות: דברים שלא משביתים את הקו, אבל פוגעים במה שהמאזין שומע.
// כל מדד נספר לפי שורת הלוג שהגשר כבר כותב. הסדר כאן = הסדר בדוח.
type qualityRule struct {
	key, label string
	bad        bool // true = פגיעה באיכות (false = ספירה רגילה, להשוואה)
	re         *regexp.Regexp
}

var qualityRules = []qualityRule{
	{"voiced", "הודעות שעלו בקול המוכן (Gemini)", false, regexp.MustCompile(`^קול מוכן: [^f][^|]`)},
	{"voice_paused", "הקול המוכן הושהה (מכסה/עומס) — הודעות הושמעו בקול הממוחשב", true, regexp.MustCompile(`יצירת קול מושהית`)},
	{"voice_failed", "הודעות שנשארו בקול הממוחשב (הקול המוכן נכשל)", true, regexp.MustCompile(`הקול של .* לא עלה \(נשאר טקסט\)`)},
	{"photo_ok", "תמונות שקיבלו תיאור", false, regexp.MustCompile(`^ניתוח תמונה \S+: `)},
	{"photo_failed", "ניתוח תמונה נכשל — תמונה בלי תיאור", true, regexp.MustCompile(`^ניתוח תמונה .*נכשל`)},
	{"media_ok", "קול של סרטונים/קוליות שהועלה", false, regexp.MustCompile(`^קול הועלה: `)},
	{"media_retry", "קול של סרטון/קולית נכשל (ינסה שוב)", true, regexp.MustCompile(`^הערה: הקול של .* נכשל \(ניסיון`)},
	{"media_failed", "סרטונים/קוליות שהקול שלהם לא יעלה בכלל", true, regexp.MustCompile(`הקול של .* לא יועלה`)},
	{"tg_blocked", "טלגרם חסם/נכשל בקריאת ערוץ (עיכוב בהודעות)", true, regexp.MustCompile(`^טלגרם: ערוץ \S+ — .*מנסה שוב`)},
	{"skipped", "הודעות שדולגו ולא עלו לקו בכלל!", true, regexp.MustCompile(`מדלג על הודעה`)},
	{"trimmed", "הודעות ישנות שנמחקו (שלוחה הגיעה ל-3,000 קבצים)", false, regexp.MustCompile(`הגיעה לגבול`)},
}

// עיכוב: מהפרסום בטלגרם עד שההודעה עלתה לקו (שלוחה ראשית בלבד, בלי ייבוא ישן).
const (
	latencySlow = 5 * time.Minute
	latencyMax  = 6 * time.Hour
)

// lineStatus: הדוח של ההפעלה הזו (nil = כבוי), כדי ש-archive.add ידווח עיכוב.
var lineStatus *statusReport

// latency: ההודעה עלתה לשלוחה הראשית אחרי age מהפרסום.
func (r *statusReport) latency(age time.Duration) {
	if r == nil || age < 0 || age > latencyMax {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.latN++
	r.latSum += age
	if age > r.latMax {
		r.latMax = age
	}
	if age > latencySlow {
		r.latSlow++
	}
}

type problem struct {
	Example string
	Count   int
	First   time.Time
	Last    time.Time
}

type statusReport struct {
	mu       sync.Mutex
	token    string
	repo     string
	runID    string
	runURL   string
	client   *http.Client
	loc      *time.Location
	started  time.Time
	deadline time.Time
	issue    int
	disabled bool

	cycles, failed, streak int
	lastOK                 time.Time
	problems               map[string]*problem
	total                  int
	quality                map[string]int
	latN, latSlow          int
	latSum, latMax         time.Duration
	lastPush               time.Time
	out                    io.Writer
}

// newStatusReport: nil בלי GITHUB_TOKEN או עם STATUS=off.
func newStatusReport(loc *time.Location) *statusReport {
	tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if tok == "" || strings.EqualFold(strings.TrimSpace(os.Getenv("STATUS")), "off") {
		return nil
	}
	repo := envOr("GITHUB_REPOSITORY", "hanyna/yemot-news-bridge")
	r := &statusReport{
		token:    tok,
		repo:     repo,
		runID:    os.Getenv("GITHUB_RUN_ID"),
		client:   &http.Client{Timeout: 20 * time.Second},
		loc:      loc,
		started:  time.Now(),
		problems: map[string]*problem{},
		quality:  map[string]int{},
		out:      os.Stderr,
	}
	if r.runID != "" {
		r.runURL = envOr("GITHUB_SERVER_URL", "https://github.com") + "/" + repo + "/actions/runs/" + r.runID
	}
	return r
}

// Write: כל שורת לוג עוברת כאן (log.SetOutput) — נכתבת כרגיל, ובעיות נאספות.
func (r *statusReport) Write(p []byte) (int, error) {
	n, err := r.out.Write(p)
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		line = stripLogDate(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		r.measure(line)
		if reProblem.MatchString(line) {
			r.note(line)
		}
	}
	return n, err
}

// stripLogDate: בלי התאריך שהלוג מוסיף בהתחלה ("2026/10/05 03:00:00 ").
func stripLogDate(line string) string {
	if len(line) > 20 && line[4] == '/' && line[7] == '/' && line[19] == ' ' {
		return strings.TrimSpace(line[20:])
	}
	return line
}

// measure: סופר את מדדי האיכות לפי שורת הלוג.
func (r *statusReport) measure(line string) {
	for _, q := range qualityRules {
		if q.re.MatchString(line) {
			r.mu.Lock()
			r.quality[q.key]++
			r.mu.Unlock()
			return
		}
	}
}

func (r *statusReport) note(line string) {
	line = stripLogDate(line)
	line = strings.ReplaceAll(line, r.token, "***")
	if len([]rune(line)) > 300 {
		line = string([]rune(line)[:300]) + "…"
	}
	key := reNum.ReplaceAllString(line, "#")
	if k := []rune(key); len(k) > 120 {
		key = string(k[:120])
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total++
	if p, ok := r.problems[key]; ok {
		p.Count++
		p.Last = now
		p.Example = line
		return
	}
	if len(r.problems) >= statusMaxKinds { // מפנים את הישנה ביותר
		var oldK string
		var old time.Time
		for k, p := range r.problems {
			if oldK == "" || p.Last.Before(old) {
				oldK, old = k, p.Last
			}
		}
		delete(r.problems, oldK)
	}
	r.problems[key] = &problem{Example: line, Count: 1, First: now, Last: now}
}

// cycle: אחרי כל סבב. מעדכן את ה-Issue פעם ב-statusEvery.
func (r *statusReport) cycle(err error, streak int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.cycles++
	r.streak = streak
	if err != nil {
		r.failed++
	} else {
		r.lastOK = time.Now()
	}
	due := time.Since(r.lastPush) >= statusEvery
	r.mu.Unlock()
	if due {
		r.push("")
	}
}

// finish: בסוף ההפעלה — עדכון אחרון + תגובה עם סיכום ההפעלה.
func (r *statusReport) finish(why string) {
	if r == nil {
		return
	}
	r.push(why)
}

func (r *statusReport) push(final string) {
	r.mu.Lock()
	if r.disabled {
		r.mu.Unlock()
		return
	}
	r.lastPush = time.Now()
	body := r.render(final)
	r.mu.Unlock()
	if err := r.ensureIssue(); err != nil {
		r.fail("פתיחת Issue", err)
		return
	}
	if err := r.api(http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", r.repo, r.issue), map[string]any{"body": body}, nil); err != nil {
		r.fail("עדכון Issue", err)
		return
	}
	if final != "" {
		if err := r.api(http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", r.repo, r.issue), map[string]any{"body": body}, nil); err != nil {
			r.fail("תגובת סיכום", err)
		}
	}
}

// fail: תקלה בדיווח — נכתבת ישר ל-stderr (לא דרך log, כדי לא להיספר כבעיה של הקו).
// בלי הרשאה (403) — מפסיקים לנסות בהפעלה הזו.
func (r *statusReport) fail(what string, err error) {
	fmt.Fprintf(r.out, "דוח מצב: %s: %v\n", what, err)
	if strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), "HTTP 401") {
		r.mu.Lock()
		r.disabled = true
		r.mu.Unlock()
		fmt.Fprintln(r.out, "דוח מצב: אין הרשאה (צריך issues: write ב-bridge.yml) — מפסיק לדווח בהפעלה הזו.")
	}
}

func (r *statusReport) ensureIssue() error {
	if r.issue != 0 {
		return nil
	}
	for page := 1; page <= 5; page++ {
		var list []struct {
			Number      int    `json:"number"`
			Title       string `json:"title"`
			PullRequest any    `json:"pull_request"`
		}
		if err := r.api(http.MethodGet, fmt.Sprintf("/repos/%s/issues?state=open&per_page=100&page=%d", r.repo, page), nil, &list); err != nil {
			return err
		}
		for _, it := range list {
			if it.Title == statusTitle && it.PullRequest == nil {
				r.issue = it.Number
				return nil
			}
		}
		if len(list) < 100 {
			break
		}
	}
	var created struct {
		Number int `json:"number"`
	}
	err := r.api(http.MethodPost, fmt.Sprintf("/repos/%s/issues", r.repo), map[string]any{
		"title": statusTitle,
		"body":  "הגשר מעדכן כאן את מצב הקו כל כמה דקות. לא לסגור.",
	}, &created)
	if err != nil {
		return err
	}
	r.issue = created.Number
	log.Printf("דוח מצב: נפתח Issue #%d (%s).", r.issue, statusTitle)
	return nil
}

func (r *statusReport) api(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, statusAPI+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		msg := string(raw)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// render: גוף ה-Issue. השורה הראשונה (הערה נסתרת) — לקריאה אוטומטית.
func (r *statusReport) render(final string) string {
	now := time.Now()
	t := func(x time.Time) string {
		if x.IsZero() {
			return "—"
		}
		return x.In(r.loc).Format("02.01 15:04")
	}
	meta, _ := json.Marshal(map[string]any{
		"updated": now.UTC().Format(time.RFC3339), "run": r.runID, "started": r.started.UTC().Format(time.RFC3339),
		"cycles": r.cycles, "failed_cycles": r.failed, "fail_streak": r.streak, "problems": r.total,
		"last_ok": func() string {
			if r.lastOK.IsZero() {
				return ""
			}
			return r.lastOK.UTC().Format(time.RFC3339)
		}(),
		"final": final, "quality": r.quality,
		"latency": map[string]any{"messages": r.latN, "avg_sec": r.latAvg().Seconds(), "max_sec": r.latMax.Seconds(), "over_5min": r.latSlow},
	})
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- line-status %s -->\n", meta)
	if final != "" {
		fmt.Fprintf(&b, "## סיכום הפעלה — %s\n\n", final)
	} else {
		fmt.Fprintf(&b, "## מצב הקו — עודכן %s (שעון ישראל)\n\n", t(now))
		b.WriteString("אם השעה כאן ישנה מ-30 דקות — הגשר לא רץ, והקו לא מתעדכן.\n\n")
	}
	run := r.runID
	if r.runURL != "" {
		run = "[" + r.runID + "](" + r.runURL + ")"
	}
	fmt.Fprintf(&b, "- הפעלה: %s, התחילה %s\n", run, t(r.started))
	fmt.Fprintf(&b, "- סבבים: %d, מהם נכשלו %d (כרגע %d ברצף). סבב מוצלח אחרון: %s\n", r.cycles, r.failed, r.streak, t(r.lastOK))
	fmt.Fprintf(&b, "- שורות בעיה בלוג: %d\n\n", r.total)
	r.renderQuality(&b)
	if len(r.problems) == 0 {
		b.WriteString("אין בעיות בהפעלה הזו.\n")
		return b.String()
	}
	ps := make([]*problem, 0, len(r.problems))
	for _, p := range r.problems {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Last.After(ps[j].Last) })
	b.WriteString("### בעיות (האחרונה למעלה)\n\n| פעמים | ראשונה | אחרונה | דוגמה |\n|---|---|---|---|\n")
	for _, p := range ps {
		ex := strings.NewReplacer("|", "/", "\n", " ", "`", "'").Replace(p.Example)
		fmt.Fprintf(&b, "| %d | %s | %s | %s |\n", p.Count, t(p.First), t(p.Last), ex)
	}
	return b.String()
}

func (r *statusReport) latAvg() time.Duration {
	if r.latN == 0 {
		return 0
	}
	return r.latSum / time.Duration(r.latN)
}

// renderQuality: טבלת האיכות — מה המאזין שמע בפועל.
func (r *statusReport) renderQuality(b *strings.Builder) {
	b.WriteString("### איכות הקו בהפעלה הזו\n\n| | מה | כמה |\n|---|---|---|\n")
	for _, q := range qualityRules {
		n := r.quality[q.key]
		mark := ""
		if q.bad && n > 0 {
			mark = "⚠️"
		}
		fmt.Fprintf(b, "| %s | %s | %d |\n", mark, q.label, n)
	}
	if r.latN > 0 {
		mark := ""
		if r.latSlow > 0 {
			mark = "⚠️"
		}
		fmt.Fprintf(b, "| %s | עיכוב מהפרסום בטלגרם עד שעלה לקו: ממוצע %s, הכי איטי %s | %d הודעות, מהן %d אחרי יותר מ-5 דקות |\n",
			mark, shortDur(r.latAvg()), shortDur(r.latMax), r.latN, r.latSlow)
	}
	b.WriteString("\n")
}

func shortDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d שניות", int(d.Seconds()))
	}
	return fmt.Sprintf("%.1f דקות", d.Minutes())
}
