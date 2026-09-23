package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSpeech(t *testing.T) {
	cases := map[string]string{
		`כוחות צה״ל פעלו ביו"ש הלילה`:        "כוחות צהל פעלו ביהודה ושומרון הלילה",
		"בס״ד\nח\"כ פלוני ורה\"מ נפגשו":      "חבר הכנסת פלוני וראש הממשלה נפגשו",
		"המחיר עלה ב-5% ל-30₪ *דחוף* #חדשות": "המחיר עלה ב-5 אחוז ל-30 שקלים דחוף חדשות",
		"שלום @user https://t.me/x/1 סוף":    "שלום סוף",
		"שורה ראשונה\n\n\nשורה שנייה!!\n":    "שורה ראשונה. שורה שנייה!",
	}
	for in, want := range cases {
		if got := cleanForSpeech(in); got != want {
			t.Errorf("\nin:   %q\ngot:  %q\nwant: %q", in, got, want)
		}
	}
}

func TestWhen(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	now := time.Date(2026, 9, 22, 21, 0, 0, 0, loc)
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 9, 22, 20, 5, 0, 0, loc), "בשעה 8 ו 5 דקות בלילה"},
		{time.Date(2026, 9, 22, 20, 0, 0, 0, loc), "בשעה 8 בלילה"},
		{time.Date(2026, 9, 22, 8, 15, 0, 0, loc), "בשעה 8 ורבע בבוקר"},
		{time.Date(2026, 9, 22, 12, 30, 0, 0, loc), "בשעה 12 וחצי בצהריים"},
		{time.Date(2026, 9, 22, 17, 10, 0, 0, loc), "בשעה 5 ו 10 דקות אחר הצהריים"},
		{time.Date(2026, 9, 22, 18, 45, 0, 0, loc), "בשעה 6 ו 45 דקות בערב"},
		{time.Date(2026, 9, 22, 0, 20, 0, 0, loc), "בשעה 12 ו 20 דקות בלילה"},
		{time.Date(2026, 9, 21, 23, 0, 0, 0, loc), "אתמול בשעה 11 בלילה"},
		{time.Date(2026, 9, 19, 8, 30, 0, 0, loc), "ביום שבת בשעה 8 וחצי בבוקר"},
		{time.Date(2026, 9, 3, 8, 30, 0, 0, loc), "ב 3 בספטמבר בשעה 8 וחצי בבוקר"},
	} {
		if got := spokenWhen(c.t, now); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestDedupe(t *testing.T) {
	long := "הודעה חשובה מאוד על אירוע ביטחוני בצומת הגדול ליד היישוב"
	items := prepare([]FeedItem{
		{Channel: "b", TS: 200, Text: long + " 📢"},
		{Channel: "a", TS: 100, Text: long},
		{Channel: "c", TS: 300, Text: long + " הצטרפו"},
		{Channel: "d", TS: 400, Text: "משהו אחר לגמרי"},
	})
	ad := prepare([]FeedItem{
		{Channel: "x", TS: 1, Text: "אורי מלמד עם תיק עזה ביד, לא תקח גם? לרכישה במחיר מיוחד היכנסו עכשיו לאתר הרשמי t.me/a"},
		{Channel: "y", TS: 2, Text: "אורי מלמד עם תיק עזה ביד, לא תקח גם? לרכישה במחיר מיוחד היכנסו עכשיו לאתר הרשמי 👇 הזמינו"},
	})
	if len(ad) != 0 {
		t.Fatalf("ad not filtered: %+v", ad)
	}
	if len(items) != 2 || items[0].Channel != "a" {
		t.Fatalf("%+v", items)
	}
}

func TestIni(t *testing.T) {
	out, ch := setIniValues("type=menu\nvoice=Jacob\n", [][2]string{{"voice", "Sivan"}, {"rate", "-2"}})
	if !ch || out != "type=menu\nvoice=Sivan\nrate=-2\n" {
		t.Fatalf("%q", out)
	}
	if !isBridgeIni("type=playfile\nvoice=Sivan") || isBridgeIni("type=menu") || isBridgeIni("type=playfile\nx=1") {
		t.Fatal("isBridgeIni")
	}
}

// fakeYemotServer: שרת מדומה של ערוץ חי + ימות המשיח, שמתנהג כמו המערכת
// האמיתית (נבדק מולה ב-23/09/2026):
//   - UploadTextFile לשלוחה שלא קיימת מחזיר OK ולא עושה כלום.
//   - רק UpdateExtension יוצר שלוחה (ודורש הרשאה במפתח).
//   - GetIVR2Dir: "extension does not exist" לשלוחה שלא קיימת; קבצי .txt (כמו
//     bridge.txt) לא מופיעים ברשימה; ext.ini ברשימת ini; שלוחות-בת ב-dirs.
//
// dirs = השלוחות שקיימות ושמות הקבצים בכל אחת.
type fakeYemotServer struct {
	mu          sync.Mutex
	files       map[string]string   // נתיב מלא → תוכן שהועלה
	dirs        map[string][]string // "ivr2:/3" → שמות קבצים קיימים
	uploads     []string
	items       []FeedItem
	channels    string
	getText     bool // האם GetTextFile מורשה
	noUpdateExt bool // UpdateExtension נדחה (אין הרשאה)
}

func splitWhat(what string) (dir, name string) {
	i := strings.LastIndex(what, "/")
	dir, name = what[:i], what[i+1:]
	if dir == "ivr2:" {
		dir = "ivr2:/"
	}
	return dir, name
}

func (f *fakeYemotServer) addName(dir, name string) {
	for _, n := range f.dirs[dir] {
		if n == name {
			return
		}
	}
	f.dirs[dir] = append(f.dirs[dir], name)
}

func iniPathOf(dir string) string {
	if dir == "ivr2:/" {
		return "ivr2:/ext.ini"
	}
	return dir + "/ext.ini"
}

func (f *fakeYemotServer) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.ParseForm()
	ok := func(extra map[string]any) {
		m := map[string]any{"responseStatus": "OK"}
		for k, v := range extra {
			m[k] = v
		}
		json.NewEncoder(w).Encode(m)
	}
	switch {
	case r.URL.Path == "/api/messages":
		json.NewEncoder(w).Encode(map[string]any{"items": f.items})
	case r.URL.Path == "/api/channels":
		w.Write([]byte(f.channels))
	case strings.HasSuffix(r.URL.Path, "UploadTextFile"):
		what := r.PostForm.Get("what")
		f.uploads = append(f.uploads, what)
		dir, name := splitWhat(what)
		if _, exists := f.dirs[dir]; !exists {
			ok(nil) // כמו במערכת האמיתית: "OK" — אבל לא נוצר כלום
			return
		}
		f.files[what] = r.PostForm.Get("contents")
		f.addName(dir, name)
		ok(nil)
	case strings.HasSuffix(r.URL.Path, "UpdateExtension"):
		if f.noUpdateExt {
			w.Write([]byte(`{"responseStatus":"FORBIDDEN","message":"API_KEY_ACL_REJECT"}`))
			return
		}
		p := r.PostForm.Get("path")
		f.uploads = append(f.uploads, "UpdateExtension:"+p)
		if _, exists := f.dirs[p]; !exists {
			f.dirs[p] = nil
		}
		ini := f.files[iniPathOf(p)]
		for k, v := range r.PostForm {
			if k != "path" && k != "token" {
				ini, _ = setIniValues(ini, [][2]string{{k, v[0]}})
			}
		}
		f.files[iniPathOf(p)] = strings.Trim(ini, "\n")
		f.addName(p, "ext.ini")
		ok(nil)
	case strings.HasSuffix(r.URL.Path, "GetTextFile"):
		if !f.getText {
			w.Write([]byte(`{"responseStatus":"FORBIDDEN","message":"API_KEY_ACL_REJECT"}`))
			return
		}
		c, found := f.files[r.PostForm.Get("what")]
		if !found {
			w.Write([]byte(`{"responseStatus":"ERROR","message":"file does not exist"}`))
			return
		}
		ok(map[string]any{"contents": c})
	case strings.HasSuffix(r.URL.Path, "GetIVR2Dir"):
		p := r.PostForm.Get("path")
		names, found := f.dirs[p]
		if !found {
			w.Write([]byte(`{"responseStatus":"ERROR","message":"extension does not exist"}`))
			return
		}
		files, inis, dirs := []map[string]string{}, []map[string]string{}, []map[string]string{}
		for _, n := range names {
			switch {
			case strings.HasSuffix(n, ".ini"):
				inis = append(inis, map[string]string{"name": n})
			case strings.HasSuffix(n, ".txt"): // ימות לא מחזירים קבצי טקסט ברשימה
			default:
				files = append(files, map[string]string{"name": n})
			}
		}
		prefix := p + "/"
		if p == "ivr2:/" {
			prefix = "ivr2:/"
		}
		for d := range f.dirs {
			if rest, cut := strings.CutPrefix(d, prefix); cut && rest != "" && !strings.Contains(rest, "/") {
				dirs = append(dirs, map[string]string{"name": rest})
			}
		}
		extIni := map[string]string{}
		for _, l := range strings.Split(f.files[iniPathOf(p)], "\n") {
			if k, v, cut := strings.Cut(l, "="); cut {
				extIni[k] = v
			}
		}
		ok(map[string]any{"files": files, "ini": inis, "dirs": dirs, "extIni": extIni})
	case strings.HasSuffix(r.URL.Path, "FileAction"):
		if r.PostForm.Get("action") == "delete" {
			for i := 0; ; i++ {
				what := r.PostForm.Get(fmt.Sprintf("what%d", i))
				if what == "" {
					break
				}
				delete(f.files, what)
				dir, name := splitWhat(what)
				var keep []string
				for _, n := range f.dirs[dir] {
					if n != name {
						keep = append(keep, n)
					}
				}
				if _, exists := f.dirs[dir]; exists {
					f.dirs[dir] = keep
				}
			}
		}
		ok(nil)
	default:
		ok(nil)
	}
}

