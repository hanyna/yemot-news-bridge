package main

// פודקאסטים (שלוחה 3): תפריט → 3/1, 3/2... — שלוחה לכל פודקאסט שב-PODCASTS.
//
// הגשר בודק את ההזנה (RSS) של כל פודקאסט כל podcastEvery. פרק חדש: הקול יורד
// ומוכן ברקע (audio.go — אותו תור כמו הקול של סרטונים), עולה לשלוחה כ-b.wav,
// ואז עולה ההקראה עם שם הפרק (b+1.tts). הפרק החדש ביותר מקבל את המספר הגבוה
// ביותר, ולכן נשמע ראשון; ההקראה נשמעת לפני הפרק. בכניסה לשלוחה נשמעת קודם
// הכותרת (99999.tts). נשמרים PODCAST_KEEP הפרקים האחרונים: כשפרק חדש מוכן,
// הישן ביותר נמחק. מה כבר בשלוחה — ב-podcast.txt שבה.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	podcastIndex    = "podcast.txt"
	podcastMaxSecs  = 4 * 3600  // פרק ארוך מזה — נחתך
	podcastMaxBytes = 600 << 20 // קובץ גדול מזה — לא מורידים
	podcastTimeout  = 10 * time.Minute
)

// podcastEvery: כל כמה זמן בודקים אם יצא פרק חדש. (משתנה — לבדיקות.)
var podcastEvery = 30 * time.Minute

// מצב של פרק.
const (
	podQueued = 1 // הקול ממתין (הורדה והעלאה ברקע)
	podAudio  = 2 // הקול בשלוחה; ההקראה עוד לא
	podDone   = 3 // בשלוחה, שלם
	podFailed = 4 // הקול לא ירד או לא עלה — הפרק לא נכנס
)

type podcastSource struct {
	name string // בתפריט ובכותרת: "חושבים בקול של הקול היהודי" (ריק — השם מההזנה)
	url  string // ההזנה (RSS)
}

// parsePodcasts: שורה לכל פודקאסט — "שם | קישור להזנה", או רק הקישור.
func parsePodcasts(s string) []podcastSource {
	var out []podcastSource
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, link := "", line
		if i := strings.LastIndex(line, "|"); i >= 0 {
			name, link = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		}
		if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
			log.Printf("הערה: שורה ב-PODCASTS בלי קישור להזנה — מדלג: %q", line)
			continue
		}
		out = append(out, podcastSource{name: name, url: link})
	}
	return out
}

type podEpisode struct {
	id    string
	base  int
	ts    int64
	state int
	// מההזנה (בזיכרון בלבד):
	title  string
	secs   int
	src    string
	queued bool // נכנס לתור בהפעלה הזו
}

type podcast struct {
	ext       string
	src       podcastSource
	title     string // מההזנה
	next      int
	last      int64 // ה-ts של הפרק החדש ביותר שנכנס
	fresh     bool  // אין עדיין אינדקס: בהפעלה הראשונה נכנסים PODCAST_KEEP הפרקים האחרונים
	eps       map[string]*podEpisode
	checked   time.Time // מתי נבדקה ההזנה לאחרונה (גם אם נכשלה)
	fetchedOK bool      // ההזנה נקראה בהפעלה הזו
	dirty     bool
}

func (p *podcast) name() string {
	if p.src.name != "" {
		return p.src.name
	}
	if t := cleanForSpeech(p.title); t != "" {
		return t
	}
	return "פודקאסט"
}

func (p *podcast) jobKey(id string) string { return "pod:" + p.ext + ":" + id }

func (p *podcast) encode() string {
	list := make([]*podEpisode, 0, len(p.eps))
	for _, e := range p.eps {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].base < list[j].base })
	var b strings.Builder
	b.WriteString("# yemot-news-bridge: פרקי הפודקאסט שבשלוחה. לא למחוק — בלי הקובץ הזה הגשר לא יודע אילו פרקים כבר כאן.\n")
	fmt.Fprintf(&b, "feed=%s\nnext=%d\nlast=%d\n", p.src.url, p.next, p.last)
	for _, e := range list {
		// שורה לכל פרק: p <מזהה> <מספר> <זמן פרסום> <מצב>
		fmt.Fprintf(&b, "p %s %d %d %d\n", e.id, e.base, e.ts, e.state)
	}
	return b.String()
}

