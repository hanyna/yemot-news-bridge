package main

// ארכיון: כל הודעה מקבלת מספר קבוע בשלוחה, ונשארת בה. ימות המשיח מרשים עד
// 3,000 קבצים בתיקייה (ומעבר לזה מוחקים בעצמם, בלי התראה) — לכן כשהשלוחה
// מגיעה כמעט לגבול, כל הודעה חדשה מוציאה את הישנה ביותר (trim).
//
// ימות המשיח משמיעים שלוחת השמעה מהקובץ עם המספר הגבוה ביותר ויורדים
// (start=max — ברירת המחדל). לכן הודעה חדשה מקבלת מספר גבוה מכל הקודמות:
// המאזין שומע אותה ראשונה, ממשיך (מקש 2) להודעות ישנות יותר, ו"המשך מהמקום
// שהפסקתם" (שלוחה 5) מדויק — מספר של הודעה לא משתנה לעולם.
//
// כל הודעה תופסת זוג מספרים (file_amount_digits=5 — 5 ספרות ומעלה):
//
//	b+1 — ההקראה (NNNNN.tts) — נשמעת ראשונה
//	b   — הקול של סרטון / הודעה קולית (NNNNN.wav), אם יש
//
// אחרי 99990 המספור ממשיך ב-100000 (6 ספרות) — 99999 שמור לכותרת.
//
// מה כבר נשמר בשלוחה — בקובץ archive.txt שבה (ימות המשיח לא משמיעים ולא
// מציגים קבצי txt). נשמרות בו ההודעות מהימים האחרונים: זה מה שצריך כדי לא
// לכפול הודעות, ולעדכן בהקראה "היום" ← "אתמול" ← "ביום שני" ← תאריך.

import (
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	archiveFirst   = 10000       // הבסיס של ההודעה הראשונה (מתחת — שמור לייבוא היסטוריה בעתיד)
	archiveLast    = 99990       // הבסיס האחרון בן 5 ספרות; 99999 שמור לכותרת של שלוחת כתב
	sixFirst       = 100000      // משם ממשיכים ב-6 ספרות (בקצב הנוכחי — אחרי יותר משנה בשלוחה 1)
	sixLast        = 999990      // תקרה
	titleFile      = "99999.tts" // "עדכוני אלישע ירד." — נשמע ראשון בכניסה לשלוחת כתב
	archiveIndex   = "archive.txt"
	fileDigits     = "5"
	lateWindow     = 12 * 3600 // הודעה ישנה ביותר מזה מהחדשה שבארכיון, ולא באינדקס — כבר טופלה
	keepIndexDays  = 9         // כמה ימים הודעה נשארת באינדקס (הניסוח משתנה עד היום ה-7)
	rerenderBudget = 40        // כמה הקראות מעדכנים בסבב אחד (אחרי חצות יש הרבה)
	firstImportAge = 48 * time.Hour
	saveEvery      = 20 // בייבוא גדול — שומרים את האינדקס כל כמה הודעות
	addTries       = 3  // הודעה שההעלאה שלה נכשלת שוב ושוב — מדלגים עליה (שלא תעכב את החדשות)
	rerenderTries  = 3
)

// maxArchiveFiles: ימות המשיח מרשים עד 3,000 קבצים ותיקיות בתיקייה, ומעבר לזה
// מוחקים בעצמם את הישנים — בלי התראה. לכן בכל שלוחה נשמרים עד כמה קבצי הודעות
// (כמעט עד הגבול — נשאר מקום ל-ext.ini, archive.txt, הכותרת ויומנים של ימות),
// ומשם כל הודעה חדשה מוציאה את הישנה ביותר. (משתנה — לבדיקות.)
var maxArchiveFiles = 2980

// בייבוא הראשון: לפחות כמה הודעות אחרונות, גם אם הן ישנות מ-48 שעות (ערוץ שקט).
var firstImportMin = map[bool]int{true: 20, false: 10} // withName (שלוחה 1) / שלוחת כתב

// importPace: הפסקה בין העלאות כשמעלים הרבה בבת אחת (שלא להציף את ימות המשיח).
var importPace = 120 * time.Millisecond

// מצב הקול של הודעה בארכיון.
const (
	audioNone    = iota // אין בהודעה סרטון או הודעה קולית
	audioPending        // ממתין להורדה ולהעלאה
	audioDone           // הקובץ בשלוחה
	audioNo             // לא זמין: ישן, ארוך מדי, בלי קול, או שטלגרם לא נותנים
)

