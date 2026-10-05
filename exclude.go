package main

// ערוצים שהוצאו מהקו (EXCLUDE_CHANNELS): ההודעות שלהם לא נכנסות (syncOnce מסנן
// אותן), ומה שכבר עלה מהם נמחק — משלוחה 1, ושלוחת הכתב שלהם מתרוקנת ומתפנה
// לערוץ אחר. בערוץ חי עצמו הם נשארים.

import (
	"log"
	"sort"
	"strings"
	"time"
)

// channelOfKey: "SamariaUpdates/340" → "SamariaUpdates".
func channelOfKey(key string) string {
	if i := strings.LastIndex(key, "/"); i > 0 {
		return key[:i]
	}
	return key
}

// xLineStart: לפני שטוויטר נכנס לקו (05.10.2026, 13:23) פחות 6 שעות. בהפעלה
// הראשונה נכנסו לשלוחה 1 גם ציוצים מהשבועות האחרונים, והם נשמעו ראשונים —
// fixXBacklog מוחק אותם משלוחה 1 (בשלוחות הכתבים הם נשארים).
var xLineStart = time.Date(2026, 10, 5, 4, 23, 0, 0, time.UTC).Unix()

// fixXBacklog: פעם אחת בכל הפעלה, אחרי שהארכיון של שלוחה 1 נטען.
func (st *state) fixXBacklog(cfg *config) {
	if st.xFixed {
		return
	}
	a := st.arch[cfg.ext]
	if a == nil {
		return
	}
	err := a.purgeWhere(cfg, func(e *archEntry) bool {
		return isXChannel(channelOfKey(e.key)) && e.ts < xLineStart
	}, "ישנות מטוויטר שנכנסו בהפעלה הראשונה של טוויטר")
	if err != nil {
		log.Printf("הערה: מחיקת הציוצים הישנים משלוחה %s נכשלה — ננסה שוב: %v", cfg.ext, err)
		return
	}
	st.xFixed = true
}

// fixXOrder: שלוחות הכתבים של טוויטר נבנו לפני המספור עם הרווחים, והריטוויטים
// נכנסו בהן אחרי הציוצים, לא לפי השעה. פעם אחת: מרוקנים אותן (השלוחה נשארת של
// אותו כתב), והן נבנות מחדש בייבוא ראשון — מהישן לחדש, לפי השעה.
func (st *state) fixXOrder(cfg *config) {
	if st.xOrdered || !st.mapped {
		return
	}
	done := true
	for ch, ext := range st.chMap {
		if !isXChannel(ch) {
			continue
		}
		a := st.arch[ext]
		if a == nil {
			done = false // עוד לא נטען
			continue
		}
		if !a.oldLayout {
			continue
		}
		st.aw.drop(func(key string) bool { return channelOfKey(key) == ch })
		if err := st.clearReporter(cfg, ch, ext); err != nil {
			log.Printf("הערה: סידור מחדש של שלוחת הכתב %s (%s) נכשל — ננסה שוב: %v", ext, ch, err)
			done = false
			continue
		}
		st.chMap[ch] = ext // אותה שלוחה — נבנית מחדש לפי השעה
		log.Printf("שלוחה %s (%s): נבנית מחדש לפי השעה.", ext, ch)
	}
	st.xOrdered = done
}

// purgeExcluded: פעם אחת בכל הפעלה (אחרי שהכול נמחק — אין מה למחוק).
func (st *state) purgeExcluded(cfg *config) {
	if len(cfg.exclude) == 0 || st.purged {
		return
	}
	done := true
	st.aw.drop(func(key string) bool { return cfg.excluded(channelOfKey(key)) })
	if a := st.arch[cfg.ext]; a != nil {
		if err := a.purge(cfg, cfg.excluded); err != nil {
			log.Printf("הערה: מחיקת הודעות של ערוצים שהוצאו מהקו משלוחה %s נכשלה — ננסה שוב: %v", cfg.ext, err)
			done = false
		}
	} else {
		done = false // הארכיון של שלוחה 1 עוד לא נטען
	}
	if cfg.channelExts {
		if !st.mapped {
			done = false
		}
		var gone []string
		for ch := range st.chMap {
			if cfg.excluded(ch) {
				gone = append(gone, ch)
			}
		}
		sort.Strings(gone)
		for _, ch := range gone {
			if err := st.clearReporter(cfg, ch, st.chMap[ch]); err != nil {
				log.Printf("הערה: פינוי שלוחת הכתב %s (%s) נכשל — ננסה שוב: %v", st.chMap[ch], ch, err)
				done = false
			}
		}
	}
	st.purged = done
}

