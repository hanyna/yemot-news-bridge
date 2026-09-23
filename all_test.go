package main

import (
	"encoding/json"
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

// fakeYemotServer: שרת מדומה של ערוץ חי + ימות המשיח. dirs = קבצים קיימים לכל שלוחה.
type fakeYemotServer struct {
	mu       sync.Mutex
	files    map[string]string   // נתיב מלא → תוכן שהועלה
	dirs     map[string][]string // "ivr2:/3" → שמות קבצים קיימים
	uploads  []string
	items    []FeedItem
	channels string
	getText  bool // האם GetTextFile מורשה
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
		f.files[what] = r.PostForm.Get("contents")
		f.uploads = append(f.uploads, what)
		dir := what[:strings.LastIndex(what, "/")]
		if dir == "ivr2:" {
			dir = "ivr2:/"
		}
		name := what[strings.LastIndex(what, "/")+1:]
		found := false
		for _, n := range f.dirs[dir] {
			if n == name {
				found = true
			}
		}
		if !found {
			f.dirs[dir] = append(f.dirs[dir], name)
		}
		ok(nil)
	case strings.HasSuffix(r.URL.Path, "GetTextFile"):
		if !f.getText {
			w.Write([]byte(`{"responseStatus":"FORBIDDEN","message":"API_KEY_ACL_REJECT"}`))
			return
		}
		c, found := f.files[r.PostForm.Get("what")]
		if !found {
			w.Write([]byte(`{"responseStatus":"ERROR","message":"file not found"}`))
			return
		}
		ok(map[string]any{"contents": c})
	case strings.HasSuffix(r.URL.Path, "GetIVR2Dir"):
		names, found := f.dirs[r.PostForm.Get("path")]
		if !found {
			w.Write([]byte(`{"responseStatus":"ERROR","message":"path not found"}`))
			return
		}
		var fs []map[string]string
		for _, n := range names {
			fs = append(fs, map[string]string{"name": n})
		}
		ok(map[string]any{"files": fs})
	default:
		ok(nil)
	}
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
		files: map[string]string{},
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
	if fl["ivr2:/2/ext.ini"] != "type=menu" {
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
	f := &fakeYemotServer{files: map[string]string{}, dirs: map[string][]string{"ivr2:/": {"ext.ini"}},
		items: []FeedItem{{Channel: "a", TS: time.Now().Unix(), Text: "שלום"}}, channels: `{"channels":[]}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.listID = "123"
	st := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/8/1/ext.ini"] != "type=template_add_number\ntemplate_id=123" || f.files["ivr2:/8/2/ext.ini"] != "type=template_remove_number\ntemplate_id=123" {
		t.Fatalf("list exts: %q %q", f.files["ivr2:/8/1/ext.ini"], f.files["ivr2:/8/2/ext.ini"])
	}
	if !strings.Contains(f.files["ivr2:/M1000.tts"], "רשימת התפוצה, הקישו 8.") {
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
