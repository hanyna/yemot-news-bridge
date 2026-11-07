package main

// שם הכתב בקול — קטע קבוע.
//
// הקול של Google "מתקן" לפעמים שם לא מוכר בתוך הודעה ארוכה ואומר שם אחר
// ("אלישע יוברג" במקום "אלישע ירד"). לכן שם הכתב שבפתיחת כל הודעה (שלוחה 1)
// לא נוצר יחד עם ההודעה: הוא נוצר פעם אחת לבדו — טקסט קצר ומנוקד, שבו המודל
// לא ממציא — נשמר, ומודבק בתחילת הקול של כל הודעה. ההודעה עצמה נוצרת בלי
// השם ("בשעה 7 בערב. ..."). אם משהו בדרך נכשל — ההודעה נוצרת כמו קודם, עם השם
// בתוך הטקסט.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	nameGap      = 350 * time.Millisecond // הפסקה בין השם להודעה
	nameMaxRunes = 60                     // פתיחה ארוכה מזה — לא שם
	nameTries    = 2
)

// nameMinSec: קטע קצר מזה — לא נאמר כלום. משתנה — לבדיקות.
var nameMinSec = 0.35

// splitNamed: "אלישע ירד, בשעה 7 בערב. הודעה" → "אלישע ירד", "בשעה 7 בערב. הודעה".
func splitNamed(text string) (name, rest string, ok bool) {
	i := strings.Index(text, ", ")
	if i <= 0 {
		return "", "", false
	}
	name, rest = strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+2:])
	if name == "" || rest == "" || len([]rune(name)) > nameMaxRunes || !strings.ContainsAny(name, "אבגדהוזחטיכלמנסעפצקרשת") {
		return "", "", false
	}
	return name, rest, true
}

// nameClip: הקול של שם הכתב (WAV כפי ש-Gemini מחזיר). נוצר פעם אחת לכל שם
// וקול, ונשמר להפעלה כולה.
func (s *speaker) nameClip(name, voice string) ([]byte, error) {
	k := voice + "|" + name
	s.mu.Lock()
	clip, ok := s.names[k]
	fails := s.nameFails[k]
	s.mu.Unlock()
	if ok {
		return clip, nil
	}
	if fails >= nameTries {
		return nil, errors.New("השם לא נוצר")
	}
	// נקודה בסוף — שהשם ייאמר שלם, בנגינה של סוף משפט.
	clip, _, err := s.synthesize(name+".", voice)
	if err == nil {
		sec, e := wavSeconds(clip)
		switch {
		case e != nil:
			err = e
		case sec < nameMinSec:
			err = fmt.Errorf("הקטע קצר מדי (%.2f שניות)", sec)
		case sec > 1.5+0.3*float64(len([]rune(name))):
			err = fmt.Errorf("הקטע ארוך מדי (%.1f שניות)", sec)
		}
	}
	var busy *speechBusyErr
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if !errors.As(err, &busy) {
			s.nameFails[k]++
		}
		return nil, err
	}
	s.names[k] = clip
	log.Printf("קול: השם %q נוצר פעם אחת (%.1f שניות) ויודבק לכל ההודעות של הכתב.", name, mustSeconds(clip))
	return clip, nil
}

func mustSeconds(wav []byte) float64 {
	sec, _ := wavSeconds(wav)
	return sec
}

// render: הקול של עבודה — שם הכתב כקטע קבוע + ההודעה, או (כשאי אפשר) הכול יחד.
func (s *speaker) render(job speechJob) ([]byte, string, error) {
	if job.named {
		if name, rest, ok := splitNamed(job.text); ok {
			clip, err := s.nameClip(name, job.voice)
			var busy *speechBusyErr
			switch {
			case errors.As(err, &busy):
				return nil, "", err // מכסה — גם ההודעה לא תיווצר עכשיו
			case err != nil:
				s.warnOnce("name:"+name, fmt.Sprintf("הערה: הקול של השם %q לא נוצר לבד (%v) — השם נאמר בתוך ההודעה כמו קודם.", name, err))
			default:
				body, model, err := s.synthesize(rest, job.voice)
				if err != nil {
					return nil, model, err
				}
				if joined, e := joinWav(clip, body, nameGap); e == nil {
					return joined, model, nil
				} else {
					s.warnOnce("join", "הערה: חיבור השם להודעה נכשל ("+e.Error()+") — השם נאמר בתוך ההודעה כמו קודם.")
				}
			}
		}
	}
	return s.synthesize(job.text, job.voice)
}

type pcmInfo struct {
	channels, bits int
	rate           int
	data           []byte
}

// wavPCM: הדגימות וה-fmt של קובץ WAV (PCM בלבד).
func wavPCM(b []byte) (pcmInfo, error) {
	var p pcmInfo
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return p, errors.New("לא WAV")
	}
	for i := 12; i+8 <= len(b); {
		id, size := string(b[i:i+4]), int(binary.LittleEndian.Uint32(b[i+4:i+8]))
		i += 8
		if size < 0 || size > len(b)-i {
			size = len(b) - i
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return p, errors.New("fmt קצר")
			}
			if binary.LittleEndian.Uint16(b[i:i+2]) != 1 {
				return p, errors.New("לא PCM")
			}
			p.channels = int(binary.LittleEndian.Uint16(b[i+2 : i+4]))
			p.rate = int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
			p.bits = int(binary.LittleEndian.Uint16(b[i+14 : i+16]))
		case "data":
			if p.rate == 0 {
				return p, errors.New("WAV בלי fmt")
			}
			p.data = b[i : i+size]
			return p, nil
		}
		i += size + size%2
	}
	return p, errors.New("WAV בלי data")
}

// joinWav: שני קבצי WAV באותו פורמט → קובץ אחד: הראשון, שקט, השני.
func joinWav(a, b []byte, gap time.Duration) ([]byte, error) {
	pa, err := wavPCM(a)
	if err != nil {
		return nil, fmt.Errorf("שם: %v", err)
	}
	pb, err := wavPCM(b)
	if err != nil {
		return nil, fmt.Errorf("הודעה: %v", err)
	}
	if pa.rate != pb.rate || pa.channels != pb.channels || pa.bits != pb.bits {
		return nil, fmt.Errorf("פורמט שונה (%d/%d/%d לעומת %d/%d/%d)", pa.rate, pa.channels, pa.bits, pb.rate, pb.channels, pb.bits)
	}
	frame := pa.channels * pa.bits / 8
	silence := make([]byte, int(float64(pa.rate)*gap.Seconds())*frame)
	n := len(pa.data) + len(silence) + len(pb.data)
	var out bytes.Buffer
	w := func(v any) { binary.Write(&out, binary.LittleEndian, v) }
	out.WriteString("RIFF")
	w(uint32(36 + n))
	out.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(pa.channels))
	w(uint32(pa.rate))
	w(uint32(pa.rate * frame))
	w(uint16(frame))
	w(uint16(pa.bits))
	out.WriteString("data")
	w(uint32(n))
	out.Write(pa.data)
	out.Write(silence)
	out.Write(pb.data)
	return out.Bytes(), nil
}
