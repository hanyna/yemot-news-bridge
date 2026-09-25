package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strconv"
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
	getText     bool         // האם GetTextFile מורשה
	noUpdateExt bool         // UpdateExtension נדחה (אין הרשאה)
	noUpload    bool         // UploadFile נדחה (אין הרשאה)
	media404    map[int]bool // /api/media: הודעות שהשרת לא נותן ("Media is too big")
	mediaHits   int
	failWhat    map[string]bool // UploadTextFile לנתיבים האלה נכשל
	failRead    map[string]bool // GetTextFile לנתיבים האלה נכשל (תקלה זמנית)
	failText    string          // UploadTextFile של תוכן שמכיל את זה — נכשל
	rss         string          // /rss/pod — הזנת פודקאסט
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
	case r.URL.Path == "/api/media":
		f.mediaHits++
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		if f.media404[id] || r.URL.Query().Get("k") == "" {
			http.Error(w, "unavailable", http.StatusNotFound)
			return
		}
		w.Write([]byte("VIDEO-" + r.URL.Query().Get("channel") + "-" + strconv.Itoa(id)))
	case strings.HasPrefix(r.URL.Path, "/voice/"):
		f.mediaHits++
		w.Write([]byte("VOICE-" + strings.TrimPrefix(r.URL.Path, "/voice/")))
	case r.URL.Path == "/rss/pod":
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		w.Write([]byte(f.rss))
	case strings.HasPrefix(r.URL.Path, "/ep/"):
		f.mediaHits++
		w.Write([]byte("EP-" + strings.TrimPrefix(r.URL.Path, "/ep/")))
	case strings.HasSuffix(r.URL.Path, "UploadFile"):
		if f.noUpload {
			w.Write([]byte(`{"responseStatus":"FORBIDDEN","message":"API_KEY_ACL_REJECT"}`))
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		what := r.FormValue("path")
		file, _, err := r.FormFile("file")
		if err != nil {
			w.Write([]byte(`{"responseStatus":"ERROR","message":"no file"}`))
			return
		}
		data, _ := io.ReadAll(file)
		f.uploads = append(f.uploads, "UploadFile:"+what)
		dir, name := splitWhat(what)
		if _, exists := f.dirs[dir]; !exists {
			ok(nil)
			return
		}
		f.files[what] = "AUDIO:" + string(data) + ";convert=" + r.FormValue("convertAudio")
		f.addName(dir, name)
		// כמו ימות המשיח: לכל קובץ קול נכתב לידו קובץ פרטים באותו שם, בסיומת txt
		if i := strings.LastIndex(name, "."); i > 0 {
			f.files[dir+"/"+name[:i]+".txt"] = "API-DID-0770000000-Date-2026\r\ntitle=" + name + "\r\n"
			f.addName(dir, name[:i]+".txt")
		}
		ok(map[string]any{"path": what})
	case strings.HasSuffix(r.URL.Path, "UploadTextFile"):
		what := r.PostForm.Get("what")
		if f.failWhat[what] || (f.failText != "" && strings.Contains(r.PostForm.Get("contents"), f.failText)) {
			w.Write([]byte(`{"responseStatus":"ERROR","message":"upload failed"}`))
			return
		}
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
		if f.failRead[r.PostForm.Get("what")] {
			w.Write([]byte(`{"responseStatus":"ERROR","message":"server busy"}`))
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
	return config{feedURL: srv.URL + "/api/messages", feedKey: "k", ext: "1",
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
			{ID: 11, Channel: "elisha_yered", TS: now - 120, Text: "ראשונה מאלישע ביו״ש"},
			{ID: 22, Channel: "hakol", TS: now - 60, Text: "מהקול"},
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
	// שלוחה 1: ארכיון קבוע — הישנה ב-10001, החדשה ב-10003 (מספר גבוה = נשמעת ראשונה).
	if !strings.HasPrefix(fl["ivr2:/1/10003.tts"], "הקול היהודי, ") || !strings.Contains(fl["ivr2:/1/10001.tts"], "ביהודה ושומרון") {
		t.Fatalf("ext1: %q | %q", fl["ivr2:/1/10001.tts"], fl["ivr2:/1/10003.tts"])
	}
	if f.has("ivr2:/1", "001.tts") {
		t.Fatal("positional file from the old structure left in ext 1")
	}
	if !strings.Contains(fl["ivr2:/1/ext.ini"], "file_amount_digits=5") || !strings.Contains(fl["ivr2:/1/archive.txt"], "next=10004") {
		t.Fatalf("ext1 ini/index: %q | %q", fl["ivr2:/1/ext.ini"], fl["ivr2:/1/archive.txt"])
	}
	// שלוחה 2: תפריט בחירת כתב, והכתבים ב-2/1, 2/2.
	if fl["ivr2:/2/ext.ini"] != "type=menu\ndigits=1" {
		t.Fatalf("ext2 ini: %q", fl["ivr2:/2/ext.ini"])
	}
	if fl["ivr2:/2/M1000.tts"] != "בחירת כתב. לעדכוני אלישע ירד הקישו 1. לעדכוני הקול היהודי הקישו 2." {
		t.Fatalf("chooser: %q", fl["ivr2:/2/M1000.tts"])
	}
	if fl["ivr2:/2/1/ext.ini"] != "type=playfile\nfile_amount_digits=5" || fl["ivr2:/2/1/99999.tts"] != "עדכוני אלישע ירד." ||
		!strings.Contains(fl["ivr2:/2/1/10001.tts"], "ביהודה ושומרון") || strings.HasPrefix(fl["ivr2:/2/1/10001.tts"], "אלישע ירד") {
		t.Fatalf("2/1: %q %q %q", fl["ivr2:/2/1/ext.ini"], fl["ivr2:/2/1/99999.tts"], fl["ivr2:/2/1/10001.tts"])
	}
	if fl["ivr2:/2/2/99999.tts"] != "עדכוני הקול היהודי." || !strings.HasSuffix(fl["ivr2:/2/2/10001.tts"], "מהקול") {
		t.Fatalf("2/2: %q %q", fl["ivr2:/2/2/99999.tts"], fl["ivr2:/2/2/10001.tts"])
	}
	if !strings.Contains(fl["ivr2:/2/1/archive.txt"], "channel=elisha_yered") {
		t.Fatalf("2/1 index: %q", fl["ivr2:/2/1/archive.txt"])
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
	f := &fakeYemotServer{getText: true, files: map[string]string{}, dirs: map[string][]string{"ivr2:/": {"ext.ini"}, "ivr2:/8": {"ext.ini", "001.tts"}},
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
	f := &fakeYemotServer{getText: true, files: map[string]string{}, dirs: map[string][]string{"ivr2:/": {"ext.ini"}, "ivr2:/8": {}},
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
	f := &fakeYemotServer{getText: true, files: map[string]string{}, dirs: map[string][]string{"ivr2:/": {"ext.ini"}, "ivr2:/7": {"ext.ini", "001.tts"}},
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
	if f.files["ivr2:/2/3/ext.ini"] != "type=playfile\nfile_amount_digits=5" || f.files["ivr2:/2/3/99999.tts"] != "עדכוני השומרון." ||
		!strings.HasSuffix(f.files["ivr2:/2/3/10001.tts"], "שלישית") {
		t.Errorf("2/3: %q %q %q", f.files["ivr2:/2/3/ext.ini"], f.files["ivr2:/2/3/99999.tts"], f.files["ivr2:/2/3/10001.tts"])
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
	if f.files["ivr2:/2/1/ext.ini"] != "type=playfile\nfile_amount_digits=5" {
		t.Fatalf("2/1 ext.ini: %q", f.files["ivr2:/2/1/ext.ini"])
	}
}

// ---------- ארכיון קבוע ----------

func archiveServer(items []FeedItem, channels string) *fakeYemotServer {
	return &fakeYemotServer{getText: true, files: map[string]string{}, dirs: realAccount(), items: items, channels: channels}
}

// TestArchiveKeepsOldMessages: הודעה שיוצאת מהשרת נשארת בקו, והודעה חדשה
// מקבלת את המספר הבא — בלי להעלות מחדש את הקודמות (גם אחרי הפעלה מחדש).
func TestArchiveKeepsOldMessages(t *testing.T) {
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{
		{ID: 1, Channel: "a", TS: now - 300, Text: "הודעה ראשונה ארוכה מספיק כדי שלא תיחשב כפולה של השנייה בשום מצב"},
		{ID: 2, Channel: "a", TS: now - 200, Text: "הודעה שנייה"},
	}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10001.tts"], "בשום מצב") || !strings.HasSuffix(f.files["ivr2:/1/10003.tts"], "הודעה שנייה") {
		t.Fatalf("import: %q | %q", f.files["ivr2:/1/10001.tts"], f.files["ivr2:/1/10003.tts"])
	}
	if f.has("ivr2:/1", "001.tts") || f.has("ivr2:/2/1", "001.tts") {
		t.Fatal("old positional files not removed")
	}
	// הפעלה חדשה: הראשונה כבר לא בשרת, ויש חדשה.
	f.items = []FeedItem{f.items[1], {ID: 3, Channel: "a", TS: now - 10, Text: "הודעה שלישית"}}
	f.uploads = nil
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10001.tts"], "בשום מצב") {
		t.Fatal("old message removed from the line")
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10005.tts"], "הודעה שלישית") || !strings.HasSuffix(f.files["ivr2:/2/1/10005.tts"], "הודעה שלישית") {
		t.Fatalf("new message: %q | %q", f.files["ivr2:/1/10005.tts"], f.files["ivr2:/2/1/10005.tts"])
	}
	for _, u := range f.uploads {
		if strings.HasSuffix(u, "10001.tts") || strings.HasSuffix(u, "10003.tts") {
			t.Fatalf("re-uploaded an archived message: %v", f.uploads)
		}
	}
}

// TestArchiveRerenderYesterday: אחרי חצות "בשעה X" הופך ל"אתמול בשעה X" — גם
// להודעה שכבר לא בשרת (הגוף נלקח מהקובץ שבשלוחה).
func TestArchiveRerenderYesterday(t *testing.T) {
	defer func() { nowFunc = time.Now }()
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	day := time.Date(2026, 9, 24, 20, 0, 0, 0, loc)
	nowFunc = func() time.Time { return day }
	f := archiveServer([]FeedItem{
		{ID: 1, Channel: "a", TS: day.Add(-2 * time.Hour).Unix(), Text: "נשארת בשרת"},
		{ID: 2, Channel: "a", TS: day.Add(-time.Hour).Unix(), Text: "יוצאת מהשרת"},
	}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/1/10003.tts"] != "אלישע ירד, בשעה 7 בערב. יוצאת מהשרת" {
		t.Fatalf("today: %q", f.files["ivr2:/1/10003.tts"])
	}
	f.items = f.items[:1]
	nowFunc = func() time.Time { return day.Add(6 * time.Hour) } // 2 בלילה, למחרת
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/1/10001.tts"] != "אלישע ירד, אתמול בשעה 6 בערב. נשארת בשרת" ||
		f.files["ivr2:/1/10003.tts"] != "אלישע ירד, אתמול בשעה 7 בערב. יוצאת מהשרת" ||
		f.files["ivr2:/2/1/10003.tts"] != "אתמול בשעה 7 בערב. יוצאת מהשרת" {
		t.Fatalf("yesterday: %q | %q | %q", f.files["ivr2:/1/10001.tts"], f.files["ivr2:/1/10003.tts"], f.files["ivr2:/2/1/10003.tts"])
	}
	// שבוע אחר כך — תאריך (ומכאן כבר לא משתנה).
	nowFunc = func() time.Time { return day.Add(8 * 24 * time.Hour) }
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/1/10003.tts"] != "אלישע ירד, ב 24 בספטמבר בשעה 7 בערב. יוצאת מהשרת" {
		t.Fatalf("date: %q", f.files["ivr2:/1/10003.tts"])
	}
}

// TestArchiveDedupe: הודעה שהועברה בערוץ אחר לא נכנסת שוב לשלוחה 1 (גם אם
// ההעברה הגיעה קודם), אבל כן לשלוחה של הערוץ שהעביר.
func TestArchiveDedupe(t *testing.T) {
	now := time.Now().Unix()
	long := "הודעה חשובה מאוד על אירוע ביטחוני בצומת הגדול ליד היישוב"
	f := archiveServer([]FeedItem{{ID: 9, Channel: "b", TS: now - 100, Text: long + " הועבר"}},
		`{"channels":[{"name":"a","title":"אלישע ירד"},{"name":"b","title":"הקול היהודי"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	// המקור (מוקדם יותר) מגיע רק עכשיו.
	f.items = append(f.items, FeedItem{ID: 1, Channel: "a", TS: now - 200, Text: long})
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.has("ivr2:/1", "10003.tts") {
		t.Fatalf("duplicate archived in ext 1: %q", f.files["ivr2:/1/10003.tts"])
	}
	if !strings.HasSuffix(f.files["ivr2:/2/1/10001.tts"], long) || !strings.HasSuffix(f.files["ivr2:/2/2/10001.tts"], "הועבר") {
		t.Fatalf("reporter exts: %q | %q", f.files["ivr2:/2/1/10001.tts"], f.files["ivr2:/2/2/10001.tts"])
	}
	// ההעברה יצאה מהשרת (והמקור נשאר) — המקור עדיין לא נכנס שוב לשלוחה 1.
	f.items = f.items[1:]
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.has("ivr2:/1", "10003.tts") {
		t.Fatalf("duplicate archived after the forward left the server: %q", f.files["ivr2:/1/10003.tts"])
	}
}

// TestArchiveIndexLost: אם archive.txt נמחק — לא מייבאים שוב (כפילויות),
// ממשיכים אחרי הקובץ האחרון.
func TestArchiveIndexLost(t *testing.T) {
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: now - 100, Text: "ישנה"}}, `{"channels":[]}`)
	f.dirs["ivr2:/1"] = []string{"ext.ini", "10000.wav", "10001.tts", "10003.tts"}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if f.has("ivr2:/1", "10005.tts") {
		t.Fatal("re-imported messages already on the line")
	}
	f.items = append(f.items, FeedItem{ID: 2, Channel: "a", TS: now + 5, Text: "חדשה"})
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10005.tts"], "חדשה") {
		t.Fatalf("new message after recovery: %v", f.dirs["ivr2:/1"])
	}
}

// TestReporterMappingStable: שינוי בסדר הערוצים לא מעביר ארכיון של כתב לשלוחה אחרת.
func TestReporterMappingStable(t *testing.T) {
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: now - 100, Text: "של א"}, {ID: 2, Channel: "b", TS: now - 90, Text: "של ב"}},
		`{"channels":[{"name":"a","title":"אלישע ירד"},{"name":"b","title":"הקול היהודי"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	f.channels = `{"channels":[{"name":"c","title":"ערוץ חדש"},{"name":"b","title":"הקול היהודי"},{"name":"a","title":"אלישע ירד"}]}`
	f.items = append(f.items, FeedItem{ID: 3, Channel: "a", TS: now - 5, Text: "עוד של א"})
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/2/1/10003.tts"], "עוד של א") || f.files["ivr2:/2/3/99999.tts"] != "עדכוני ערוץ חדש." {
		t.Fatalf("mapping moved: 2/1=%q 2/3=%q", f.files["ivr2:/2/1/10003.tts"], f.files["ivr2:/2/3/99999.tts"])
	}
	want := "בחירת כתב. לעדכוני אלישע ירד הקישו 1. לעדכוני הקול היהודי הקישו 2. לעדכוני ערוץ חדש הקישו 3."
	if f.files["ivr2:/2/M1000.tts"] != want {
		t.Fatalf("chooser: %q", f.files["ivr2:/2/M1000.tts"])
	}
}

// ---------- קול של סרטונים והודעות קוליות ----------

func withFakeTranscode(t *testing.T) {
	oldT, oldF := transcode, haveFFmpeg
	transcode = func(in, out string, maxSecs int, podcast bool) error {
		b, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		return os.WriteFile(out, append([]byte("MP3:"), b...), 0o644)
	}
	haveFFmpeg = func() bool { return true }
	t.Cleanup(func() { transcode, haveFFmpeg = oldT, oldF })
}

func TestAudio(t *testing.T) {
	withFakeTranscode(t)
	now := time.Now().Unix()
	f := archiveServer(nil, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	video := `<article class="msg"><div class="msgtext">צפו בתיעוד</div><div class="photo"><div class="vidwrap"><video src="/api/media?channel=a&amp;id=2" controls preload="none" playsinline></video><span class="durbadge durtop">1:05</span></div></div></article>`
	voice := `<article class="msg"><div class="voicebox"><span class="voiceico">🎤</span><audio controls preload="none" src="` + srv.URL + `/voice/3.ogg"></audio><span class="voicedur">0:39</span></div></article>`
	gif := `<article class="msg"><div class="photo"><div class="vidwrap"><video class="gifvid" src="x" muted loop playsinline preload="none"></video></div></div></article>`
	big := `<article class="msg"><div class="msgtext">סרטון שטלגרם לא נותנים</div><div class="photo"><div class="vidwrap"><video src="/api/media?channel=a&amp;id=5" controls></video><span class="durbadge durtop">3:00</span></div></div></article>`
	long := `<article class="msg"><div class="msgtext">נאום ארוך</div><div class="photo"><div class="vidwrap"><video src="/api/media?channel=a&amp;id=6" controls></video><span class="durbadge durtop">1:30:00</span></div></div></article>`
	f.items = []FeedItem{
		{ID: 1, Channel: "a", TS: now - 300, Text: "רק טקסט"},
		{ID: 2, Channel: "a", TS: now - 200, Text: "צפו בתיעוד", HTML: video},
		{ID: 3, Channel: "a", TS: now - 100, Text: "", HTML: voice},
		{ID: 4, Channel: "a", TS: now - 50, Text: "", HTML: gif},
		{ID: 5, Channel: "a", TS: now - 40, Text: "סרטון שטלגרם לא נותנים", HTML: big},
		{ID: 6, Channel: "a", TS: now - 30, Text: "נאום ארוך", HTML: long},
	}
	f.media404 = map[int]bool{5: true}
	defer func(b []time.Duration) { retryBackoff = b }(retryBackoff)
	retryBackoff = []time.Duration{0, 0, 0} // הסרטון שלא זמין — כל הניסיונות מיד
	cfg := newTestCfg(srv)
	cfg.audio, cfg.audioMax = true, 20*60
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	// ההקראה של הסרטון, והקול שלו מיד אחריה (מספר נמוך באחד = נשמע אחריה).
	if !strings.HasSuffix(f.files["ivr2:/1/10003.tts"], "צפו בתיעוד. מצורף להודעה: סרטון באורך דקה ו 5 שניות") {
		t.Fatalf("video intro: %q", f.files["ivr2:/1/10003.tts"])
	}
	for _, p := range []string{"ivr2:/1/10002.wav", "ivr2:/2/1/10002.wav"} {
		if f.files[p] != "AUDIO:MP3:VIDEO-a-2;convert=1" {
			t.Fatalf("%s: %q", p, f.files[p])
		}
	}
	if f.files["ivr2:/1/10004.wav"] != "AUDIO:MP3:VOICE-3.ogg;convert=1" {
		t.Fatalf("voice: %q", f.files["ivr2:/1/10004.wav"])
	}
	if f.has("ivr2:/1", "10006.wav") || f.has("ivr2:/1", "10008.wav") || f.has("ivr2:/1", "10010.wav") {
		t.Fatalf("audio for gif / unavailable / too long: %v", f.dirs["ivr2:/1"])
	}
	if f.mediaHits != 6 { // סרטון, קולית, והסרטון שלא זמין (4 ניסיונות). הארוך — לא מורידים בכלל.
		t.Fatalf("downloads: %d", f.mediaHits)
	}
	idx := f.files["ivr2:/1/archive.txt"]
	// שורה באינדקס: e <מפתח> <מספר> <זמן> <ניסוח> <קול: 2=עלה, 3=לא זמין> <סוג>
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^e a/2 10002 \d+ 0 2 v$`),
		regexp.MustCompile(`(?m)^e a/3 10004 \d+ 0 2 o$`),
		regexp.MustCompile(`(?m)^e a/4 10006 \d+ 0 0 -$`), // GIF — אין קול
		regexp.MustCompile(`(?m)^e a/5 10008 \d+ 0 3 v$`),
		regexp.MustCompile(`(?m)^e a/6 10010 \d+ 0 3 v$`),
	} {
		if !want.MatchString(idx) {
			t.Fatalf("index missing %v:\n%s", want, idx)
		}
	}
}

// TestAudioNoPermission: בלי הרשאה ל-UploadFile — ההקראה עולה, הקול ממתין
// (לא מוותרים עליו), ונרשמת הודעה ברורה.
func TestAudioNoPermission(t *testing.T) {
	withFakeTranscode(t)
	now := time.Now().Unix()
	video := `<div class="msgtext">תיעוד</div><div class="photo"><div class="vidwrap"><video src="/api/media?channel=a&amp;id=2" controls></video><span class="durbadge durtop">0:20</span></div></div>`
	f := archiveServer([]FeedItem{{ID: 2, Channel: "a", TS: now - 20, Text: "תיעוד", HTML: video}}, `{"channels":[]}`)
	f.noUpload = true
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.audio = true
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10001.tts"], "תיעוד. מצורף להודעה: סרטון באורך 20 שניות") {
		t.Fatalf("intro: %q", f.files["ivr2:/1/10001.tts"])
	}
	if !st.aw.warnedFor("UploadFile") || st.aw.pending() != 1 || !strings.Contains(f.files["ivr2:/1/archive.txt"], "e a/2 10000 ") {
		t.Fatalf("warned=%v queue=%d index=%q", st.aw.warnedFor("UploadFile"), st.aw.pending(), f.files["ivr2:/1/archive.txt"])
	}
	if f.mediaHits != 0 {
		t.Fatalf("downloaded without upload permission: %d", f.mediaHits)
	}
	// ההרשאה נוספה; הפעלה חדשה מעלה את הקול שממתין (הסרטון כבר לא בשרת — השרת מוצא אותו לפי מזהה).
	f.noUpload, f.items = false, nil
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/1/10000.wav"] != "AUDIO:MP3:VIDEO-a-2;convert=1" {
		t.Fatalf("pending audio after restart: %q", f.files["ivr2:/1/10000.wav"])
	}
}

// TestArchiveNeedsReadPermission: בלי הרשאה ל-GetTextFile הארכיון לא יכול לדעת
// מה כבר בשלוחה — הסבב נכשל עם הסבר, אבל התפריטים ממשיכים להתעדכן.
func TestArchiveNeedsReadPermission(t *testing.T) {
	f := &fakeYemotServer{files: map[string]string{}, dirs: realAccount(),
		items: []FeedItem{{ID: 1, Channel: "a", TS: time.Now().Unix(), Text: "שלום"}}, channels: `{"channels":[]}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	err := syncOnce(&cfg, &state{})
	if err == nil || !strings.Contains(err.Error(), "/api/GetTextFile") {
		t.Fatalf("want a GetTextFile permission error, got %v", err)
	}
	if !strings.Contains(f.files["ivr2:/M1000.tts"], "ברוכים הבאים") {
		t.Fatal("welcome menu not maintained when the archive failed")
	}
}

// TestAudioBackground: ה-worker ברקע, כמו בהפעלה האמיתית — ההקראה עולה בסבב,
// הקול מצטרף ברקע (הורדה אחת לשתי השלוחות), ונרשם באינדקס בסבב שאחריו.
func TestAudioBackground(t *testing.T) {
	withFakeTranscode(t)
	now := time.Now().Unix()
	video := `<div class="msgtext">תיעוד</div><div class="photo"><div class="vidwrap"><video src="/api/media?channel=a&amp;id=2" controls></video><span class="durbadge durtop">0:20</span></div></div>`
	f := archiveServer([]FeedItem{{ID: 2, Channel: "a", TS: now - 20, Text: "תיעוד", HTML: video}}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.audio = true
	st := &state{}
	st.ensureMaps()
	st.aw.start(&cfg)
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10001.tts"], "סרטון באורך 20 שניות") {
		t.Fatalf("intro: %q", f.files["ivr2:/1/10001.tts"])
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st.aw.mu.Lock()
		n := len(st.aw.results)
		st.aw.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background worker did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range []string{"ivr2:/1/10000.wav", "ivr2:/2/1/10000.wav"} {
		if f.files[p] != "AUDIO:MP3:VIDEO-a-2;convert=1" {
			t.Fatalf("%s: %q", p, f.files[p])
		}
	}
	if f.mediaHits != 1 {
		t.Fatalf("one download for both extensions, got %d", f.mediaHits)
	}
	for _, idx := range []string{f.files["ivr2:/1/archive.txt"], f.files["ivr2:/2/1/archive.txt"]} {
		if !regexp.MustCompile(`(?m)^e a/2 10000 \d+ 0 2 v$`).MatchString(idx) {
			t.Fatalf("audio not recorded as done:\n%s", idx)
		}
	}
}

// TestArchiveQuietChannel: ערוץ שקט — ההודעה האחרונה שלו נשארת בשרת ימים
// רבים. היא לא נכנסת שוב לשום שלוחה, גם אחרי שהיא יוצאת מהאינדקס של שלוחה 1.
func TestArchiveQuietChannel(t *testing.T) {
	defer func() { nowFunc = time.Now }()
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	day := time.Date(2026, 9, 1, 12, 0, 0, 0, loc)
	nowFunc = func() time.Time { return day }
	f := archiveServer([]FeedItem{
		{ID: 1, Channel: "a", TS: day.Add(-time.Hour).Unix(), Text: "ההודעה האחרונה של הערוץ השקט"},
		{ID: 1, Channel: "b", TS: day.Add(-30 * time.Minute).Unix(), Text: "מהערוץ הפעיל"},
	}, `{"channels":[{"name":"a","title":"אלישע ירד"},{"name":"b","title":"הקול היהודי"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	// 10 ימים: הערוץ הפעיל מפרסם כל יום, השקט לא. כל יום — הפעלה חדשה.
	for d := 1; d <= 10; d++ {
		now := day.Add(time.Duration(d) * 24 * time.Hour)
		nowFunc = func() time.Time { return now }
		f.items = append(f.items, FeedItem{ID: 1 + d, Channel: "b", TS: now.Add(-time.Minute).Unix(), Text: fmt.Sprintf("עדכון יומי %d", d)})
		if err := syncOnce(&cfg, &state{}); err != nil {
			t.Fatal(err)
		}
	}
	quiet := 0
	for p, c := range f.files {
		if strings.HasSuffix(p, ".tts") && strings.Contains(c, "הערוץ השקט") {
			quiet++
		}
	}
	if quiet != 2 { // פעם אחת בשלוחה 1, ופעם אחת בשלוחת הכתב
		t.Fatalf("quiet channel's post archived %d times (want 2)", quiet)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10023.tts"], "עדכון יומי 10") || f.has("ivr2:/1", "10025.tts") {
		t.Fatalf("ext 1 after 10 days: %v", f.dirs["ivr2:/1"])
	}
	// שלוחה 1: ההודעה יצאה מהאינדקס (ישנה, וחדשות אחריה). שלוחת הכתב השקט: נשארת.
	if strings.Contains(f.files["ivr2:/1/archive.txt"], "e a/1 ") || !strings.Contains(f.files["ivr2:/2/1/archive.txt"], "e a/1 ") {
		t.Fatalf("index pruning:\n%s\n---\n%s", f.files["ivr2:/1/archive.txt"], f.files["ivr2:/2/1/archive.txt"])
	}
}

// TestArchivePrune: הודעה יוצאת מהאינדקס רק כשהיא ישנה מ-9 ימים וגם מכוסה בכלל
// "ישנה ביותר מ-12 שעות מהחדשה שבארכיון" — אחרת (ערוץ שקט) היא הייתה נכנסת שוב.
// הודעה שהקול שלה ממתין — נשארת.
func TestArchivePrune(t *testing.T) {
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour).Unix()
	post := FeedItem{ID: 1, Channel: "a", TS: old}
	mk := func(last int64, audio int) *archive {
		return &archive{last: last, entries: map[string]*archEntry{"a/1": {key: "a/1", base: 10000, ts: old, audio: audio}}}
	}
	quiet := mk(old, audioNone)
	quiet.encode(now)
	if _, kept := quiet.entries["a/1"]; !kept || !quiet.has(post, now) {
		t.Fatal("quiet channel's last post pruned — it would be imported again")
	}
	busy := mk(now.Unix(), audioNone)
	busy.encode(now)
	if _, kept := busy.entries["a/1"]; kept {
		t.Fatal("old entry kept in a busy archive")
	}
	if !busy.has(post, now) || !busy.has(post, now.Add(30*24*time.Hour)) {
		t.Fatal("pruned post not recognized as already archived")
	}
	// הודעה שמגיעה באיחור של כמה שעות (ערוץ שנחסם זמנית) — עדיין נכנסת.
	if busy.has(FeedItem{ID: 9, Channel: "b", TS: now.Add(-20 * time.Hour).Unix()}, now) {
		t.Fatal("a late post (20 hours) would be dropped")
	}
	waiting := mk(now.Unix(), audioPending)
	waiting.encode(now)
	if _, kept := waiting.entries["a/1"]; !kept {
		t.Fatal("entry with pending audio pruned")
	}
}

// TestArchiveIndexFormat: האינדקס נקרא בחזרה בדיוק כמו שנכתב — וגם אם הרווחים
// הפכו בדרך לטאבים או לכמה רווחים.
func TestArchiveIndexFormat(t *testing.T) {
	now := time.Now()
	a := &archive{next: 10006, last: now.Unix(), cutoff: now.Unix() - 99, channel: "elisha_yered", entries: map[string]*archEntry{
		"elisha_yered/5": {key: "elisha_yered/5", base: 10000, ts: now.Unix() - 60, class: 1, audio: audioDone, media: "v"},
		"elisha_yered/7": {key: "elisha_yered/7", base: 10002, ts: now.Unix(), class: 0, audio: audioNone},
		"elisha_yered/6": {key: "elisha_yered/6", base: -1, ts: now.Unix() - 30},
	}}
	txt := a.encode(now)
	for _, variant := range []string{txt, strings.ReplaceAll(txt, " ", "\t"), strings.ReplaceAll(txt, " ", "   ")} {
		f := parseArchive(variant)
		if f.next != a.next || f.last != a.last || f.cutoff != a.cutoff || len(f.entries) != 3 {
			t.Fatalf("header/entries lost:\n%s\n%+v", variant, f)
		}
		if f.channel != "elisha_yered" {
			t.Fatalf("channel: %q", f.channel)
		}
		for k, want := range a.entries {
			if got := f.entries[k]; got == nil || *got != *want {
				t.Fatalf("%s: got %+v want %+v\n%s", k, got, want, variant)
			}
		}
	}
}

// TestAudioWorkerQueue: עבודה לא מתחילה לפני סוף הסבב (כדי שכל השלוחות של
// אותה הודעה יקבלו הורדה אחת); החדשה קודם; בין סרטון לסרטון יש הפסקה, והודעה
// קולית לא מחכה לה.
func TestAudioWorkerQueue(t *testing.T) {
	defer func(g time.Duration) { videoGap = g }(videoGap)
	videoGap = time.Hour
	w := newAudioWorker()
	w.add(&audioJob{key: "a/1", kind: "v", ts: 1}, audioTarget{"1", 10000})
	if j, _ := w.next(false); j != nil {
		t.Fatal("job started before the cycle ended")
	}
	w.add(&audioJob{key: "a/1", kind: "v", ts: 1}, audioTarget{"2/1", 10000})
	w.add(&audioJob{key: "a/2", kind: "o", ts: 2}, audioTarget{"1", 10002})
	w.add(&audioJob{key: "a/3", kind: "v", ts: 3}, audioTarget{"1", 10004})
	w.release()
	j, job := w.next(false)
	if j == nil || job.key != "a/3" {
		t.Fatalf("want the newest (a/3) first, got %+v", job)
	}
	w.mu.Lock()
	w.finish(j, audioDone)
	w.lastVideo = time.Now()
	w.mu.Unlock()
	if j, job = w.next(false); j == nil || job.key != "a/2" {
		t.Fatalf("during the gap between videos want the voice note (a/2), got %+v", job)
	}
	w.mu.Lock()
	w.finish(j, audioDone)
	w.lastVideo = time.Time{}
	w.mu.Unlock()
	if j, job = w.next(false); j == nil || job.key != "a/1" || len(job.targets) != 2 {
		t.Fatalf("want a/1 with both extensions, got %+v", job)
	}
	w.add(&audioJob{key: "a/1", kind: "v", ts: 1}, audioTarget{"2/1", 10000}) // כבר בעבודה שרצה
	if w.pending() != 1 {
		t.Fatalf("duplicate job for a running download: %d", w.pending())
	}
	w.mu.Lock()
	w.finish(j, audioDone)
	res := len(w.results)
	w.mu.Unlock()
	if res != 3 || w.pending() != 0 {
		t.Fatalf("results=%d pending=%d", res, w.pending())
	}
}

// TestArchiveTrim: ימות המשיח מרשים עד 3,000 קבצים בשלוחה. כשהשלוחה מלאה, כל
// הודעה חדשה מוציאה את הישנה ביותר (הודעה שלמה: הקול וההקראה שלו). הכותרת
// נשארת, והודעה שיצאה לא חוזרת גם אחרי הפעלה מחדש.
func TestArchiveTrim(t *testing.T) {
	defer func(m int) { maxArchiveFiles = m }(maxArchiveFiles)
	maxArchiveFiles = 4
	f := archiveServer(nil, `{"channels":[]}`)
	f.dirs["ivr2:/2/1"] = []string{"ext.ini", "10001.tts", "10002.wav", "10003.tts", "10005.tts", "10007.tts", "10009.tts", "99999.tts"}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	a := &archive{ext: "2/1", files: archiveFiles(f.dirs["ivr2:/2/1"])}
	a.trim(&cfg)
	if got := strings.Join(f.dirs["ivr2:/2/1"], ","); got != "ext.ini,10005.tts,10007.tts,10009.tts,99999.tts" || len(a.files) != 3 {
		t.Fatalf("after trim: %s (list %v)", got, a.files)
	}

	// שלוחה 1 עם גבול של 3 קבצים: הודעה בכל סבב.
	maxArchiveFiles = 3
	now := time.Now().Unix()
	st := &state{}
	for i := 1; i <= 5; i++ {
		f.items = append(f.items, FeedItem{ID: i, Channel: "a", TS: now - int64(100-i), Text: fmt.Sprintf("הודעה %d", i)})
		if err := syncOnce(&cfg, st); err != nil {
			t.Fatal(err)
		}
		got := archiveFiles(f.dirs["ivr2:/1"])
		var want []string
		for j := max(1, i-2); j <= i; j++ { // 3 האחרונות
			want = append(want, fmt.Sprintf("%05d.tts", 10001+2*(j-1)))
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("after message %d: %v, want %v", i, got, want)
		}
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10009.tts"], "הודעה 5") {
		t.Fatalf("newest: %q", f.files["ivr2:/1/10009.tts"])
	}
	// הפעלה חדשה, וההודעות שיצאו עדיין בשרת של ערוץ חי — לא חוזרות.
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(archiveFiles(f.dirs["ivr2:/1"]), ","); got != "10005.tts,10007.tts,10009.tts" {
		t.Fatalf("after restart: %s", got)
	}
}

// TestSixDigits: אחרי 99990 המספור ממשיך ב-100000 (6 ספרות) — מעל הכותרת 99999,
// כך שההודעה החדשה עדיין נשמעת ראשונה.
func TestSixDigits(t *testing.T) {
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: now - 10, Text: "חדשה"}}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	f.dirs["ivr2:/1"] = []string{"ext.ini", "99989.tts", "99991.tts"} // 99990/99991 — ההודעה האחרונה בת 5 ספרות
	f.files["ivr2:/1/ext.ini"] = "type=playfile\nfile_amount_digits=5"
	f.files["ivr2:/1/archive.txt"] = fmt.Sprintf("next=99992\nlast=%d\ncutoff=%d\n", now-100, now-1000)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/100001.tts"], "חדשה") || !strings.Contains(f.files["ivr2:/1/archive.txt"], "next=100002\n") {
		t.Fatalf("files %v index %q", f.dirs["ivr2:/1"], f.files["ivr2:/1/archive.txt"])
	}
	if fileNum("100001.tts") != 100001 || fileNum("99999.tts") != 99999 || isArchiveNum(99999) || fileNum("1000001.tts") != -1 {
		t.Fatal("fileNum / isArchiveNum")
	}
}

// TestExcludeChannel: ערוץ שהוצא מהקו (EXCLUDE_CHANNELS) — ההודעות שלו נמחקות
// משלוחה 1, שלוחת הכתב שלו מתרוקנת ויוצאת מתפריט הבחירה, והודעות חדשות שלו לא
// נכנסות. שאר הערוצים לא נפגעים.
func TestExcludeChannel(t *testing.T) {
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{
		{ID: 1, Channel: "a", TS: now - 300, Text: "של א"},
		{ID: 2, Channel: "SamariaUpdates", TS: now - 200, Text: "של השומרון"},
		{ID: 3, Channel: "a", TS: now - 100, Text: "עוד של א"},
	}, `{"channels":[{"name":"a","title":"אלישע ירד"},{"name":"SamariaUpdates","title":"עדכוני השומרון"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10003.tts"], "של השומרון") || !f.has("ivr2:/2/2", "10001.tts") {
		t.Fatalf("setup: %v | %v", f.dirs["ivr2:/1"], f.dirs["ivr2:/2/2"])
	}
	// מוציאים את הערוץ מהקו (הפעלה חדשה עם ההגדרה), ומגיעה ממנו הודעה חדשה.
	cfg.exclude = parseExclude("samariaupdates, @other")
	f.items = append(f.items, FeedItem{ID: 4, Channel: "SamariaUpdates", TS: now - 10, Text: "חדשה של השומרון"})
	for run := 1; run <= 2; run++ { // וגם בהפעלה שאחריה — כלום לא חוזר
		if err := syncOnce(&cfg, &state{}); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(archiveFiles(f.dirs["ivr2:/1"]), ","); got != "10001.tts,10005.tts" {
			t.Fatalf("run %d, ext 1: %s", run, got)
		}
		if len(archiveFiles(f.dirs["ivr2:/2/2"])) != 0 || f.has("ivr2:/2/2", "99999.tts") || f.files["ivr2:/2/2/archive.txt"] != "" {
			t.Fatalf("run %d, 2/2 not cleared: %v", run, f.dirs["ivr2:/2/2"])
		}
		if f.files["ivr2:/2/M1000.tts"] != "בחירת כתב. לעדכוני אלישע ירד הקישו 1." {
			t.Fatalf("run %d, chooser: %q", run, f.files["ivr2:/2/M1000.tts"])
		}
	}
	if !strings.Contains(f.files["ivr2:/1/archive.txt"], "e SamariaUpdates/2 -1 ") {
		t.Fatalf("index: %q", f.files["ivr2:/1/archive.txt"])
	}
}

// TestPublishedVerb: "פורסם סרטון", "פורסמה תמונה", "פורסמה הודעה קולית", "פורסמו 2 תמונות".
func TestPublishedVerb(t *testing.T) {
	now := time.Now().Unix()
	items := prepare([]FeedItem{
		{Channel: "a", TS: now, HTML: `<div class="photo"><img src="a"></div>`},
		{Channel: "b", TS: now, HTML: `<div class="photo"><div class="album two"><img src="a"><img src="b"></div></div>`},
		{Channel: "c", TS: now, HTML: `<div class="voicebox"><span class="voiceico">🎤</span><audio controls preload="none" src="x.ogg"></audio></div>`},
		{Channel: "d", TS: now, HTML: `<div class="photo"><div class="roundwrap"><video class="roundvid" src="/api/media?channel=d&amp;id=1"></video></div></div>`},
	})
	want := map[string]string{"a": "פורסמה תמונה", "b": "פורסמו 2 תמונות", "c": "פורסמה הודעה קולית", "d": "פורסם סרטון קצר"}
	for _, it := range items {
		if it.Text != want[it.Channel] {
			t.Errorf("%s: %q, want %q", it.Channel, it.Text, want[it.Channel])
		}
		delete(want, it.Channel)
	}
	if len(want) > 0 {
		t.Errorf("missing: %v", want)
	}
}

// TestArchiveStaleIndex: הריצה נקטעה אחרי שהודעה עלתה ולפני שהאינדקס נשמר —
// ממשיכים אחרי הקובץ האחרון שבשלוחה, בלי לדרוס אותו.
func TestArchiveStaleIndex(t *testing.T) {
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{{ID: 7, Channel: "a", TS: now - 10, Text: "חדשה"}}, `{"channels":[]}`)
	f.dirs["ivr2:/1"] = []string{"ext.ini", "10001.tts", "10003.tts"}
	f.files["ivr2:/1/ext.ini"] = "type=playfile\nfile_amount_digits=5"
	f.files["ivr2:/1/10003.tts"] = "שמורה"
	f.files["ivr2:/1/archive.txt"] = fmt.Sprintf("next=10002\nlast=%d\ncutoff=%d\ne a/1 10000 %d 0 0 -\n", now-100, now-1000, now-100)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/1/10003.tts"] != "שמורה" || !strings.HasSuffix(f.files["ivr2:/1/10005.tts"], "חדשה") {
		t.Fatalf("10003=%q 10005=%q", f.files["ivr2:/1/10003.tts"], f.files["ivr2:/1/10005.tts"])
	}
	if !strings.Contains(f.files["ivr2:/1/archive.txt"], "next=10006\n") {
		t.Fatalf("index: %q", f.files["ivr2:/1/archive.txt"])
	}
}

// TestDigitsFailureKeepsOldFiles: כל עוד ההגדרה file_amount_digits=5 לא נכנסה
// (בלעדיה קבצים בני 5 ספרות לא מושמעים), לא עוברים לארכיון ולא מוחקים את הישנים.
func TestDigitsFailureKeepsOldFiles(t *testing.T) {
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: time.Now().Unix() - 10, Text: "שלום"}}, `{"channels":[]}`)
	f.failWhat = map[string]bool{"ivr2:/1/ext.ini": true}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	st := &state{}
	err := syncOnce(&cfg, st)
	if err == nil || !strings.Contains(err.Error(), "file_amount_digits") {
		t.Fatalf("want a file_amount_digits error, got %v", err)
	}
	if !f.has("ivr2:/1", "001.tts") || f.has("ivr2:/1", "10001.tts") {
		t.Fatalf("migrated without file_amount_digits: %v", f.dirs["ivr2:/1"])
	}
	f.failWhat = nil
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.has("ivr2:/1", "001.tts") || !f.has("ivr2:/1", "10001.tts") || !strings.Contains(f.files["ivr2:/1/ext.ini"], "file_amount_digits=5") {
		t.Fatalf("not migrated after the setting went in: %v %q", f.dirs["ivr2:/1"], f.files["ivr2:/1/ext.ini"])
	}
}

// TestArchiveOwnerConflict: שלוחה שהארכיון שבה של ערוץ אחר — לא כותבים לתוכה.
func TestArchiveOwnerConflict(t *testing.T) {
	f := archiveServer(nil, `{"channels":[]}`)
	f.dirs["ivr2:/2/1"] = []string{"ext.ini", "10001.tts"}
	f.files["ivr2:/2/1/archive.txt"] = "next=10002\nlast=0\ncutoff=0\nchannel=b\n"
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if _, err := loadArchive(&cfg, "2/1", "a", false, time.Now()); !errors.Is(err, errArchiveOwner) {
		t.Fatalf("want errArchiveOwner, got %v", err)
	}
}

// TestReporterMappingReadError: אם האינדקס של שלוחת כתב לא נקרא (תקלה זמנית),
// לא מחלקים שלוחות בסבב הזה — אחרת ערוץ היה מקבל שלוחה אחרת מזו שבה הארכיון שלו.
func TestReporterMappingReadError(t *testing.T) {
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{{ID: 5, Channel: "b", TS: now - 20, Text: "של ב"}, {ID: 6, Channel: "a", TS: now - 10, Text: "של א"}},
		`{"channels":[{"name":"b","title":"הקול היהודי"},{"name":"a","title":"אלישע ירד"}]}`)
	f.dirs["ivr2:/2/2"] = []string{"ext.ini", "10001.tts"}
	f.files["ivr2:/2/2/ext.ini"] = "type=playfile\nfile_amount_digits=5"
	f.files["ivr2:/2/2/10001.tts"] = "ישנה של ב"
	f.files["ivr2:/2/2/archive.txt"] = fmt.Sprintf("next=10002\nlast=%d\ncutoff=%d\nchannel=b\n", now-1000, now-5000)
	f.failRead = map[string]bool{"ivr2:/2/2/archive.txt": true}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if _, created := f.dirs["ivr2:/2/1"]; created {
		t.Fatalf("assigned a reporter extension while an index was unreadable: %q", f.files["ivr2:/2/1/10001.tts"])
	}
	f.failRead = nil
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/2/2/10003.tts"], "של ב") || !strings.HasSuffix(f.files["ivr2:/2/1/10001.tts"], "של א") {
		t.Fatalf("2/2=%q 2/1=%q", f.files["ivr2:/2/2/10003.tts"], f.files["ivr2:/2/1/10001.tts"])
	}
}

// TestRerenderAfterRename: הערוץ שינה את שמו — עדכון הזמן בהקראות שכבר
// בשלוחה ממשיך (השם שבהן נשאר כמו שהוא).
func TestRerenderAfterRename(t *testing.T) {
	defer func() { nowFunc = time.Now }()
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	day := time.Date(2026, 9, 24, 20, 0, 0, 0, loc)
	nowFunc = func() time.Time { return day }
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: day.Add(-time.Hour).Unix(), Text: "הודעה"}},
		`{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	f.channels = `{"channels":[{"name":"a","title":"אלישע ירד מהשטח"}]}`
	nowFunc = func() time.Time { return day.Add(6 * time.Hour) }
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/1/10001.tts"] != "אלישע ירד, אתמול בשעה 7 בערב. הודעה" || f.files["ivr2:/2/1/10001.tts"] != "אתמול בשעה 7 בערב. הודעה" {
		t.Fatalf("%q | %q", f.files["ivr2:/1/10001.tts"], f.files["ivr2:/2/1/10001.tts"])
	}
}

// TestArchiveSkipsFailingPost: הודעה שההעלאה שלה נכשלת שוב ושוב — אחרי 3
// ניסיונות מדלגים עליה, כדי שלא תעכב את כל ההודעות שאחריה.
func TestArchiveSkipsFailingPost(t *testing.T) {
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{
		{ID: 1, Channel: "a", TS: now - 30, Text: "ראשונה"},
		{ID: 2, Channel: "a", TS: now - 20, Text: "תקולה זנזנת"},
		{ID: 3, Channel: "a", TS: now - 10, Text: "שלישית"},
	}, `{"channels":[]}`)
	f.failText = "זנזנת"
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	st := &state{}
	for round := 1; round <= 2; round++ {
		if err := syncOnce(&cfg, st); err == nil {
			t.Fatalf("round %d: want an error while the post keeps failing", round)
		}
		if f.has("ivr2:/1", "10003.tts") {
			t.Fatalf("round %d: a later post jumped the queue", round)
		}
	}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10001.tts"], "ראשונה") || !strings.HasSuffix(f.files["ivr2:/1/10003.tts"], "שלישית") {
		t.Fatalf("10001=%q 10003=%q", f.files["ivr2:/1/10001.tts"], f.files["ivr2:/1/10003.tts"])
	}
	if !strings.Contains(f.files["ivr2:/1/archive.txt"], "e a/2 -1 ") {
		t.Fatalf("skipped post not recorded:\n%s", f.files["ivr2:/1/archive.txt"])
	}
}

// TestFirstImportWindow: בהפעלה הראשונה מייבאים 48 שעות אחורה — ולפחות 20
// הודעות אחרונות בשלוחה 1 (10 בשלוחת כתב), גם אם הן ישנות יותר.
func TestFirstImportWindow(t *testing.T) {
	defer func(d time.Duration) { importPace = d }(importPace)
	importPace = 0
	now := time.Now()
	var items []FeedItem
	for i := 1; i <= 25; i++ { // ישנות: לפני 4-5 ימים
		items = append(items, FeedItem{ID: i, Channel: "a", TS: now.Add(-121*time.Hour + time.Duration(i)*time.Hour).Unix(), Text: fmt.Sprintf("ישנה %d", i)})
	}
	for i := 1; i <= 10; i++ { // מהיממה האחרונה
		items = append(items, FeedItem{ID: 100 + i, Channel: "a", TS: now.Add(-21*time.Hour + time.Duration(i)*time.Hour).Unix(), Text: fmt.Sprintf("חדשה %d", i)})
	}
	f := archiveServer(items, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	fl := f.files
	if !strings.HasSuffix(fl["ivr2:/1/10001.tts"], "ישנה 16") || !strings.HasSuffix(fl["ivr2:/1/10039.tts"], "חדשה 10") || f.has("ivr2:/1", "10041.tts") {
		t.Fatalf("ext 1: first=%q last=%q files=%v", fl["ivr2:/1/10001.tts"], fl["ivr2:/1/10039.tts"], f.dirs["ivr2:/1"])
	}
	if !strings.HasSuffix(fl["ivr2:/2/1/10001.tts"], "חדשה 1") || !strings.HasSuffix(fl["ivr2:/2/1/10019.tts"], "חדשה 10") || f.has("ivr2:/2/1", "10021.tts") {
		t.Fatalf("2/1: first=%q last=%q files=%v", fl["ivr2:/2/1/10001.tts"], fl["ivr2:/2/1/10019.tts"], f.dirs["ivr2:/2/1"])
	}
}

// podcastRSS: הזנת RSS לבדיקות, כמו של spreaker — פרק לכל זמן, החדש ראשון.
func podcastRSS(base string, eps ...int64) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>חושבים בקול - הקול היהודי</title>`)
	for i := len(eps) - 1; i >= 0; i-- {
		fmt.Fprintf(&b, `<item><title>פרק %d, שיחה על ההתיישבות</title><guid isPermaLink="false">https://api.example/episode/%d</guid>`+
			`<pubDate>%s</pubDate><enclosure url="%s/ep/%d.mp3" length="1000" type="audio/mpeg"/><itunes:duration>2671</itunes:duration></item>`,
			i+1, i+1, time.Unix(eps[i], 0).UTC().Format(time.RFC1123Z), base, i+1)
	}
	b.WriteString(`</channel></rss>`)
	return b.String()
}

// TestPodcasts: שלוחה 3 — תפריט פודקאסטים ושלוחה לכל פודקאסט. נכנסים הפרקים
// האחרונים (הקול, ואחריו ההקראה עם שם הפרק); פרק חדש נכנס ראשון והישן ביותר
// יוצא; ואחרי הפעלה מחדש שום דבר לא נכנס ולא יורד פעמיים.
func TestPodcasts(t *testing.T) {
	withFakeTranscode(t)
	defer func(d time.Duration) { podcastEvery = d }(podcastEvery)
	podcastEvery = 0
	day, now := int64(24*3600), time.Now().Unix()
	f := archiveServer(nil, `{"channels":[]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	f.rss = podcastRSS(srv.URL, now-3*day, now-2*day, now-day)
	cfg := newTestCfg(srv)
	cfg.podcasts = parsePodcasts("חושבים בקול של הקול היהודי | " + srv.URL + "/rss/pod")
	cfg.podcastExt, cfg.podcastKeep = "3", 2
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	fl := f.files
	if fl["ivr2:/3/ext.ini"] != "type=menu\ndigits=1" || fl["ivr2:/3/M1000.tts"] != "פודקאסטים. לחושבים בקול של הקול היהודי הקישו 1." {
		t.Fatalf("ext 3: %q | %q", fl["ivr2:/3/ext.ini"], fl["ivr2:/3/M1000.tts"])
	}
	if fl["ivr2:/3/1/ext.ini"] != "type=playfile\nfile_amount_digits=5" || fl["ivr2:/3/1/99999.tts"] != "חושבים בקול של הקול היהודי." {
		t.Fatalf("3/1: %q | %q", fl["ivr2:/3/1/ext.ini"], fl["ivr2:/3/1/99999.tts"])
	}
	// שני הפרקים האחרונים (2 ו-3), הישן לפני החדש; הראשון — לא.
	if fl["ivr2:/3/1/10000.wav"] != "AUDIO:MP3:EP-2.mp3;convert=1" || fl["ivr2:/3/1/10002.wav"] != "AUDIO:MP3:EP-3.mp3;convert=1" || f.has("ivr2:/3/1", "10004.wav") {
		t.Fatalf("episodes: %v", f.dirs["ivr2:/3/1"])
	}
	intro := fl["ivr2:/3/1/10003.tts"]
	if !strings.HasPrefix(intro, "פרק 3, שיחה על ההתיישבות. פורסם ב ") || !strings.HasSuffix(intro, ", באורך 45 דקות.") {
		t.Fatalf("intro: %q", intro)
	}
	if !strings.Contains(fl["ivr2:/M1000.tts"], "לפודקאסטים, הקישו 3.") {
		t.Fatalf("welcome: %q", fl["ivr2:/M1000.tts"])
	}
	// פרק חדש: נכנס ראשון (המספר הגבוה), והישן ביותר (פרק 2) נמחק.
	f.rss = podcastRSS(srv.URL, now-3*day, now-2*day, now-day, now-60)
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fl["ivr2:/3/1/10005.tts"], "פרק 4") || f.has("ivr2:/3/1", "10000.wav") || f.has("ivr2:/3/1", "10001.tts") || !f.has("ivr2:/3/1", "10003.tts") {
		t.Fatalf("after a new episode: %v", f.dirs["ivr2:/3/1"])
	}
	// הפעלה מחדש: שום דבר לא נכנס שוב ולא יורד שוב.
	hits := f.mediaHits
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if f.mediaHits != hits || f.has("ivr2:/3/1", "10007.tts") || !f.has("ivr2:/3/1", "10005.tts") {
		t.Fatalf("restart: downloads %d→%d, files %v", hits, f.mediaHits, f.dirs["ivr2:/3/1"])
	}
	if len(parsePodcasts("# הערה\nלא קישור\nhttps://a.example/feed")) != 1 {
		t.Fatal("parsePodcasts")
	}
}

// TestTranscodeReal: הפקודה האמיתית של ffmpeg (כמו שתרוץ ב-GitHub) — קול יוצא,
// וסרטון בלי קול מזוהה כ"אין קול" (לא מנסים שוב).
func TestTranscodeReal(t *testing.T) {
	if !haveFFmpeg() {
		t.Skip("ffmpeg לא מותקן")
	}
	dir := t.TempDir()
	withSound, silent := dir+"/a.mp4", dir+"/b.mp4"
	gen := func(out string, audio bool) {
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:s=64x64:d=2"}
		if audio {
			args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-shortest")
		}
		if b, err := exec.Command("ffmpeg", append(args, out)...).CombinedOutput(); err != nil {
			t.Skipf("לא הצלחתי ליצור סרטון לבדיקה: %v %s", err, b)
		}
	}
	gen(withSound, true)
	gen(silent, false)
	if err := transcode(withSound, dir+"/a.mp3", 20*60, false); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dir + "/a.mp3"); err != nil || st.Size() < 1000 {
		t.Fatalf("mp3 missing or too small: %v %v", st, err)
	}
	if err := transcode(withSound, dir+"/p.mp3", 0, true); err != nil { // פרק של פודקאסט
		t.Fatal(err)
	}
	if st, err := os.Stat(dir + "/p.mp3"); err != nil || st.Size() < 1000 {
		t.Fatalf("podcast mp3 missing or too small: %v %v", st, err)
	}
	err := transcode(silent, dir+"/b.mp3", 60, false)
	if err == nil || !strings.Contains(err.Error(), "matches no streams") {
		t.Fatalf("silent video should report no audio stream, got %v", err)
	}
}

// חתימת "להצטרפות לערוץ..." בסוף הודעה — לא פרסומת: ההודעה נכנסת, בלי החתימה.
func TestPromoFooterIsNotAnAd(t *testing.T) {
	cases := map[string]string{
		"הותרו לפרסום שמותיהם של שני חללי צהל. השם יקום דמם. להצטרפות לערוץ הטלגרם של אלחנן גרונר": "הותרו לפרסום שמותיהם של שני חללי צהל. השם יקום דמם.",
		"פיגוע דריסה באזור כביש 443. המחבל חוסל. להצטרפות לערוץ >> t.me/x":                         "פיגוע דריסה באזור כביש 443. המחבל חוסל.",
		"עדכון מהשטח. להצטרפות לעדכוני הקול היהודי בוואטסאפ":                                       "עדכון מהשטח.",
		"מבצע צבאי נרחב בשומרון": "מבצע צבאי נרחב בשומרון",
	}
	for in, want := range cases {
		if got, _ := stripPromo(in); got != want {
			t.Errorf("stripPromo(%q) = %q, want %q", in, got, want)
		}
	}
	// ביטוי באמצע הודעה ארוכה — לא נוגעים
	mid := "קוראים להצטרפות לערוץ החדש של המועצה. " + strings.Repeat("ועוד פרטים רבים על האירוע. ", 10)
	if got, found := stripPromo(mid); found || got != mid {
		t.Errorf("mid: %v %q", found, got)
	}
	// פרסומת אמיתית — עדיין נזרקת
	now := time.Now().Unix()
	items := prepareClean([]FeedItem{
		{ID: 1, Channel: "realelchangr", TS: now, Text: "המחבל חוסל. להצטרפות לערוץ הטלגרם של אלחנן גרונר"},
		{ID: 2, Channel: "realelchangr", TS: now, Text: "הספר החדש לרכישה במחיר מבצע עם קוד קופון GRONER69. להצטרפות לערוץ הטלגרם של אלחנן גרונר"},
		{ID: 3, Channel: "realelchangr", TS: promoSince - 3600, Text: "הודעה ישנה. להצטרפות לערוץ הטלגרם של אלחנן גרונר"},
		{ID: 4, Channel: "hakolhayehudi", TS: promoSince - 3600, Text: "הודעה ישנה. להצטרפות לעדכוני הקול היהודי בוואטסאפ"},
	})
	if len(items) != 3 || items[0].Text != "המחבל חוסל." || items[0].OldPromo != true || promoBacklog(items[0]) {
		t.Fatalf("%+v", items)
	}
	// הודעה ישנה שהכלל הישן זרק — לא נכנסת באיחור; הודעה ישנה שהכלל הישן לא זרק — כרגיל
	if !promoBacklog(items[1]) || promoBacklog(items[2]) {
		t.Fatalf("backlog: %+v", items[1:])
	}
}