type archEntry struct {
	key   string // ערוץ/מזהה ההודעה
	base  int    // -1: דילגנו על ההודעה (ההעלאה נכשלה שוב ושוב)
	ts    int64
	class int    // ניסוח הזמן שבהקראה שבשלוחה (whenClass)
	audio int    // audioNone / audioPending / audioDone / audioNo
	media string // "v" סרטון, "o" הודעה קולית, "" אין
}

type archive struct {
	ext      string
	channel  string // שלוחת כתב: הערוץ שלה. שלוחה 1: ריק
	withName bool   // שלוחה 1: כל הודעה פותחת בשם הערוץ
	next     int
	last     int64 // ה-ts של ההודעה החדשה ביותר שנכנסה
	cutoff   int64 // הודעות ישנות מזה — מלפני הארכיון, לא נכנסות
	fresh    bool  // אין עדיין אינדקס: הייבוא הראשון
	entries  map[string]*archEntry
	dirty    bool
	requeued bool
	spoken   bool           // requeueSpeech כבר רץ בהפעלה הזו
	adds     int            // הודעות שנוספו מאז השמירה האחרונה
	addFails map[string]int // ניסיונות העלאה שנכשלו, לכל הודעה
	rrFails  map[string]int // ניסיונות עדכון זמן שנכשלו, לכל הודעה
	files    []string       // קבצי ההודעות בשלוחה, מהמספר הנמוך לגבוה (הרשימה בטעינה, ומה שנוסף מאז)
	trimWait time.Time      // מחיקה נכשלה — לא מנסים שוב לפני כן
}

func introFile(base int) string { return fmt.Sprintf("%05d.tts", base+1) }
func audioFile(base int) string { return fmt.Sprintf("%05d.wav", base) }

// itemKey: מזהה קבוע של הודעה — הערוץ ומספר ההודעה בטלגרם.
func itemKey(it FeedItem) string {
	if it.ID > 0 {
		return it.Channel + "/" + strconv.Itoa(it.ID)
	}
	h := fnv.New32a()
	h.Write([]byte(it.Text))
	return fmt.Sprintf("%s/t%d-%x", it.Channel, it.TS, h.Sum32())
}

// fileNum: המספר שבשם קובץ ארכיון (12345.tts / 12345.wav / 123456.tts), או -1.
func fileNum(name string) int {
	n := strings.ToLower(name)
	if !(strings.HasSuffix(n, ".tts") || strings.HasSuffix(n, ".wav")) {
		return -1
	}
	d := n[:len(n)-4]
	if len(d) != 5 && len(d) != 6 {
		return -1
	}
	for _, c := range d {
		if c < '0' || c > '9' {
			return -1
		}
	}
	v, _ := strconv.Atoi(d)
	return v
}

// isArchiveNum: מספר של קובץ הודעה בארכיון (לא הכותרת 99999).
func isArchiveNum(v int) bool {
	return (v >= archiveFirst && v <= archiveLast+1) || (v >= sixFirst && v <= sixLast+1)
}

// archiveFiles: קבצי ההודעות מתוך רשימת הקבצים בשלוחה, מהמספר הנמוך לגבוה.
func archiveFiles(names []string) []string {
	var out []string
	for _, n := range names {
		if isArchiveNum(fileNum(n)) {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return fileNum(out[i]) < fileNum(out[j]) })
	return out
}

// addFile מוסיף קובץ לרשימה (במקומו לפי המספר; קובץ שכבר ברשימה — לא פעמיים).
func (a *archive) addFile(name string) {
	v := fileNum(name)
	i := sort.Search(len(a.files), func(i int) bool { return fileNum(a.files[i]) >= v })
	for j := i; j < len(a.files) && fileNum(a.files[j]) == v; j++ {
		if strings.EqualFold(a.files[j], name) {
			return
		}
	}
	a.files = append(a.files, "")
	copy(a.files[i+1:], a.files[i:])
	a.files[i] = name
}

// isOldTTS: קובץ הקראה מהמבנה הקודם (001.tts ... — 10 האחרונות שהתחלפו כל הזמן).
func isOldTTS(name string) bool {
	n := strings.ToLower(name)
	if len(n) != 7 || !strings.HasSuffix(n, ".tts") {
		return false
	}
	_, err := strconv.Atoi(n[:3])
	return err == nil
}