func parsePodcastIndex(txt string) (feed string, next int, last int64, eps map[string]*podEpisode) {
	eps = map[string]*podEpisode{}
	for _, l := range strings.Split(strings.ReplaceAll(txt, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "feed="):
			feed = strings.TrimPrefix(l, "feed=")
		case strings.HasPrefix(l, "next="):
			next, _ = strconv.Atoi(strings.TrimPrefix(l, "next="))
		case strings.HasPrefix(l, "last="):
			last, _ = strconv.ParseInt(strings.TrimPrefix(l, "last="), 10, 64)
		case strings.HasPrefix(l, "p ") || strings.HasPrefix(l, "p\t"):
			f := strings.Fields(l)
			if len(f) < 5 {
				continue
			}
			e := &podEpisode{id: f[1]}
			var err error
			if e.base, err = strconv.Atoi(f[2]); err != nil {
				continue
			}
			if e.ts, err = strconv.ParseInt(f[3], 10, 64); err != nil {
				continue
			}
			if e.state, err = strconv.Atoi(f[4]); err != nil {
				continue
			}
			eps[e.id] = e
		}
	}
	return
}

// loadPodcast קורא את השלוחה של פודקאסט.
//   - יש אינדקס: ממשיכים ממנו (אחרי הקובץ האחרון שבשלוחה, אם יש מעבר ל-next).
//   - האינדקס של פודקאסט אחר (הוחלף ב-PODCASTS): הפרקים הקודמים נמחקים, ומתחילים מחדש.
//   - אין אינדקס אבל יש פרקים (האינדקס נמחק): ממשיכים אחריהם; רק פרקים חדשים נכנסים.
//   - שלוחה ריקה: בהפעלה הראשונה נכנסים PODCAST_KEEP הפרקים האחרונים.
func loadPodcast(cfg *config, ext string, src podcastSource, now time.Time) (*podcast, error) {
	p := &podcast{ext: ext, src: src, next: archiveFirst, eps: map[string]*podEpisode{}}
	txt, exists, err := cfg.y.read(ext, podcastIndex)
	if err != nil {
		return nil, err
	}
	info, err := cfg.y.dir(ext)
	if err != nil {
		return nil, err
	}
	files := archiveFiles(info.Files)
	if exists {
		feed, next, last, eps := parsePodcastIndex(txt)
		if feed != "" && feed != src.url {
			log.Printf("שלוחה %s: הפודקאסט בה הוחלף (%s ← %s) — הפרקים הקודמים נמחקים.", ext, feed, src.url)
			var paths []string
			for _, f := range files {
				paths = append(paths, ivrPath(ext, f))
			}
			for i := 0; i < len(paths); i += 50 {
				if err := cfg.y.remove(paths[i:min(i+50, len(paths))]); err != nil {
					return nil, fmt.Errorf("מחיקת הפרקים של הפודקאסט הקודם: %w", err)
				}
			}
			p.fresh, p.dirty = true, true
			return p, nil
		}
		if next > archiveFirst {
			p.next = next
		}
		p.last, p.eps = last, eps
	} else if len(files) == 0 {
		p.fresh, p.dirty = true, true
		return p, nil
	} else {
		log.Printf("אזהרה: בשלוחה %s יש פרקים אבל אין %s — ממשיך אחריהם; רק פרקים שיתפרסמו מעכשיו ייכנסו.", ext, podcastIndex)
		p.last, p.dirty = now.Unix(), true
		for _, f := range files { // כדי שיימחקו בתורם
			b := fileNum(f) &^ 1
			if id := fmt.Sprintf("x%d", b); p.eps[id] == nil {
				p.eps[id] = &podEpisode{id: id, base: b, state: podDone}
			}
		}
	}
	if len(files) > 0 {
		if top := fileNum(files[len(files)-1]); top >= p.next {
			p.next, p.dirty = top-top%2+2, true
		}
	}
	return p, nil
}

