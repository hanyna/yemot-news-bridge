package main

// מוזיקה בתפריט הראשי: המתקשר שומע קודם שיר (כמה שניות בעוצמה מלאה), ואז
// העוצמה של השיר יורדת והתפריט מתחיל — עם השיר בשקט ברקע. בסוף התפריט השיר
// דועך.
//
// השיר: קובץ menu-music.mp3 בתיקייה הראשית של המאגר (מעלים אותו בגיטהאב:
// Add file ← Upload files), או MENU_MUSIC ב-bridge.yml — שם קובץ אחר או קישור
// ישיר לקובץ שמע. MENU_MUSIC: "off" = בלי מוזיקה.
//
// עובד רק עם הקול המוכן (SPEECH=on): השיר מתערבב לתוך קובץ הקול של התפריט
// (M1000.wav). כשאין קול מוכן (מכסה / תקלה) — התפריט מושמע כטקסט, בלי שיר.
//
// כדי לא ליצור את קול התפריט מחדש בכל הפעלה, הגשר שומר בשלוחה הראשית קובץ
// סימון (menu-music.txt) עם "חתימה" של השיר וההגדרות. החלפת שיר / עוצמה /
// אורך הפתיחה — התפריט נוצר מחדש פעם אחת.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	menuMusicDefault = "menu-music.mp3" // בתיקייה הראשית של המאגר
	menuMusicMarker  = "menu-music.txt" // בשלוחה הראשית: החתימה של השיר שבתפריט
	menuMusicNone    = "none"
	menuMusicMaxSize = 40 << 20
	menuMusicTail    = 2.0 // כמה שניות השיר ממשיך (ודועך) אחרי סוף התפריט
)

type menuMusic struct {
	data  []byte
	name  string
	intro float64 // שניות של שיר בעוצמה מלאה לפני התפריט
	level float64 // עוצמת השיר מתחת לדיבור (0..1)
	sig   string
}

// loadMenuMusic: nil = בלי מוזיקה (כבוי, אין קובץ, או תקלה — נרשם בלוג).
func loadMenuMusic(src, intro, level string) *menuMusic {
	src = strings.TrimSpace(src)
	if strings.EqualFold(src, "off") {
		return nil
	}
	explicit := src != ""
	if !explicit {
		src = menuMusicDefault
	}
	var data []byte
	var err error
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		data, err = downloadMusic(src)
	} else {
		data, err = os.ReadFile(src)
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return nil // אין שיר במאגר — תפריט רגיל
		}
	}
	if err == nil && len(data) > menuMusicMaxSize {
		err = errors.New("הקובץ גדול מדי (עד 40MB)")
	}
	if err == nil && len(data) < 1000 {
		err = errors.New("הקובץ ריק")
	}
	if err != nil {
		log.Printf("הערה: מוזיקה בתפריט — לא הצלחתי לקרוא את %s: %v. התפריט בלי מוזיקה.", shortURL(src), err)
		return nil
	}
	m := &menuMusic{data: data, name: shortURL(src),
		intro: numEnv(intro, 4, 0, 30), level: numEnv(level, 15, 0, 100) / 100}
	h := sha256.Sum256(data)
	m.sig = fmt.Sprintf("%x|%g|%g", h[:6], m.intro, m.level)
	return m
}

func numEnv(s string, def, lo, hi float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < lo || v > hi {
		return def
	}
	return v
}

func downloadMusic(u string) ([]byte, error) {
	c := &http.Client{Timeout: 2 * time.Minute}
	resp, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, menuMusicMaxSize+1))
}

// musicSig: מה אמור להיות בקובץ הסימון.
func (m *menuMusic) musicSig() string {
	if m == nil {
		return menuMusicNone
	}
	return m.sig
}

// isWelcomeWav: קובץ הקול של התפריט הראשי — היחיד שמקבל מוזיקה.
func isWelcomeWav(t speechTarget) bool { return t.ext == "" && t.file == "M1000.wav" }

// wavSeconds: אורך קובץ WAV (לפי כותרת ה-fmt וה-data).
func wavSeconds(b []byte) (float64, error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0, errors.New("לא WAV")
	}
	var byteRate uint32
	for p := 12; p+8 <= len(b); {
		id, size := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:p+8]))
		p += 8
		switch id {
		case "fmt ":
			if p+12 <= len(b) {
				byteRate = binary.LittleEndian.Uint32(b[p+8 : p+12])
			}
		case "data":
			if byteRate == 0 {
				return 0, errors.New("WAV בלי fmt")
			}
			if size > len(b)-p || size <= 0 {
				size = len(b) - p
			}
			return float64(size) / float64(byteRate), nil
		}
		p += size + size%2
	}
	return 0, errors.New("WAV בלי data")
}

// mixMenuMusic: שיר בעוצמה מלאה (intro שניות) ← העוצמה יורדת ← התפריט מעל
// השיר השקט ← השיר דועך אחרי התפריט. מקבל ומחזיר WAV של טלפון (8000 הרץ,
// מונו). שיר קצר מהתפריט — מתנגן שוב מההתחלה. משתנה — לבדיקות.
var mixMenuMusic = func(voice []byte, m *menuMusic) ([]byte, error) {
	dur, err := wavSeconds(voice)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "menumusic")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	vin, mus, out := filepath.Join(dir, "voice.wav"), filepath.Join(dir, "music"), filepath.Join(dir, "out.wav")
	if err := os.WriteFile(vin, voice, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(mus, m.data, 0o644); err != nil {
		return nil, err
	}
	intro, lvl := m.intro, m.level
	total := intro + dur + menuMusicTail
	duck := 1.0 // שנייה של ירידה הדרגתית בעוצמה, לפני שהדיבור מתחיל
	if intro < duck {
		duck = intro
	}
	var vol string
	if duck > 0 {
		vol = fmt.Sprintf("if(lt(t\\,%.3f)\\,1\\,if(lt(t\\,%.3f)\\,1-(1-%.3f)*(t-%.3f)/%.3f\\,%.3f))",
			intro-duck, intro, lvl, intro-duck, duck, lvl)
	} else {
		vol = fmt.Sprintf("%.3f", lvl)
	}
	filter := fmt.Sprintf(
		"[1:a]aformat=channel_layouts=mono,"+
			"silenceremove=start_periods=1:start_threshold=-50dB,"+
			"loudnorm=I=-20:TP=-3,aresample=8000,"+
			"atrim=0:%.3f,asetpts=N/SR/TB,"+
			"volume='%s':eval=frame,"+
			"afade=t=in:d=0.3,afade=t=out:st=%.3f:d=%.3f[m];"+
			"[0:a]aformat=channel_layouts=mono,aresample=8000,adelay=%d:all=1,apad=whole_dur=%.3f[v];"+
			"[v][m]amix=inputs=2:duration=first:normalize=0,alimiter=limit=0.95[out]",
		total, vol, intro+dur, menuMusicTail, int(intro*1000), total)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", vin, "-stream_loop", "-1", "-i", mus,
		"-filter_complex", filter, "-map", "[out]", "-t", fmt.Sprintf("%.3f", total),
		"-ar", "8000", "-ac", "1", "-sample_fmt", "s16", out)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %.300s", err, strings.TrimSpace(buf.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if got, err := wavSeconds(data); err != nil || got < dur {
		return nil, fmt.Errorf("התוצאה קצרה מהתפריט (%.1f מתוך %.1f שניות)", got, dur)
	}
	return data, nil
}