type archiveFile struct {
	next         int
	last, cutoff int64
	channel      string
	entries      map[string]*archEntry
}

func parseArchive(txt string) archiveFile {
	f := archiveFile{entries: map[string]*archEntry{}}
	for _, l := range strings.Split(strings.ReplaceAll(txt, "\r\n", "\n"), "\n") {
		l = strings.TrimRight(l, " \r")
		switch {
		case strings.HasPrefix(l, "next="):
			f.next, _ = strconv.Atoi(strings.TrimPrefix(l, "next="))
		case strings.HasPrefix(l, "last="):
			f.last, _ = strconv.ParseInt(strings.TrimPrefix(l, "last="), 10, 64)
		case strings.HasPrefix(l, "cutoff="):
			f.cutoff, _ = strconv.ParseInt(strings.TrimPrefix(l, "cutoff="), 10, 64)
		case strings.HasPrefix(l, "channel="):
			f.channel = strings.TrimSpace(strings.TrimPrefix(l, "channel="))
		case strings.HasPrefix(l, "e ") || strings.HasPrefix(l, "e\t"):
			p := strings.Fields(l) // רווחים (ואם ימות המשיח ישנו אותם לטאבים — גם זה עובד)
			if len(p) < 6 {
				continue
			}
			e := &archEntry{key: p[1]}
			var err error
			if e.base, err = strconv.Atoi(p[2]); err != nil {
				continue
			}
			if e.ts, err = strconv.ParseInt(p[3], 10, 64); err != nil {
				continue
			}
			e.class, _ = strconv.Atoi(p[4])
			e.audio, _ = strconv.Atoi(p[5])
			if len(p) > 6 && p[6] != "-" {
				e.media = p[6]
			}
			f.entries[e.key] = e
		}
	}
	return f
}

// encode: תוכן archive.txt. הודעה יוצאת מהאינדקס (אבל נשארת בשלוחה) רק כשהיא
// ישנה מ-9 ימים וגם מכוסה בכלל "ישנה ביותר מ-12 שעות מהחדשה שבארכיון" — אחרת
// היא הייתה נראית חדשה ונכנסת שוב (למשל בערוץ שקט שההודעה האחרונה שלו ישנה).
func (a *archive) encode(now time.Time) string {
	horizon := now.Add(-keepIndexDays * 24 * time.Hour).Unix()
	var list []*archEntry
	for k, e := range a.entries {
		if e.ts < horizon && (e.ts <= a.last-lateWindow || e.ts < a.cutoff) && e.audio != audioPending {
			delete(a.entries, k)
			continue
		}
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].base != list[j].base {
			return list[i].base < list[j].base
		}
		return list[i].ts < list[j].ts
	})
	var b strings.Builder
	b.WriteString("# yemot-news-bridge: מה כבר נשמר בשלוחה. לא למחוק — בלי הקובץ הזה הגשר לא יודע אילו הודעות כבר כאן.\n")
	fmt.Fprintf(&b, "next=%d\nlast=%d\ncutoff=%d\n", a.next, a.last, a.cutoff)
	if a.channel != "" {
		fmt.Fprintf(&b, "channel=%s\n", a.channel)
	}
	for _, e := range list {
		media := e.media
		if media == "" {
			media = "-"
		}
		// שורה לכל הודעה: e <ערוץ/מזהה> <מספר> <זמן> <ניסוח הזמן> <מצב הקול> <סוג המדיה>
		fmt.Fprintf(&b, "e %s %d %d %d %d %s\n", e.key, e.base, e.ts, e.class, e.audio, media)
	}
	return b.String()
}

func (a *archive) save(cfg *config, now time.Time) error {
	if err := cfg.y.upload(a.ext, archiveIndex, a.encode(now)); err != nil {
		return fmt.Errorf("שמירת %s בשלוחה %s: %w", archiveIndex, a.ext, err)
	}
	a.dirty, a.adds = false, 0
	return nil
}

var errArchiveOwner = errors.New("השלוחה שייכת לערוץ אחר")

