package main

// פענוח כותרות עברית מ-yt-dlp: בווינדוס yt-dlp (פייתון) פולט ל-pipe בקידוד
// המערכת (cp1255, לפעמים cp862 הישן) ולא UTF-8, ולכן העברית הגיעה שבורה
// וההתאמה ל"התוכנית המלאה" נכשלה. decodeTitle מזהה את הקידוד ומחזיר UTF-8 תקין.

import (
	"strings"
	"unicode/utf8"
)

// hasHebrew: יש בטקסט (UTF-8) לפחות אות עברית אחת.
func hasHebrew(s string) bool {
	for _, r := range s {
		if r >= 0x05D0 && r <= 0x05EA {
			return true
		}
	}
	return false
}

// mapHebrewBytes: ממפה בייטים בטווח [lo,lo+26] לאותיות א–ת (05D0–05EA),
// ASCII (<0x80) עובר כמו שהוא, שאר הבייטים מושמטים. מחזיר "" אם לא נוצרה עברית.
func mapHebrewBytes(b []byte, lo byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c) // ASCII: ספרות, רווח, '|', '.', 'I'
		case c >= lo && c <= lo+26:
			sb.WriteRune(rune(0x05D0 + int(c-lo))) // א..ת (כולל סופיות)
		}
	}
	out := sb.String()
	if !hasHebrew(out) {
		return "" // לא הקידוד הזה
	}
	return out
}

// decodeTitle מחזיר את הכותרת כ-UTF-8 תקין, ואת שם הקידוד שזוהה.
//   - כבר UTF-8 עם עברית → כמו שהוא ("utf8").
//   - אחרת מנסה cp1255 (אותיות ב-0xE0..0xFA) ואז cp862 (0x80..0x9A).
//   - אם כלום לא נתן עברית → הטקסט המקורי ("raw").
func decodeTitle(s string) (string, string) {
	if utf8.ValidString(s) && hasHebrew(s) {
		return s, "utf8"
	}
	b := []byte(s)
	if d := mapHebrewBytes(b, 0xE0); d != "" {
		return d, "cp1255"
	}
	if d := mapHebrewBytes(b, 0x80); d != "" {
		return d, "cp862"
	}
	return s, "raw"
}