// podcastFor: הפודקאסט של שלוחה — נטען פעם אחת בכל הפעלה.
func (st *state) podcastFor(cfg *config, ext string, src podcastSource, now time.Time) (*podcast, error) {
	if p, ok := st.pods[ext]; ok && p.src.url == src.url {
		p.src = src // השם בתפריט יכול להשתנות
		return p, nil
	}
	if coolingDown(st, "podcast:"+ext) {
		return nil, errors.New("ממתין לניסיון חוזר")
	}
	p, err := loadPodcast(cfg, ext, src, now)
	if err != nil {
		st.failAt["podcast:"+ext] = time.Now()
		return nil, err
	}
	st.pods[ext] = p
	return p, nil
}

type rssItem struct {
	Title     string `xml:"title"`
	GUID      string `xml:"guid"`
	PubDate   string `xml:"pubDate"`
	Enclosure struct {
		URL  string `xml:"url,attr"`
		Type string `xml:"type,attr"`
	} `xml:"enclosure"`
	Duration string `xml:"duration"` // itunes:duration — "2671" או "44:31"
}

type rssFeed struct {
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

var feedHTTP = &http.Client{Timeout: 30 * time.Second}

// fetchPodcast קורא הזנת RSS: שם הפודקאסט והפרקים שיש להם קובץ קול, מהישן לחדש.
func fetchPodcast(link string) (string, []*podEpisode, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", "yemot-news-bridge (podcast feed reader)")
	resp, err := feedHTTP.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("סטטוס %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return "", nil, err
	}
	var f rssFeed
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict, d.Entity = false, xml.HTMLEntity
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := d.Decode(&f); err != nil {
		return "", nil, fmt.Errorf("הזנה לא תקינה: %w", err)
	}
	var eps []*podEpisode
	for _, it := range f.Channel.Items {
		src := strings.TrimSpace(it.Enclosure.URL)
		if src == "" || (it.Enclosure.Type != "" && !strings.HasPrefix(it.Enclosure.Type, "audio/")) {
			continue
		}
		ts := parseFeedTime(it.PubDate)
		if ts <= 0 {
			continue
		}
		key := strings.TrimSpace(it.GUID)
		if key == "" {
			key = src
		}
		h := fnv.New64a()
		h.Write([]byte(key))
		eps = append(eps, &podEpisode{id: fmt.Sprintf("%016x", h.Sum64()), ts: ts,
			title: strings.TrimSpace(html.UnescapeString(it.Title)), secs: durationSecs(it.Duration), src: src})
	}
	sort.SliceStable(eps, func(i, j int) bool { return eps[i].ts < eps[j].ts })
	return strings.TrimSpace(html.UnescapeString(f.Channel.Title)), eps, nil
}

func parseFeedTime(s string) int64 {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST", "2 Jan 2006 15:04:05 -0700", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// sync: כל podcastEvery — בודקים בהזנה אם יש פרקים חדשים ומכניסים אותם לתור.
func (p *podcast) sync(cfg *config, st *state, now time.Time, keep int) {
	if !p.checked.IsZero() && time.Since(p.checked) < podcastEvery {
		return
	}
	p.checked = time.Now()
	title, feed, err := fetchPodcast(p.src.url)
	if err != nil {
		log.Printf("הערה: ההזנה של הפודקאסט %s (שלוחה %s) לא נקראה — ננסה שוב בעוד %v: %v", p.name(), p.ext, podcastEvery, err)
		return
	}
	p.title, p.fetchedOK = title, true
	inFeed := map[string]*podEpisode{}
	for _, f := range feed {
		inFeed[f.id] = f
	}
	for _, e := range p.eps { // הפרטים מההזנה — להקראה ולתור
		if f, ok := inFeed[e.id]; ok {
			e.title, e.secs, e.src = f.title, f.secs, f.src
		} else if e.state == podQueued {
			e.state, p.dirty = podFailed, true // הפרק ירד מההזנה לפני שהקול שלו עלה
		}
	}
	// פרקים חדשים: בהפעלה הראשונה — האחרונים; אחר כך — מה שחדש מהאחרון שנכנס.
	var add []*podEpisode
	for _, f := range feed {
		if p.eps[f.id] == nil && (p.fresh || f.ts > p.last) {
			add = append(add, f)
		}
	}
	if len(add) > keep {
		add = add[len(add)-keep:]
	}
	p.fresh = false
	for _, f := range add {
		if p.next > archiveLast {
			log.Printf("אזהרה: נגמר המספור בשלוחה %s — פרקים חדשים לא נכנסים.", p.ext)
			break
		}
		f.base, f.state = p.next, podQueued
		p.next += 2
		p.eps[f.id] = f
		if f.ts > p.last {
			p.last = f.ts
		}
		p.dirty = true
		log.Printf("פודקאסט %s: פרק חדש לשלוחה %s (%s): %.80s", p.name(), p.ext, introFile(f.base), f.title)
	}
	for _, e := range p.eps {
		if e.state == podQueued && !e.queued && e.src != "" {
			e.queued = true
			st.aw.add(&audioJob{key: p.jobKey(e.id), kind: "p", src: e.src, ts: e.ts}, audioTarget{ext: p.ext, base: e.base})
		}
	}
}

// onAudio: ה-worker סיים את הקול של פרק (audioTick).
func (p *podcast) onAudio(key string, audio int) {
	e := p.eps[strings.TrimPrefix(key, "pod:"+p.ext+":")]
	if e == nil || e.state != podQueued {
		return
	}
	e.queued, p.dirty = false, true
	if audio == audioDone {
		e.state = podAudio
	} else {
		e.state = podFailed
	}
}

// finish: ההקראה לפרקים שהקול שלהם עלה, מחיקת הישנים, ושמירת האינדקס.
func (p *podcast) finish(cfg *config, keep int) {
	for _, e := range p.sorted() {
		if e.state != podAudio {
			continue
		}
		if e.title == "" && !p.fetchedOK {
			continue // השם יגיע מההזנה
		}
		if err := cfg.y.upload(p.ext, introFile(e.base), podcastIntro(p, e, cfg.loc)); err != nil {
			log.Printf("הערה: ההקראה של פרק בשלוחה %s נכשלה — ננסה שוב: %v", p.ext, err)
			break
		}
		e.state, p.dirty = podDone, true
		log.Printf("פודקאסט %s: הפרק %s בשלוחה %s.", p.name(), introFile(e.base), p.ext)
	}
	for id, e := range p.eps {
		if e.state == podFailed {
			delete(p.eps, id) // ישן מ-last — לא ייכנס שוב
			p.dirty = true
		}
	}
	p.rotate(cfg, keep)
	if p.dirty {
		if err := cfg.y.upload(p.ext, podcastIndex, p.encode()); err != nil {
			log.Printf("הערה: שמירת %s בשלוחה %s נכשלה — ננסה שוב: %v", podcastIndex, p.ext, err)
			return
		}
		p.dirty = false
	}
}

func (p *podcast) sorted() []*podEpisode {
	list := make([]*podEpisode, 0, len(p.eps))
	for _, e := range p.eps {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].base < list[j].base })
	return list
}