// loadArchive קורא את הארכיון של שלוחה.
//   - יש אינדקס: ממשיכים ממנו. אם בשלוחה יש קבצים מעבר ל-next שבאינדקס (האינדקס
//     לא נשמר לפני שהריצה נקטעה) — ממשיכים אחריהם, כדי לא לדרוס הודעה קיימת.
//   - אין אינדקס ואין קבצי ארכיון: מעבר מהמבנה הקודם (מוחק את 001.tts...
//     הישנים; הייבוא הראשון — ב-sync).
//   - אין אינדקס אבל יש קבצי ארכיון (האינדקס נמחק): ממשיכים אחריהם, בלי לייבא
//     שוב את מה שכבר בשרת.
func loadArchive(cfg *config, ext, channel string, withName bool, now time.Time) (*archive, error) {
	a := &archive{ext: ext, channel: channel, withName: withName, next: archiveFirst,
		entries: map[string]*archEntry{}, addFails: map[string]int{}, rrFails: map[string]int{}}
	txt, exists, err := cfg.y.read(ext, archiveIndex)
	if err != nil {
		return nil, err
	}
	info, err := cfg.y.dir(ext)
	if err != nil {
		return nil, err
	}
	if !info.Exists {
		// שלוחה שלא קיימת — יוצרים (העלאת קבצים לשלוחה שלא קיימת "מצליחה" בלי לעשות כלום).
		ini, _ := setIniValues("type=playfile\nfile_amount_digits="+fileDigits, [][2]string{{"voice", cfg.voice}, {"rate", cfg.rate}})
		if err := cfg.y.createExt(ext, ini); err != nil {
			return nil, fmt.Errorf("יצירת שלוחה %s: %w", ext, err)
		}
		log.Printf("שלוחה %s נוצרה (ארכיון).", ext)
	}
	a.files = archiveFiles(info.Files)
	top := -1 // המספר הגבוה ביותר של קובץ הודעה בשלוחה
	if len(a.files) > 0 {
		top = fileNum(a.files[len(a.files)-1])
	}
	if exists {
		f := parseArchive(txt)
		if channel != "" && f.channel != "" && f.channel != channel {
			return nil, fmt.Errorf("%w: בשלוחה %s הארכיון של %s, לא של %s", errArchiveOwner, ext, f.channel, channel)
		}
		if f.next > archiveFirst {
			a.next = f.next
		}
		a.last, a.cutoff, a.entries = f.last, f.cutoff, f.entries
		if a.channel == "" {
			a.channel = f.channel
		}
		if top >= a.next {
			log.Printf("אזהרה: בשלוחה %s יש קבצים עד %05d, מעבר למה שנשמר באינדקס (%05d) — ממשיך אחריהם.", ext, top, a.next)
			a.next, a.dirty = top-top%2+2, true
		}
		return a, nil
	}
	a.dirty = true
	if top >= 0 {
		a.next = top - top%2 + 2
		a.cutoff = now.Unix() + 1 // מה שכבר פורסם — כבר בשלוחה (אי אפשר לדעת מה חסר)
		log.Printf("אזהרה: בשלוחה %s יש הודעות בארכיון אבל אין %s — ממשיך אחרי %05d, בלי לייבא שוב את ההודעות שכבר בשרת.", ext, archiveIndex, top)
		return a, nil
	}
	a.fresh = true
	var old []string
	for _, n := range info.Files {
		if isOldTTS(n) {
			old = append(old, ivrPath(ext, n))
		}
	}
	if len(old) > 0 {
		if err := cfg.y.remove(old); err != nil {
			log.Printf("הערה: מחיקת ההקראות מהמבנה הקודם בשלוחה %s לא הצליחה (לא קריטי): %v", ext, err)
		} else {
			log.Printf("שלוחה %s: עוברת לארכיון קבוע — נמחקו %d הקראות מהמבנה הקודם.", ext, len(old))
		}
	}
	return a, nil
}

// has: האם ההודעה כבר טופלה — בארכיון או דולגה (כפולה / נכשלה שוב ושוב), מלפני
// הארכיון, או שיצאה מהאינדקס (ישנה מ-9 ימים, וגם 12 שעות לפני החדשה שבארכיון —
// ראו encode). הודעה שמגיעה באיחור (למשל ערוץ שטלגרם חסמו לכמה שעות) — עדיין
// נכנסת, כל עוד היא מ-9 הימים האחרונים.
func (a *archive) has(it FeedItem, now time.Time) bool {
	if _, ok := a.entries[itemKey(it)]; ok {
		return true
	}
	return it.TS < a.cutoff || (it.TS <= a.last-lateWindow && it.TS < now.Add(-keepIndexDays*24*time.Hour).Unix())
}