// purge מוחק מהשלוחה את ההודעות (ההקראה והקול) של ערוצים שהוצאו מהקו. ההודעות
// נשארות באינדקס כמדולגות, כך שלא ייכנסו שוב.
func (a *archive) purge(cfg *config, gone func(channel string) bool) error {
	return a.purgeWhere(cfg, func(e *archEntry) bool { return gone(channelOfKey(e.key)) }, "של ערוצים שהוצאו מהקו")
}

// purgeWhere מוחק מהשלוחה הודעות (ההקראה והקול) לפי תנאי. הן נשארות באינדקס
// כמדולגות, כך שלא ייכנסו שוב.
func (a *archive) purgeWhere(cfg *config, match func(e *archEntry) bool, what string) error {
	var victims []*archEntry
	for _, e := range a.entries {
		if e.base >= 0 && match(e) {
			victims = append(victims, e)
		}
	}
	if len(victims) == 0 {
		return nil
	}
	info, err := cfg.y.dir(a.ext)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, n := range info.Files {
		have[strings.ToLower(n)] = true
	}
	del := map[string]bool{}
	var paths []string
	for _, e := range victims {
		for _, n := range []string{audioFile(e.base), introFile(e.base), speechFile(e.base), fullFile(e.base)} {
			if have[n] {
				del[n] = true
				paths = append(paths, ivrPath(a.ext, n))
			}
		}
	}
	for i := 0; i < len(paths); i += 50 {
		if err := cfg.y.remove(paths[i:min(i+50, len(paths))]); err != nil {
			return err
		}
	}
	for _, e := range victims {
		e.base, e.audio = -1, audioNo
	}
	kept := a.files[:0]
	for _, n := range a.files {
		if !del[strings.ToLower(n)] {
			kept = append(kept, n)
		}
	}
	a.files, a.dirty = kept, true
	log.Printf("שלוחה %s: נמחקו %d הודעות %s (%d קבצים).", a.ext, len(victims), what, len(paths))
	return nil
}

// clearReporter מרוקן את שלוחת הכתב של ערוץ שהוצא מהקו: ההודעות, הכותרת והאינדקס.
// השלוחה נשארת שלוחת השמעה ריקה של הגשר — ותתפנה לערוץ חדש, אם יתווסף.
func (st *state) clearReporter(cfg *config, channel, ext string) error {
	info, err := cfg.y.dir(ext)
	if err != nil {
		return err
	}
	var paths []string
	if info.Exists {
		for _, n := range info.Files {
			if fileNum(n) >= 0 || isOldTTS(n) { // הודעות, קול, הכותרת (99999)
				paths = append(paths, ivrPath(ext, n))
			}
		}
		if _, exists, err := cfg.y.read(ext, archiveIndex); err != nil {
			return err
		} else if exists {
			paths = append(paths, ivrPath(ext, archiveIndex))
		}
	}
	for i := 0; i < len(paths); i += 50 {
		if err := cfg.y.remove(paths[i:min(i+50, len(paths))]); err != nil {
			return err
		}
	}
	delete(st.chMap, channel)
	delete(st.arch, ext)
	delete(st.chExt, channel)
	delete(st.titleSet, ext)
	log.Printf("שלוחה %s (%s): הערוץ הוצא מהקו — נמחקו %d קבצים, והשלוחה פנויה.", ext, channel, len(paths))
	return nil
}

// drop מוציא מהתור עבודות שעוד לא התחילו (למשל של ערוץ שהוצא מהקו).
func (w *audioWorker) drop(match func(key string) bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	kept := w.queue[:0]
	n := 0
	for _, j := range w.queue {
		if !j.running && match(j.key) {
			n++
			continue
		}
		kept = append(kept, j)
	}
	w.queue = kept
	if n > 0 {
		log.Printf("הוצאו מהתור %d קבצי קול של ערוצים שהוצאו מהקו.", n)
	}
}