// has: האם הקובץ קיים בשלוחה במערכת המדומה.
func (f *fakeYemotServer) has(dir, name string) bool {
	for _, n := range f.dirs[dir] {
		if n == name {
			return true
		}
	}
	return false
}

func newTestCfg(srv *httptest.Server) config {
	yemotBase = srv.URL + "/ym/api/"
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	return config{feedURL: srv.URL + "/api/messages", feedKey: "k", ext: "1", maxMsgs: 10, perChan: 5,
		channelExts: true, loc: loc, y: &yemot{client: srv.Client(), apiKey: "KEY"}, client: srv.Client(), feedClient: srv.Client()}
}

func TestNewMenuStructure(t *testing.T) {
	now := time.Now().Unix()
	f := &fakeYemotServer{
		getText: true,
		files:   map[string]string{},
		dirs: map[string][]string{
			"ivr2:/":  {"M1000.tts", "ext.ini"},
			"ivr2:/1": {"ext.ini", "001.tts"},
			"ivr2:/2": {"ext.ini", "001.tts", "002.tts"}, // שלוחת כתב ישנה של הגשר → תפריט בחירה
			"ivr2:/3": {"ext.ini", "001.tts"},            // ישנה של הגשר → מפנה לתפריט
			"ivr2:/4": {"ext.ini", "001.wav"},            // של המשתמש → לא נוגעים
			"ivr2:/5": {"ext.ini", "001.tts"},
			"ivr2:/6": {"ext.ini", "001.tts"},
		},
		items: []FeedItem{
			{Channel: "elisha_yered", TS: now - 120, Text: "ראשונה מאלישע ביו״ש"},
			{Channel: "hakol", TS: now - 60, Text: "מהקול"},
		},
		channels: `{"channels":[{"name":"elisha_yered","title":"אלישע ירד"},{"name":"hakol","title":"הקול היהודי"}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.adminList = "606"
	cfg.publicList = "" // שלוחה 8 נבדקת בנפרד
	st := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	fl := f.files
	// שלוחה 1: כל העדכונים, החדשה במספר הגבוה.
	if !strings.HasPrefix(fl["ivr2:/1/002.tts"], "הקול היהודי, ") || !strings.Contains(fl["ivr2:/1/001.tts"], "ביהודה ושומרון") {
		t.Fatalf("ext1: %q | %q", fl["ivr2:/1/001.tts"], fl["ivr2:/1/002.tts"])
	}
	// שלוחה 2: תפריט בחירת כתב, והכתבים ב-2/1, 2/2.
	if fl["ivr2:/2/ext.ini"] != "type=menu\ndigits=1" {
		t.Fatalf("ext2 ini: %q", fl["ivr2:/2/ext.ini"])
	}
	if fl["ivr2:/2/M1000.tts"] != "בחירת כתב. לעדכוני אלישע ירד הקישו 1. לעדכוני הקול היהודי הקישו 2." {
		t.Fatalf("chooser: %q", fl["ivr2:/2/M1000.tts"])
	}
	if fl["ivr2:/2/1/ext.ini"] != "type=playfile" || !strings.HasPrefix(fl["ivr2:/2/1/001.tts"], "עדכוני אלישע ירד. ") {
		t.Fatalf("2/1: %q %q", fl["ivr2:/2/1/ext.ini"], fl["ivr2:/2/1/001.tts"])
	}
	if !strings.HasPrefix(fl["ivr2:/2/2/001.tts"], "עדכוני הקול היהודי. ") {
		t.Fatalf("2/2: %q", fl["ivr2:/2/2/001.tts"])
	}
	if f.has("ivr2:/2", "001.tts") || f.has("ivr2:/2", "002.tts") {
		t.Fatalf("stale TTS left in ext 2 (now a menu): %v", f.dirs["ivr2:/2"])
	}
	// 5 המשך, 6 הקלטה.
	if fl["ivr2:/5/ext.ini"] != "type=last_play" {
		t.Fatalf("ext5: %q", fl["ivr2:/5/ext.ini"])
	}
	if fl["ivr2:/6/ext.ini"] != "type=record\nsay_record_number=no\nhangup_insert_file=yes\nrecord_end_run_tzintuk=yes\nhangup_send_tzintuk=yes\nlist_tzintuk=606" {
		t.Fatalf("ext6: %q", fl["ivr2:/6/ext.ini"])
	}
	// 3 ישנה → מפנה לתפריט; 4 של המשתמש → לא נגעו.
	if fl["ivr2:/3/ext.ini"] != "type=go_to_folder\ngo_to_folder=/" {
		t.Fatalf("ext3: %q", fl["ivr2:/3/ext.ini"])
	}
	if _, touched := fl["ivr2:/4/ext.ini"]; touched {
		t.Fatal("touched user's ext 4")
	}
	// 8 — אין מספר רשימה → לא מוגדרת ולא בתפריט.
	if _, touched := fl["ivr2:/8/ext.ini"]; touched {
		t.Fatal("ext 8 set without list id")
	}
	want := "ברוכים הבאים לקו עדכוני ארץ ישראל. לכל העדכונים, הקישו 1. לבחירת כתב מסוים, הקישו 2. להמשך ההאזנה מהמקום שהפסקתם, הקישו 5. להשארת הודעה למנהל המערכת, הקישו 6."
	if fl["ivr2:/M1000.tts"] != want {
		t.Fatalf("welcome:\n%s\nwant:\n%s", fl["ivr2:/M1000.tts"], want)
	}

	// סבב שני בלי שינוי — שום העלאה.
	f.uploads = nil
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if len(f.uploads) != 0 {
		t.Fatalf("second round uploaded: %v", f.uploads)
	}

	// הפעלה חדשה אחרי שיש הקלטות בשלוחה 6 — עדיין "של הגשר" (קובץ הסימון), נשארת בתפריט.
	f.dirs["ivr2:/6"] = append(f.dirs["ivr2:/6"], "000.wav")
	st2 := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}}
	if err := syncOnce(&cfg, st2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.files["ivr2:/M1000.tts"], "הקישו 6") {
		t.Fatal("ext 6 dropped after recordings arrived")
	}
}

func TestListExt(t *testing.T) {
	f := &fakeYemotServer{files: map[string]string{}, dirs: map[string][]string{"ivr2:/": {"ext.ini"}, "ivr2:/8": {"ext.ini", "001.tts"}},
		items: []FeedItem{{Channel: "a", TS: time.Now().Unix(), Text: "שלום"}}, channels: `{"channels":[]}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.publicList, cfg.lineNumber = "800", "0772263731"
	st := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/8/ext.ini"] != "type=menu\ndigits=1" || f.files["ivr2:/8/1/ext.ini"] != "type=tzintuk\nlist_tzintuk=800" ||
		f.files["ivr2:/8/2/ext.ini"] != "type=telezchor\ntelezchor_end=hangup\ntelezchor_target_number=0772263731" {
		t.Fatalf("ext8: %q | %q | %q", f.files["ivr2:/8/ext.ini"], f.files["ivr2:/8/1/ext.ini"], f.files["ivr2:/8/2/ext.ini"])
	}
	if f.files["ivr2:/8/M1000.tts"] != "צינתוקים ותזכורות. להרשמה או הסרה מרשימת הצינתוקים, הקישו 1. לתזכורת קבועה לחייג לקו, בימים ובשעות שתבחרו, הקישו 2." {
		t.Fatalf("ext8 menu: %q", f.files["ivr2:/8/M1000.tts"])
	}
	if !strings.HasSuffix(f.files["ivr2:/M1000.tts"], "לצינתוקים ותזכורות, הקישו 8.") {
		t.Fatalf("welcome: %q", f.files["ivr2:/M1000.tts"])
	}
}

func TestCallbackExt(t *testing.T) {
	f := &fakeYemotServer{files: map[string]string{}, dirs: map[string][]string{"ivr2:/": {"ext.ini"}, "ivr2:/8": {}},
		items: []FeedItem{{Channel: "a", TS: time.Now().Unix(), Text: "שלום"}}, channels: `{"channels":[]}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.publicList, cfg.callback = "", true // בלי רשימת צינתוקים — רק שיחה חוזרת
	st := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/8/ext.ini"] != "type=menu\ndigits=1" {
		t.Fatalf("ext8 ini: %q", f.files["ivr2:/8/ext.ini"])
	}
	if f.files["ivr2:/8/3/ext.ini"] != "type=system_sharing\nsystem_sharing_custom_did=real_did\nsystem_sharing_to_myself=yes" {
		t.Fatalf("ext8/3: %q", f.files["ivr2:/8/3/ext.ini"])
	}
	if f.files["ivr2:/8/M1000.tts"] != "צינתוקים ותזכורות. לשיחה חוזרת מהמערכת, כדי לחסוך בדקות השיחה שלכם, הקישו 3." {
		t.Fatalf("ext8 menu: %q", f.files["ivr2:/8/M1000.tts"])
	}
	if !strings.HasSuffix(f.files["ivr2:/M1000.tts"], "לצינתוקים ותזכורות, הקישו 8.") {
		t.Fatalf("welcome: %q", f.files["ivr2:/M1000.tts"])
	}
}

func TestMediaFlashCut(t *testing.T) {
	flashEnabled = true
	defer func() { flashEnabled = false }()
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	now := time.Date(2026, 9, 22, 21, 0, 0, 0, loc)
	vid := `<article class="msg"><div class="photo"><div class="vidwrap"><video></video><span class="durbadge">1:05</span></div></div></article>`
	album := `<article class="msg"><div class="msgtext">תמונות מהשטח</div><div class="photo"><div class="grid"><img src="a"><img src="b"><img src="c"></div></div></article>`
	sticker := `<article class="msg"><div class="stickerbox"><img src="s"></div></article>`
	poll := `<article class="msg"><div class="pollbox"><div class="pollq">📊 האם לצאת?</div><div class="pollopt"><span>כן</span> <b>60%</b></div><div class="pollopt"><span>לא</span> <b>40%</b></div></div></article>`
	items := prepare([]FeedItem{
		{Channel: "a", TS: now.Add(-10 * time.Minute).Unix(), Text: "סרטון", HTML: vid},
		{Channel: "b", TS: now.Add(-20 * time.Minute).Unix(), Text: "תמונות מהשטח", HTML: album},
		{Channel: "c", TS: now.Add(-5 * time.Minute).Unix(), Text: "סטיקר", HTML: sticker},
		{Channel: "d", TS: now.Add(-3 * time.Minute).Unix(), Text: "האם לצאת?", HTML: poll},
		{Channel: "e", TS: now.Add(-50 * time.Minute).Unix(), Text: "פיגוע ירי בצומת, פצועים"},
		{Channel: "f", TS: now.Add(-15 * time.Minute).Unix(), Text: "אזעקה בשומרון"},
	})
	got := map[string]FeedItem{}
	for _, it := range items {
		got[it.Channel] = it
	}
	if _, ok := got["c"]; ok {
		t.Error("sticker should be skipped")
	}
	if got["a"].Text != "פורסם סרטון באורך דקה ו 5 שניות" {
		t.Errorf("video: %q", got["a"].Text)
	}
	if got["b"].Text != "תמונות מהשטח. מצורף להודעה: 3 תמונות" {
		t.Errorf("album: %q", got["b"].Text)
	}
	if !strings.HasPrefix(got["d"].Text, "פורסם סקר: האם לצאת? האפשרויות: כן 60 אחוז, לא 40 אחוז") {
		t.Errorf("poll: %q", got["d"].Text)
	}
	// e: מבזק ישן (50 דק') — לא פעיל; f: מבזק פעיל — ראשון גם שאינו החדש ביותר.
	parts := buildParts(items, nil, loc, now, 10, true, true)
	if !strings.HasPrefix(parts[0], "מבזק. f, ") {
		t.Errorf("flash not first: %q", parts[0])
	}
	for _, p := range parts[1:] {
		if strings.HasPrefix(p, "מבזק") {
			t.Errorf("expired flash still marked: %q", p)
		}
	}
	// חיתוך בסוף משפט
	long := strings.Repeat("זה משפט ארוך מאוד עם הרבה מילים. ", 60)
	p := spokenItem(FeedItem{Channel: "x", TS: now.Unix(), Text: long}, nil, loc, now, true, 1000)
	if len([]rune(p)) > 1000 || !strings.HasSuffix(p, "מילים. המשך ההודעה לא הוקרא.") {
		t.Errorf("cut: len=%d tail=%q", len([]rune(p)), string([]rune(p)[len([]rune(p))-40:]))
	}
	if !isAd("הספר החדש לרכישה באתר") || isAd("מבצע צבאי נרחב בשומרון") {
		t.Error("isAd")
	}
}

func TestFlashDisabledByDefault(t *testing.T) {
	now := time.Now()
	items := prepare([]FeedItem{{Channel: "a", TS: now.Unix(), Text: "פיגוע ירי בצומת"}})
	if items[0].Flash || strings.HasPrefix(buildParts(items, nil, time.UTC, now, 10, true, true)[0], "מבזק") {
		t.Fatal("flash should be off")
	}
}

func TestDictionary(t *testing.T) {
	cases := map[string]string{
		`סא"ל (מיל') פלוני`:               "סגן אלוף במילואים פלוני",
		`עלה ל-2 מיל' ש"ח`:                "עלה ל-2 מיליון שקלים",
		`רה"מ נפגש עם שהב"ט ויו"ר ועה"ח`:  "ראש הממשלה נפגש עם שר הביטחון ויושב ראש ועדת החוץ והביטחון",
		`בס"ד. בע"ה אחה"צ בבית הכנ' בק"א`: "בעזרת השם אחר הצהריים בבית הכנסת בקריית ארבע",
		`IDF: RPG found, watch LIVE`:      "צהל: אר פי גי found, watch שידור חי",
	}
	for in, want := range cases {
		if got := cleanForSpeech(in); got != want {
			t.Errorf("\nin:   %s\ngot:  %s\nwant: %s", in, got, want)
		}
	}
}

func TestKeyNotInErrors(t *testing.T) {
	_, err := getJSON(&http.Client{Timeout: time.Second}, "http://127.0.0.1:1/api/messages", "SECRET-KEY-123")
	if err == nil || strings.Contains(err.Error(), "SECRET-KEY-123") {
		t.Fatalf("key leaked in error: %v", err)
	}
}

func TestAdminRegisterExt(t *testing.T) {
	f := &fakeYemotServer{files: map[string]string{}, dirs: map[string][]string{"ivr2:/": {"ext.ini"}, "ivr2:/7": {"ext.ini", "001.tts"}},
		items: []FeedItem{{Channel: "a", TS: time.Now().Unix(), Text: "שלום"}}, channels: `{"channels":[]}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.adminList, cfg.adminRegister = "606", true
	st := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/7/ext.ini"] != "type=tzintuk\nlist_tzintuk=606" {
		t.Fatalf("ext7: %q", f.files["ivr2:/7/ext.ini"])
	}
	if strings.Contains(f.files["ivr2:/M1000.tts"], "הקישו 7") {
		t.Fatal("ext 7 must stay hidden from the menu")
	}
	cfg.adminRegister = false
	st2 := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}}
	if err := syncOnce(&cfg, st2); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/7/ext.ini"] != "type=go_to_folder\ngo_to_folder=/" {
		t.Fatalf("ext7 after: %q", f.files["ivr2:/7/ext.ini"])
	}
}

// realAccount: מבנה כמו בקו האמיתי — שלוחות 1-9 קיימות, בלי שלוחות-בת.
func realAccount() map[string][]string {
	d := map[string][]string{"ivr2:/": {"M1000.tts", "ext.ini"}}
	for i := 1; i <= 9; i++ {
		d[fmt.Sprintf("ivr2:/%d", i)] = []string{"ext.ini", "001.tts", "002.tts"}
	}
	return d
}

// TestCreatesMissingSubExtensions: הבאג מהקו האמיתי — שלוחות-בת (2/2.., 8/1..)
// לא נוצרו כי UploadTextFile מחזיר OK בלי ליצור, והתפריט הכריז עליהן בכל זאת.
func TestCreatesMissingSubExtensions(t *testing.T) {
	now := time.Now().Unix()
	f := &fakeYemotServer{getText: true, files: map[string]string{}, dirs: realAccount(),
		items: []FeedItem{
			{Channel: "a", TS: now - 60, Text: "ראשונה"},
			{Channel: "b", TS: now - 50, Text: "שנייה"},
			{Channel: "c", TS: now - 40, Text: "שלישית"},
		},
		channels: `{"channels":[{"name":"a","title":"אלישע ירד"},{"name":"b","title":"הקול היהודי"},{"name":"c","title":"עדכוני השומרון"}]}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.publicList, cfg.lineNumber, cfg.callback = "800", "0772263731", true
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"2/1", "2/2", "2/3", "8/1", "8/2", "8/3"} {
		if _, exists := f.dirs["ivr2:/"+ext]; !exists {
			t.Errorf("extension %s was not created", ext)
		}
	}
	if f.files["ivr2:/2/3/ext.ini"] != "type=playfile" || !strings.HasPrefix(f.files["ivr2:/2/3/001.tts"], "עדכוני השומרון. ") {
		t.Errorf("2/3: %q %q", f.files["ivr2:/2/3/ext.ini"], f.files["ivr2:/2/3/001.tts"])
	}
	if got := f.files["ivr2:/2/M1000.tts"]; got != "בחירת כתב. לעדכוני אלישע ירד הקישו 1. לעדכוני הקול היהודי הקישו 2. לעדכוני השומרון הקישו 3." {
		t.Errorf("chooser: %q", got)
	}
	if f.files["ivr2:/8/1/ext.ini"] != "type=tzintuk\nlist_tzintuk=800" {
		t.Errorf("8/1: %q", f.files["ivr2:/8/1/ext.ini"])
	}
	if f.has("ivr2:/8", "001.tts") || f.has("ivr2:/5", "001.tts") || f.has("ivr2:/6", "002.tts") {
		t.Error("stale TTS from the old structure left behind")
	}
	for _, want := range []string{"הקישו 2", "הקישו 6", "הקישו 8"} {
		if !strings.Contains(f.files["ivr2:/M1000.tts"], want) {
			t.Errorf("welcome is missing %q: %s", want, f.files["ivr2:/M1000.tts"])
		}
	}
}

// TestNoCreatePermission: בלי הרשאה ל-UpdateExtension — לא מכריזים על אפשרויות
// שלא קיימות (אחרת המתקשר מקיש ושומע שוב את התפריט). כשההרשאה ניתנת — מתקן לבד.
func TestNoCreatePermission(t *testing.T) {
	defer func(d time.Duration) { setupRetry = d }(setupRetry)
	f := &fakeYemotServer{getText: true, noUpdateExt: true, files: map[string]string{}, dirs: realAccount(),
		items:    []FeedItem{{Channel: "a", TS: time.Now().Unix(), Text: "שלום"}},
		channels: `{"channels":[{"name":"a","title":"אלישע ירד"}]}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.publicList, cfg.callback = "800", true
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	welcome := f.files["ivr2:/M1000.tts"]
	if strings.Contains(welcome, "הקישו 2") || strings.Contains(welcome, "הקישו 8") {
		t.Fatalf("announced options that lead nowhere: %s", welcome)
	}
	if f.files["ivr2:/8/M1000.tts"] != "שלוחה זו אינה פעילה כרגע." {
		t.Fatalf("ext 8 menu with dead options: %q", f.files["ivr2:/8/M1000.tts"])
	}
	// בעל הקו הוסיף את ההרשאה — הניסיון החוזר יוצר ומכריז.
	f.noUpdateExt, setupRetry = false, 0
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	welcome = f.files["ivr2:/M1000.tts"]
	if !strings.Contains(welcome, "הקישו 2") || !strings.Contains(welcome, "הקישו 8") {
		t.Fatalf("not announced after permission was granted: %s", welcome)
	}
	if _, exists := f.dirs["ivr2:/2/1"]; !exists {
		t.Fatal("2/1 not created after permission was granted")
	}
}

// TestSubExtWithoutIni: שלוחת כתב שקיימת בלי ext.ini משלה יורשת type=menu
// משלוחה 2 (ואז הקשה עליה משמיעה שוב את התפריט) — צריך לכתוב לה type=playfile.
func TestSubExtWithoutIni(t *testing.T) {
	d := realAccount()
	d["ivr2:/2/1"] = []string{"001.tts", "playfile_log.ymgr"} // יומן של ימות — לא "קובץ של המשתמש"
	f := &fakeYemotServer{getText: true, files: map[string]string{}, dirs: d,
		items:    []FeedItem{{Channel: "a", TS: time.Now().Unix(), Text: "שלום"}},
		channels: `{"channels":[{"name":"a","title":"אלישע ירד"}]}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/2/1/ext.ini"] != "type=playfile" {
		t.Fatalf("2/1 ext.ini: %q", f.files["ivr2:/2/1/ext.ini"])
	}
}