// promoBacklog: הודעה עם חתימת "להצטרפות לערוץ" מלפני התיקון (promoSince) — לא
// נכנסת באיחור (הכלל הישן זרק אותה; ראו stripPromo ב-content.go).
func promoBacklog(it FeedItem) bool { return it.OldPromo && it.TS < promoSince }

// sync מוסיף לארכיון את ההודעות החדשות. items: נקיות (prepareClean), מהישנה
// לחדשה. הודעה שדומה להודעה שכבר בארכיון (אותה הודעה שהועברה בערוץ אחר) — לא
// נוספת. כישלון בהעלאה עוצר (כדי לשמור על הסדר) — ננסה שוב בסבב הבא; הודעה
// שנכשלת שוב ושוב — מדלגים עליה.
func (a *archive) sync(cfg *config, st *state, items []FeedItem, titles map[string]string, now time.Time) error {
	if a.fresh {
		a.fresh = false
		a.cutoff = firstImportFrom(items, now, firstImportMin[a.withName])
		a.dirty = true
	}
	if !a.requeued {
		a.requeued = true
		a.requeue(cfg, st, items, now)
	}
	a.requeueSpeech(cfg, now)
	var seen []string
	pending := 0
	for _, it := range items {
		if promoBacklog(it) {
			continue
		}
		if a.has(it, now) {
			if !it.MediaOnly {
				seen = append(seen, dedupeKey(it.Text))
			}
		} else if it.TS > 0 {
			pending++
		}
	}
	bulk := pending > saveEvery
	if bulk {
		log.Printf("שלוחה %s: מוסיף %d הודעות לארכיון.", a.ext, pending)
	}
	for _, it := range items {
		if it.TS <= 0 || promoBacklog(it) || a.has(it, now) {
			continue
		}
		k := ""
		if !it.MediaOnly {
			k = dedupeKey(it.Text)
			dup := false
			for _, s := range seen {
				if similar(k, s) {
					dup = true
					break
				}
			}
			if dup {
				// נרשמת כמטופלת — כדי שלא תיכנס כשההודעה המקורית תצא מהשרת.
				a.entries[itemKey(it)] = &archEntry{key: itemKey(it), base: -1, ts: it.TS}
				a.dirty = true
				continue
			}
		}
		if a.next > sixLast {
			return fmt.Errorf("המספור בשלוחה %s הגיע לסוף (%d), והודעות חדשות לא נוספות. כדי להתחיל מחדש: למחוק בשלוחה את קבצי ההודעות ואת %s", a.ext, a.next, archiveIndex)
		}
		if err := a.add(cfg, st, it, titles, now); err != nil {
			key := itemKey(it)
			a.addFails[key]++
			if a.addFails[key] < addTries {
				return err
			}
			log.Printf("אזהרה: מדלג על הודעה %s בשלוחה %s אחרי %d ניסיונות: %v", key, a.ext, addTries, err)
			a.entries[key] = &archEntry{key: key, base: -1, ts: it.TS}
			a.dirty = true
			continue
		}
		if k != "" {
			seen = append(seen, k)
		}
		if a.adds >= saveEvery {
			if err := a.save(cfg, now); err != nil {
				return err
			}
		}
		if bulk {
			time.Sleep(importPace)
		}
	}
	a.trim(cfg)
	return nil
}

// trimRetry: אחרי מחיקה שנכשלה — מתי לנסות שוב (עם רשימת קבצים מעודכנת מהשרת).
var trimRetry = 10 * time.Minute

