package main

import "testing"

// encodeHeb מקודד מחרוזת UTF-8 לבייטים בקידוד עברי ישן (lo = בסיס האות א):
// cp1255 → 0xE0, cp862 → 0x80. ASCII נשאר. לבדיקות בלבד.
func encodeHeb(s string, lo byte) string {
	var b []byte
	for _, r := range s {
		switch {
		case r < 0x80:
			b = append(b, byte(r))
		case r >= 0x05D0 && r <= 0x05EA:
			b = append(b, lo+byte(r-0x05D0))
		default:
			b = append(b, '?')
		}
	}
	return string(b)
}

func TestDecodeTitle(t *testing.T) {
	orig := "הפטריוטים עם ינון מגל | 12.10.2026 | התוכנית המלאה"

	// כבר UTF-8 — חוזר כמו שהוא.
	if got, how := decodeTitle(orig); got != orig || how != "utf8" {
		t.Errorf("utf8: got %q (%s)", got, how)
	}

	// cp1255 (ווינדוס עברית) — מה ש-yt-dlp פולט בפועל.
	if got, how := decodeTitle(encodeHeb(orig, 0xE0)); got != orig || how != "cp1255" {
		t.Errorf("cp1255: got %q (%s), רצינו %q", got, how, orig)
	}

	// cp862 (DOS עברית).
	if got, how := decodeTitle(encodeHeb(orig, 0x80)); got != orig || how != "cp862" {
		t.Errorf("cp862: got %q (%s), רצינו %q", got, how, orig)
	}

	// הזיהוי עובד אחרי הפענוח (זה מה שנשבר קודם).
	dec, _ := decodeTitle(encodeHeb(orig, 0xE0))
	if !c14IsFull(dec) {
		t.Errorf("c14IsFull נכשל אחרי פענוח cp1255: %q", dec)
	}
	if n := c14ShowName(dec); n != "הפטריוטים עם ינון מגל" {
		t.Errorf("c14ShowName אחרי פענוח: %q", n)
	}
}
