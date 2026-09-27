package main

import (
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pcmWav: WAV של טלפון (8000 הרץ, מונו) מדגימות.
func pcmWav(samples []int16) []byte {
	raw := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(raw[2*i:], uint16(v))
	}
	return asWav(raw, "audio/L16;rate=8000")
}

func rms(wav []byte, from, to float64) float64 {
	pcm := wav[44:]
	a, b := int(from*8000)*2, int(to*8000)*2
	if b > len(pcm) {
		b = len(pcm)
	}
	var sum float64
	n := 0
	for i := a; i+1 < b; i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		sum += v * v
		n++
	}
	if n == 0 {
		return 0
	}
	return math.Sqrt(sum / float64(n))
}

// ffmpeg אמיתי: שיר בעוצמה מלאה, ירידה, תפריט מעל שיר שקט, דעיכה בסוף.
func TestMixMenuMusic(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("אין ffmpeg")
	}
	dir := t.TempDir()
	song := filepath.Join(dir, "song.mp3")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-af", "volume=0.5", song).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	m := loadMenuMusic(song, "4", "15")
	if m == nil || m.intro != 4 || m.level != 0.15 {
		t.Fatalf("%+v", m)
	}
	voice := pcmWav(make([]int16, 8000*5)) // 5 שניות "דיבור" (שקט — כדי למדוד רק את השיר)
	out, err := mixMenuMusic(voice, m)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := wavSeconds(out)
	if d < 10.5 || d > 11.5 { // 4 פתיחה + 5 תפריט + 2 דעיכה
		t.Fatalf("duration %.2f", d)
	}
	loud, under, end := rms(out, 1, 2.5), rms(out, 5, 8), rms(out, 10.8, 11)
	if loud < 1000 {
		t.Fatalf("intro too quiet: %.0f", loud)
	}
	if r := under / loud; r < 0.08 || r > 0.25 { // השיר קצר מהתפריט — מתנגן שוב, ב-15%
		t.Fatalf("ducking ratio %.3f (loud %.0f under %.0f)", r, loud, under)
	}
	if end > under/2 {
		t.Fatalf("no fade out: end %.0f under %.0f", end, under)
	}
}

func TestLoadMenuMusic(t *testing.T) {
	wd, _ := os.Getwd()
	os.Chdir(t.TempDir())
	t.Cleanup(func() { os.Chdir(wd) })
	if loadMenuMusic("", "", "") != nil {
		t.Fatal("no file → no music")
	}
	os.WriteFile(menuMusicDefault, []byte(strings.Repeat("x", 2000)), 0o644)
	m := loadMenuMusic("", "abc", "500")
	if m == nil || m.intro != 4 || m.level != 0.15 {
		t.Fatalf("defaults: %+v", m)
	}
	if loadMenuMusic("off", "", "") != nil {
		t.Fatal("off")
	}
	m2 := loadMenuMusic("", "6", "")
	if m2.sig == m.sig {
		t.Fatal("settings change must change the signature")
	}
	var none *menuMusic
	if none.musicSig() != menuMusicNone {
		t.Fatal("nil sig")
	}
}

// התפריט הראשי מקבל את השיר (רק הוא), קובץ הסימון נשמר, ובהפעלה הבאה לא יוצרים שוב.
// שיר חדש — התפריט נוצר מחדש פעם אחת.
func TestMenuMusicInWelcome(t *testing.T) {
	g := &fakeTTS{status: map[string]int{}}
	withFakeTTS(t, g)
	oldMix := mixMenuMusic
	t.Cleanup(func() { mixMenuMusic = oldMix })
	mixMenuMusic = func(v []byte, m *menuMusic) ([]byte, error) {
		return append([]byte("MUSIC["+string(m.data)+"]:"), v...), nil
	}
	f := archiveServer(nil, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.speech = newSpeakerVoices("GKEY", "Charon", "Puck", "on")
	cfg.music = &menuMusic{data: []byte("song1"), intro: 4, level: 0.15, sig: "s1"}
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	st.speechTick(&cfg)
	welcome := f.files["ivr2:/M1000.tts"]
	if w := f.files["ivr2:/M1000.wav"]; !strings.Contains(w, "MUSIC[song1]:PHONE:"+welcome) {
		t.Fatalf("welcome wav: %q", w)
	}
	if f.files["ivr2:/menu-music.txt"] != "s1" {
		t.Fatalf("marker: %q", f.files["ivr2:/menu-music.txt"])
	}
	if w := f.files["ivr2:/2/M1000.wav"]; w == "" || strings.Contains(w, "MUSIC") {
		t.Fatalf("chooser menu must not get music: %q", w)
	}
	// הפעלה מחדש, אותו שיר — לא יוצרים שוב
	calls := len(g.calls)
	cfg.speech = newSpeakerVoices("GKEY", "Charon", "Puck", "on")
	st = &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	st.speechTick(&cfg)
	if len(g.calls) != calls {
		t.Fatalf("regenerated: %v", g.calls[calls:])
	}
	// שיר חדש — נוצר מחדש
	cfg.speech = newSpeakerVoices("GKEY", "Charon", "Puck", "on")
	cfg.music = &menuMusic{data: []byte("song2"), sig: "s2"}
	st = &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	st.speechTick(&cfg)
	if w := f.files["ivr2:/M1000.wav"]; !strings.Contains(w, "MUSIC[song2]:") || f.files["ivr2:/menu-music.txt"] != "s2" {
		t.Fatalf("new song: %q marker %q", w, f.files["ivr2:/menu-music.txt"])
	}
	// השיר הוסר — נוצר מחדש בלי מוזיקה
	cfg.speech = newSpeakerVoices("GKEY", "Charon", "Puck", "on")
	cfg.music = nil
	st = &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	st.speechTick(&cfg)
	if w := f.files["ivr2:/M1000.wav"]; strings.Contains(w, "MUSIC") || w == "" || f.files["ivr2:/menu-music.txt"] != menuMusicNone {
		t.Fatalf("removed: %q marker %q", w, f.files["ivr2:/menu-music.txt"])
	}
}