// ready: כמה פרקים שלמים יש בשלוחה.
func (p *podcast) ready() int {
	n := 0
	for _, e := range p.eps {
		if e.state == podDone {
			n++
		}
	}
	return n
}

// rotate: נשארים keep הפרקים האחרונים שבשלוחה; הישנים נמחקים.
func (p *podcast) rotate(cfg *config, keep int) {
	var in []*podEpisode
	for _, e := range p.sorted() {
		if e.state == podDone || e.state == podAudio {
			in = append(in, e)
		}
	}
	if len(in) <= keep {
		return
	}
	old := in[:len(in)-keep]
	info, err := cfg.y.dir(p.ext)
	if err != nil {
		log.Printf("הערה: לא הצלחתי לבדוק את הקבצים בשלוחה %s (מחיקת פרקים ישנים): %v", p.ext, err)
		return
	}
	have := map[string]bool{}
	for _, n := range info.Files {
		have[strings.ToLower(n)] = true
	}
	var paths []string
	for _, e := range old {
		for _, n := range []string{audioFile(e.base), introFile(e.base)} {
			if have[n] {
				paths = append(paths, ivrPath(p.ext, n))
			}
		}
	}
	if len(paths) > 0 {
		if err := cfg.y.remove(paths); err != nil {
			log.Printf("הערה: מחיקת פרקים ישנים משלוחה %s נכשלה — ננסה שוב: %v", p.ext, err)
			return
		}
	}
	for _, e := range old {
		delete(p.eps, e.id)
	}
	p.dirty = true
	log.Printf("פודקאסט %s: נמחקו %d פרקים ישנים (נשמרים %d האחרונים).", p.name(), len(old), keep)
}