// trim: כשבשלוחה יותר מ-maxArchiveFiles קבצי הודעות — מוחקים את ההודעות
// הישנות ביותר (המספרים הנמוכים), הודעה שלמה בכל פעם (הקול b וההקראה b+1),
// עד שחוזרים לגבול. במצב רגיל: כל הודעה חדשה מוציאה את הישנה ביותר.
func (a *archive) trim(cfg *config) {
	if len(a.files) <= maxArchiveFiles || time.Now().Before(a.trimWait) {
		return
	}
	removed, first, last := 0, "", ""
	for len(a.files) > maxArchiveFiles {
		n := 0 // כמה קבצים מתחילת הרשימה נמחקים בבקשה הזו (עד כ-50)
		for len(a.files)-n > maxArchiveFiles && n < 50 {
			base := fileNum(a.files[n]) &^ 1 // הבסיס (זוגי) של ההודעה
			for n < len(a.files) && fileNum(a.files[n])&^1 == base {
				n++
			}
		}
		paths := make([]string, n)
		for i, name := range a.files[:n] {
			paths[i] = ivrPath(a.ext, name)
		}
		if err := cfg.y.remove(paths); err != nil {
			// אולי הקבצים כבר לא שם (נמחקו ידנית) — רושמים מחדש מהשרת, ומנסים שוב אחר כך.
			log.Printf("הערה: מחיקת הודעות ישנות משלוחה %s נכשלה — ננסה שוב בעוד %v: %v", a.ext, trimRetry, err)
			a.trimWait = time.Now().Add(trimRetry)
			if info, derr := cfg.y.dir(a.ext); derr == nil {
				a.files = archiveFiles(info.Files)
			}
			break
		}
		if first == "" {
			first = a.files[0]
		}
		last = a.files[n-1]
		removed += n
		a.files = a.files[n:]
	}
	if removed > 0 {
		log.Printf("שלוחה %s: הגיעה לגבול (ימות המשיח מרשים עד 3,000 קבצים) — נמחקו %d הקבצים הישנים ביותר (%s עד %s).",
			a.ext, removed, first, last)
	}
}

// firstImportFrom: ממתי לייבא בהפעלה הראשונה — 48 השעות האחרונות, ולפחות
// ההודעות האחרונות (min) גם אם הן ישנות יותר. (items: מהישנה לחדשה.)
func firstImportFrom(items []FeedItem, now time.Time, min int) int64 {
	from := now.Add(-firstImportAge).Unix()
	if len(items) == 0 {
		return from
	}
	i := len(items) - min
	if i < 0 {
		i = 0
	}
	if ts := items[i].TS; ts < from {
		from = ts
	}
	return from
}

func (a *archive) add(cfg *config, st *state, it FeedItem, titles map[string]string, now time.Time) error {
	if a.next > archiveLast && a.next < sixFirst {
		// נגמרו המספרים בני 5 ספרות — ממשיכים ב-6 (file_amount_digits=5 = "5 ספרות ומעלה").
		log.Printf("שלוחה %s: המספור עובר ל-6 ספרות (%d).", a.ext, sixFirst)
		a.next = sixFirst
	}
	b := a.next
	if p := st.describePhoto(cfg, it, now); p != "" {
		it.Text = withPhoto(it.Text, p) // "... פורסמה תמונה. בתמונה: ... כתוב בתמונה: ..."
	}
	text := spokenItem(it, titles, cfg.loc, now, a.withName, maxPerFile)
	if err := cfg.y.upload(a.ext, introFile(b), text); err != nil {
		return fmt.Errorf("שליחה לשלוחה %s (%s): %w", a.ext, introFile(b), err)
	}
	cfg.speech.add(a.ext, b, text, false) // קול מוכן מראש (voice.go) — ברקע
	e := &archEntry{key: itemKey(it), base: b, ts: it.TS, class: whenClass(time.Unix(it.TS, 0).In(cfg.loc), now)}
	if cfg.audio {
		e.media, e.audio = st.queueAudio(cfg, it, a.ext, b, now)
	}
	a.entries[e.key] = e
	a.next = b + 2
	if it.TS > a.last {
		a.last = it.TS
	}
	a.dirty = true
	a.adds++
	a.addFile(introFile(b))
	log.Printf("שלוחה %s / %s (%d תווים): %.80s", a.ext, introFile(b), len([]rune(text)), text)
	a.trim(cfg) // בשלוחה מלאה — ההודעה החדשה מוציאה את הישנה ביותר
	return nil
}

// requeue: אחרי הפעלה מחדש — הודעות שהקול שלהן עוד לא הועלה חוזרות לתור.
func (a *archive) requeue(cfg *config, st *state, items []FeedItem, now time.Time) {
	if !cfg.audio {
		return
	}
	win := map[string]FeedItem{}
	for _, it := range items {
		win[itemKey(it)] = it
	}
	for _, e := range a.entries {
		if e.audio != audioPending || e.base < 0 {
			continue
		}
		if it, ok := win[e.key]; ok {
			e.media, e.audio = st.queueAudio(cfg, it, a.ext, e.base, now)
		} else if e.media != "v" || now.Unix()-e.ts >= int64(audioMaxAge/time.Second) || !st.queueVideoByKey(e.key, e.ts, a.ext, e.base) {
			e.audio = audioNo // הודעה קולית שכבר לא בשרת, או ישנה — אין מאיפה / לא מורידים
		}
		a.dirty = true
	}
}

