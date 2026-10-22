package main

// הנחיית סגנון לקריין במודלי הקול החדשים של Google.
//
// במודלים gemini-3.8-flash-tts / 3.8-flash-lite-tts / 3.1-flash-tts-preview השדה
// systemInstruction חסום ("Developer instruction is not enabled for this model").
// לפי התיעוד של Google, הדרך לתת להם הנחיה היא ממשק Interactions: הטקסט נשלח
// כתמליל להקראה מילה במילה, ולצדו (לא בתוכו) שדה style — משפט קצר על הטון
// והקצב. הוא לא מוקרא. Google ממליצים על הנחיה קצרה: הנחיות ארוכות גורמות
// לקול "לזוז".
//
// אם הדרך הזו לא עובדת במודל (תשובה 400/404, או תשובה בלי קול) — נזכרים בזה
// ועוברים לדרך הרגילה (generateContent, בלי הנחיות) לכל אורך ההפעלה. ככה במצב
// הגרוע ביותר הקו עובד בדיוק כמו קודם. בלוג: "מקריא עם הנחיית סגנון" כשזה
// עובד, ו"הקראה עם הנחיית סגנון נכשלה" כשלא (זה מופיע גם בדוח המצב).
//
// לשינוי ההנחיה בלי לגעת בקוד: SPEECH_STYLE ב-bridge.yml.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// speechStyle: ההנחיה לקריין. באנגלית — כך בכל הדוגמאות של Google, והמודל
// מבין אותה טוב יותר; "Israeli Hebrew" — כדי שלא ינחש שפה/מבטא בטקסט קצר.
var speechStyle = "Native Israeli Hebrew speaker. Professional radio news anchor reading a news bulletin: clear, confident, neutral and restrained, fairly fast pace. Read every word exactly as written, never act out sounds or emotions."

// speechStyleRevision: הגרסה של ממשק Interactions (כמו בדוגמאות של Google).
const speechStyleRevision = "2026-05-20"

var errStyleNoAudio = errors.New("בלי קול בתשובה")

// styleBroken: הדרך עם ההנחיה לא מתאימה למודל הזה (ולא סתם מכסה / עומס / רשת).
func styleBroken(status int, err error) bool {
	if err == nil {
		return false
	}
	return status == http.StatusBadRequest || status == http.StatusNotFound || errors.Is(err, errStyleNoAudio)
}

// callStyle: טקסט → WAV דרך Interactions, עם הנחיית הסגנון.
func (s *speaker) callStyle(key, model, text, voice string) ([]byte, int, error) {
	req := map[string]any{
		"model": model,
		"input": []any{map[string]any{
			"type": "user_input",
			"content": []any{map[string]any{
				"type":        "text",
				"text":        text,
				"annotations": []any{map[string]any{"type": "speech_metadata", "style": speechStyle}},
			}},
		}},
		"response_format":   map[string]any{"type": "audio"},
		"generation_config": map[string]any{"speech_config": []any{map[string]any{"voice": voice}}},
	}
	body, _ := json.Marshal(req)
	raw, status, err := s.post(speechStyleEndpoint, key, model, body, map[string]string{"Api-Revision": speechStyleRevision})
	if err != nil {
		return nil, status, err
	}
	data, mime, err := styleAudio(raw)
	if err != nil {
		return nil, status, fmt.Errorf("%s: %w", model, err)
	}
	return asWav(data, mime), status, nil
}

// styleAudio: הקול מהתשובה — steps[].content[] מסוג audio (האחרון), או
// output_audio / outputs (כמו ב-SDK), למקרה שהמבנה שונה מעט.
func styleAudio(raw []byte) ([]byte, string, error) {
	type block struct {
		Type     string `json:"type"`
		Data     string `json:"data"`
		MimeType string `json:"mime_type"`
	}
	var r struct {
		Steps []struct {
			Type    string  `json:"type"`
			Content []block `json:"content"`
		} `json:"steps"`
		Outputs     []block `json:"outputs"`
		OutputAudio *block  `json:"output_audio"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", fmt.Errorf("תשובה לא תקינה: %v", err)
	}
	var last *block
	for i := range r.Steps {
		for j := range r.Steps[i].Content {
			if b := &r.Steps[i].Content[j]; b.Type == "audio" && b.Data != "" {
				last = b
			}
		}
	}
	for i := range r.Outputs {
		if b := &r.Outputs[i]; last == nil && b.Type == "audio" && b.Data != "" {
			last = b
		}
	}
	if last == nil && r.OutputAudio != nil && r.OutputAudio.Data != "" {
		last = r.OutputAudio
	}
	if last == nil {
		snip := string(raw)
		if len(snip) > 160 {
			snip = snip[:160]
		}
		return nil, "", fmt.Errorf("%w (%s)", errStyleNoAudio, snip)
	}
	data, err := base64.StdEncoding.DecodeString(last.Data)
	if err != nil {
		return nil, "", err
	}
	return data, last.MimeType, nil
}
