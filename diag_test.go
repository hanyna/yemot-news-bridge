//go:build diag

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiagVoices(t *testing.T) {
	text := "ברוכים הבאים לקו עדכוני ארץ ישראל. לכל העדכונים, הקישו 1. לבחירת כתב מסוים, הקישו 2. לפודקאסטים, הקישו 3. להמשך ההאזנה מהמקום שהפסקתם, הקישו 5. להשארת הודעה למנהל המערכת, הקישו 6. לצינתוקים ותזכורות, הקישו 8."
	s := newSpeaker(os.Getenv("GEMINI_API_KEY"), "Charon", "on")
	os.MkdirAll("samples", 0o755)
	var rep strings.Builder
	for _, v := range []string{"Puck", "Sadachbia", "Achird", "Algieba", "Orus"} {
		wav, model, err := s.synthesize(text, v)
		if err == nil {
			wav, err = prepareSpeech(wav)
		}
		if err != nil {
			fmt.Fprintf(&rep, "%s: %v\n", v, err)
			continue
		}
		os.WriteFile("samples/"+v+".wav", wav, 0o644)
		fmt.Fprintf(&rep, "%s: %s %.1fs\n", v, model, float64(len(wav)-44)/16000)
		time.Sleep(3 * time.Second)
	}
	fmt.Printf("::notice title=voices::%s\n", strings.ReplaceAll(rep.String(), "\n", "%0A"))
}