// podcastIntro: "<שם הפרק>. פורסם ב 8 בספטמבר, באורך 44 דקות."
func podcastIntro(p *podcast, e *podEpisode, loc *time.Location) string {
	title := cleanForSpeech(e.title)
	if title == "" {
		title = "פרק מתוך " + p.name()
	}
	s := title
	if e.ts > 0 {
		t := time.Unix(e.ts, 0).In(loc)
		s += fmt.Sprintf(". פורסם ב %d %s", t.Day(), months[t.Month()])
	}
	if e.secs >= 60 {
		m := (e.secs + 30) / 60
		if d := spokenDuration(fmt.Sprintf("%d:%02d:00", m/60, m%60)); d != "" {
			s += ", באורך " + d
		}
	}
	s += "."
	if r := []rune(s); len(r) > maxPerFile {
		s = cutAtWord(r[:maxPerFile]) + "."
	}
	return s
}

// syncPodcasts: לכל פודקאסט — השלוחה שלו (3/1, 3/2...), והפרקים החדשים לתור.
func (st *state) syncPodcasts(cfg *config, now time.Time) {
	if len(cfg.podcasts) == 0 || !setupSpecial(cfg, st, cfg.podcastExt, "type=menu\ndigits=1") {
		return
	}
	for i, src := range cfg.podcasts {
		if i >= 9 {
			log.Printf("הערה: בשלוחת הפודקאסטים יש מקום ל-9 — השאר לא נכנסים.")
			break
		}
		ext := cfg.podcastExt + "/" + strconv.Itoa(i+1)
		if !ensureChannelExt(cfg, st, "פודקאסט "+src.url, ext) {
			continue
		}
		p, err := st.podcastFor(cfg, ext, src, now)
		if err != nil {
			log.Printf("הערה: שלוחת הפודקאסט %s לא נטענה: %v%s", ext, err, aclHint(err, "GetTextFile"))
			continue
		}
		p.sync(cfg, st, now, cfg.podcastKeep)
	}
}

// finishPodcasts: אחרי audioTick — הקראות, מחיקת פרקים ישנים, ותפריט שלוחת הפודקאסטים.
// מחזיר true כשיש בה לפחות פודקאסט אחד עם פרק שלם — רק אז היא מוכרזת בתפריט הראשי.
func (st *state) finishPodcasts(cfg *config) bool {
	if len(cfg.podcasts) == 0 || !st.special[cfg.podcastExt] {
		return false
	}
	var opts []string
	for i, src := range cfg.podcasts {
		p := st.pods[cfg.podcastExt+"/"+strconv.Itoa(i+1)]
		if p == nil || p.src.url != src.url {
			continue
		}
		p.finish(cfg, cfg.podcastKeep)
		if p.ready() == 0 {
			continue // עוד אין מה לשמוע
		}
		st.ensureTitle(cfg, p.ext, p.name()+".")
		opts = append(opts, fmt.Sprintf("ל%s הקישו %d.", p.name(), i+1))
	}
	text := "שלוחה זו אינה פעילה כרגע."
	if len(opts) > 0 {
		text = "פודקאסטים. " + strings.Join(opts, " ")
	}
	if text != st.podMenuText {
		if err := uploadSpoken(cfg, cfg.podcastExt, "M1000.tts", text); err != nil {
			log.Printf("הערה: עדכון תפריט הפודקאסטים נכשל: %v", err)
		} else {
			st.podMenuText = text
			log.Printf("תפריט הפודקאסטים (שלוחה %s): %s", cfg.podcastExt, text)
		}
	}
	return len(opts) > 0
}