// rerender מעדכן את ניסוח הזמן בהקראות: "בשעה 8 בערב" ← "אתמול בשעה 8 בערב" ←
// "ביום שני בשעה 8 בערב" ← "ב 3 בספטמבר בשעה 8 בערב". מחליפים רק את ביטוי
// הזמן שבפתיחה — כך שגוף ההודעה (ושם הערוץ) נשארים בדיוק כמו שהיו.
func (a *archive) rerender(cfg *config, now time.Time, budget *int) {
	var list []*archEntry
	for _, e := range a.entries {
		if e.base >= 0 && e.class != whenClass(time.Unix(e.ts, 0).In(cfg.loc), now) {
			list = append(list, e)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].base > list[j].base }) // החדשות קודם
	for _, e := range list {
		if *budget <= 0 {
			return
		}
		*budget--
		t := time.Unix(e.ts, 0).In(cfg.loc)
		c := whenClass(t, now)
		giveUp := func(why string, err error) {
			a.rrFails[e.key]++
			if a.rrFails[e.key] >= rerenderTries {
				log.Printf("הערה: לא מעדכן את הזמן ב-%s בשלוחה %s (%s): %v", introFile(e.base), a.ext, why, err)
				e.class, a.dirty = c, true
			}
		}
		old, exists, err := cfg.y.read(a.ext, introFile(e.base))
		if err == nil && !exists {
			err = errors.New("הקובץ לא נמצא בשלוחה")
		}
		if err != nil {
			giveUp("קריאה", err)
			continue
		}
		text, ok := replaceWhen(old, t, e.class, c)
		if !ok {
			e.class, a.dirty = c, true // לא מצאנו את ביטוי הזמן בפתיחה — משאירים כמו שהיא
			continue
		}
		if err := cfg.y.upload(a.ext, introFile(e.base), text); err != nil {
			giveUp("העלאה", err)
			continue
		}
		e.class, a.dirty = c, true
		if cfg.speech != nil {
			// הקול הישן אומר את הניסוח הקודם — נמחק (עד שהחדש מוכן מושמע הטקסט), ונוצר מחדש.
			if a.hasFile(speechFile(e.base)) {
				if err := a.dropFile(cfg, speechFile(e.base)); err != nil {
					log.Printf("הערה: מחיקת הקול הישן %s בשלוחה %s נכשלה: %v", speechFile(e.base), a.ext, err)
				}
			}
			cfg.speech.add(a.ext, e.base, text, true)
		}
	}
}

// replaceWhen מחליף את ביטוי הזמן שבפתיחת ההקראה (לפני הגוף — ב-120 התווים
// הראשונים), מהניסוח הישן לחדש.
func replaceWhen(text string, t time.Time, from, to int) (string, bool) {
	oldW, newW := whenText(t, from)+". ", whenText(t, to)+". "
	head := text
	if r := []rune(text); len(r) > 120 {
		head = string(r[:120])
	}
	i := strings.Index(head, oldW)
	if i < 0 || (i > 0 && !strings.HasSuffix(text[:i], ", ") && !strings.HasSuffix(text[:i], ". ")) {
		return "", false
	}
	return text[:i] + newW + text[i+len(oldW):], true
}

// itemHead: "אלישע ירד, אתמול בשעה 8 בערב. " (withName=false: בלי שם הערוץ).
func itemHead(channel string, t time.Time, class int, titles map[string]string, withName bool) string {
	head := whenText(t, class) + ". "
	if withName {
		head = speakerName(channel, titles) + ", " + head
	}
	return head
}

// archiveFor: הארכיון של שלוחה — נטען פעם אחת בכל הפעלה.
func (st *state) archiveFor(cfg *config, ext, channel string, withName bool, now time.Time) (*archive, error) {
	if a, ok := st.arch[ext]; ok {
		return a, nil
	}
	if coolingDown(st, "archive:"+ext) {
		return nil, fmt.Errorf("ממתין לניסיון חוזר")
	}
	a, err := loadArchive(cfg, ext, channel, withName, now)
	if err != nil {
		st.failAt["archive:"+ext] = time.Now()
		return nil, err
	}
	if channel != "" {
		a.channel = channel
	}
	st.arch[ext] = a
	return a, nil
}

