package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestVoiceNames(t *testing.T) {
	cases := map[string]string{
		"אלישע ירד, בשעה 8 בערב.":  "אֱלִישָׁע יֵרֵד, בשעה 8 בערב.",
		"עדכוני אריאל דנינו.":      "עדכוני אֲרִיאֵל דָּנִינוֹ.",
		"לאלישע ירד ולמאיר אטינגר": "לאֱלִישָׁע יֵרֵד ולמֵאִיר אֶטִּינְגֶר",
		"אלישע ירדן":               "אלישע ירדן", // שם אחר — לא נוגעים
		"איילת לאש ואליה אביב":     "אַיֶּלֶת לָאשׁ ואֵלִיָּה אָבִיב",
		"הודעה בלי שמות":           "הודעה בלי שמות",
	}
	for in, want := range cases {
		if got := voiceText(in); got != want {
			t.Errorf("%q → %q (want %q)", in, got, want)
		}
	}
	// פעם שנייה לא מנקדת שוב
	if once := voiceText("אלישע ירד"); voiceText(once) != once {
		t.Fatal("double niqqud")
	}
}

func TestCleanLaugh(t *testing.T) {
	cases := map[string]string{
		"חחחח איזה יופי":      "איזה יופי",
		"הוא אמר *פיהוק* חחח": "הוא אמר פיהוק",
		"חהחה ממש":            "ממש",
		"החח הוא לא מילה":     "החח הוא לא מילה",
		"שמח, חחח, באמת":      "שמח, באמת",
	}
	for in, want := range cases {
		if got := cleanForSpeech(in); got != want {
			t.Errorf("%q → %q (want %q)", in, got, want)
		}
	}
}

// ההנחיות לקריין נשלחות בשדה נפרד; מודל שדוחה אותן — ממשיכים בלעדיהן.
func TestAnchorInstructions(t *testing.T) {
	for _, reject := range []bool{false, true} {
		var mu sync.Mutex
		var sys []bool
		var texts []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Contents []struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"contents"`
				SystemInstruction *struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"systemInstruction"`
			}
			json.Unmarshal(raw, &req)
			has := req.SystemInstruction != nil && strings.Contains(req.SystemInstruction.Parts[0].Text, "קריין חדשות")
			sys = append(sys, has)
			if reject && has {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"message":"Invalid JSON payload: Unknown name \"systemInstruction\""}}`)
				return
			}
			texts = append(texts, req.Contents[0].Parts[0].Text)
			json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
				map[string]any{"inlineData": map[string]any{"mimeType": "audio/L16;codec=pcm;rate=24000", "data": "AAAA"}}}}}}})
		}))
		old := speechEndpoint
		speechEndpoint = srv.URL + "/%s:generateContent"
		s := newSpeaker("GKEY", "", "on")
		for i := 0; i < 2; i++ {
			if _, _, err := s.synthesize("עדכוני אלישע ירד.", ""); err != nil {
				t.Fatal(err)
			}
		}
		speechEndpoint = old
		srv.Close()
		if !reject && (len(sys) != 2 || !sys[0] || !sys[1]) {
			t.Fatalf("instructions not sent: %v", sys)
		}
		// דחייה: ניסיון אחד עם ההנחיות, ומשם בלעדיהן — והשפה נשארת
		if reject && (len(sys) != 3 || !sys[0] || sys[1] || sys[2] || !s.noSys[speechModels[0]] || s.noLang[speechModels[0]]) {
			t.Fatalf("reject: %v noSys=%v noLang=%v", sys, s.noSys, s.noLang)
		}
		if texts[0] != "עדכוני אֱלִישָׁע יֵרֵד." || strings.Contains(texts[0], "קריין") {
			t.Fatalf("text: %q", texts[0])
		}
	}
}

func TestStoneThrowing(t *testing.T) {
	for in, want := range map[string]string{
		`ז"א לעבר רכב ישראלי`: "זריקת אבנים לעבר רכב ישראלי",
		`דיווח על ז״א בכביש`:  "דיווח על זריקת אבנים בכביש",
		`בעקבות הז"א`:         "בעקבות הזריקת אבנים",
	} {
		if got := cleanForSpeech(in); got != want {
			t.Errorf("%q → %q (want %q)", in, got, want)
		}
	}
}