// saveArchives שומר את האינדקסים שהשתנו (פעם אחת בסוף כל סבב).
func (st *state) saveArchives(cfg *config, now time.Time) error {
	exts := make([]string, 0, len(st.arch))
	for ext := range st.arch {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	var first error
	for _, ext := range exts {
		if a := st.arch[ext]; a.dirty {
			if err := a.save(cfg, now); err != nil {
				log.Printf("הערה: %v — ננסה שוב בסבב הבא.", err)
				if first == nil {
					first = err
				}
			}
		}
	}
	return first
}

// reporterExts: ערוץ ← שלוחת כתב (2/1..2/9), קבוע לאורך זמן — נשמר באינדקס של
// כל שלוחה, כך שהארכיון של כתב לא עובר לשלוחה אחרת כשרשימת הערוצים משתנה.
// ערוץ חדש מקבל את המקום שלו לפי הסדר (כמו במבנה הקודם) אם הוא פנוי, ואחרת
// את השלוחה הפנויה הראשונה. אם אינדקס של שלוחה כלשהי לא נקרא — לא מחלקים
// שלוחות חדשות בסבב הזה (ננסה שוב בסבב הבא).
func (st *state) reporterExts(cfg *config) map[string]string {
	if !st.mapped {
		complete := true
		for x := 1; x <= 9; x++ {
			ext := chooseExt + "/" + strconv.Itoa(x)
			txt, exists, err := cfg.y.read(ext, archiveIndex)
			switch {
			case err != nil:
				complete = false
			case exists:
				if ch := parseArchive(txt).channel; ch != "" {
					st.chMap[ch] = ext
				}
			}
		}
		if !complete {
			return st.chMap
		}
		st.mapped = true
	}
	taken := map[string]bool{}
	for _, e := range st.chMap {
		taken[e] = true
	}
	for i, ch := range st.channels {
		if _, ok := st.chMap[ch.Name]; ok {
			continue
		}
		want := ""
		if e := chooseExt + "/" + strconv.Itoa(i+1); i < 9 && !taken[e] {
			want = e
		}
		for x := 1; want == "" && x <= 9; x++ {
			if e := chooseExt + "/" + strconv.Itoa(x); !taken[e] {
				want = e
			}
		}
		if want == "" {
			continue // אין שלוחה פנויה
		}
		st.chMap[ch.Name], taken[want] = want, true
	}
	return st.chMap
}

// ensureTitle: הכותרת של שלוחת כתב (99999.tts — נשמעת ראשונה בכניסה).
func (st *state) ensureTitle(cfg *config, ext, text string) {
	if st.titleSet[ext] == text {
		return
	}
	if err := cfg.y.upload(ext, titleFile, text); err != nil {
		log.Printf("הערה: כותרת שלוחה %s נכשלה: %v", ext, err)
		return
	}
	st.titleSet[ext] = text
}

// ensureDigits: שמות הקבצים בארכיון בני 5 ספרות (file_amount_digits=5). עד
// שההגדרה בטוח במקום — לא עוברים לארכיון (ולא מוחקים את הקבצים הישנים).
func (st *state) ensureDigits(cfg *config, ext string) error {
	if st.digitsSet[ext] {
		return nil
	}
	ini, exists, err := cfg.y.read(ext, "ext.ini")
	if err != nil {
		return fmt.Errorf("קריאת ההגדרות של שלוחה %s: %w%s", ext, err, aclHint(err, "GetTextFile"))
	}
	if !exists {
		ini = "type=playfile"
	}
	updated, changed := setIniValues(ini, [][2]string{{"file_amount_digits", fileDigits}})
	if changed {
		if err := cfg.y.upload(ext, "ext.ini", updated); err != nil {
			return fmt.Errorf("עדכון ההגדרות של שלוחה %s: %w%s", ext, err, aclHint(err, "UploadTextFile"))
		}
		log.Printf("שלוחה %s: file_amount_digits=%s (שמות קבצים בני 5 ספרות).", ext, fileDigits)
	}
	st.digitsSet[ext] = true
	return nil
}
