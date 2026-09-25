package main

// פונקציות מול ה-API של ימות המשיח.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// כתובת ה-API של ימות המשיח.
var yemotBase = "https://www.call2all.co.il/ym/api/"

type yemot struct {
	client *http.Client
	apiKey string
}

type yemotResp struct {
	ResponseStatus string `json:"responseStatus"`
	Message        string `json:"message"`
	Contents       string `json:"contents"`
}

// call שולח בקשת POST (טופס מקודד) עם המפתח בכותרת Authorization.
func (y *yemot) call(method string, form url.Values) (*yemotResp, error) {
	_, r, err := y.post(method, form)
	return r, err
}

// post כמו call, ומחזיר גם את גוף התגובה המלא (ל-GetIVR2Dir).
func (y *yemot) post(method string, form url.Values) ([]byte, *yemotResp, error) {
	req, err := http.NewRequest(http.MethodPost, yemotBase+method, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Authorization", y.apiKey)
	resp, err := y.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	var r yemotResp
	if err := json.Unmarshal(body, &r); err != nil {
		return body, nil, fmt.Errorf("תגובה לא צפויה מימות המשיח (%s): %.200s", method, strings.TrimSpace(string(body)))
	}
	if r.ResponseStatus != "OK" {
		return body, &r, fmt.Errorf("שגיאה מימות המשיח ב-%s (%s): %s", method, r.ResponseStatus, r.Message)
	}
	return body, &r, nil
}

// path בונה נתיב במערכת: ext ריק = השלוחה הראשית.
func ivrPath(ext, file string) string {
	if ext == "" {
		return "ivr2:/" + file
	}
	return "ivr2:/" + ext + "/" + file
}

// upload כותב קובץ טקסט (tts / ini) למערכת.
func (y *yemot) upload(ext, file, text string) error {
	form := url.Values{}
	form.Set("what", ivrPath(ext, file))
	form.Set("contents", text)
	_, err := y.call("UploadTextFile", form)
	return err
}

// uploadClient: העלאת קובץ קול (ופרק שלם של פודקאסט) לוקחת יותר זמן מבקשה רגילה.
var uploadClient = &http.Client{Timeout: 10 * time.Minute}

// uploadFile מעלה קובץ (UploadFile, multipart). convert=true: ימות המשיח
// ממירים אותו (MP3 וכו') לפורמט של טלפון.
func (y *yemot) uploadFile(ext, name, filename string, data []byte, convert bool) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("path", ivrPath(ext, name))
	if convert {
		_ = w.WriteField("convertAudio", "1")
	}
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, yemotBase+"UploadFile", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", y.apiKey)
	resp, err := uploadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var r yemotResp
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("תגובה לא צפויה מימות המשיח (UploadFile): %.200s", strings.TrimSpace(string(body)))
	}
	if r.ResponseStatus != "OK" {
		return fmt.Errorf("שגיאה מימות המשיח ב-UploadFile (%s): %s", r.ResponseStatus, r.Message)
	}
	return nil
}

// uploadProbe בודק שלמפתח יש הרשאה ל-UploadFile, בלי להעלות כלום: בקשה בלי
// קובץ. עם הרשאה ימות המשיח עונים "File upload expected" (שום דבר לא נכתב);
// בלי הרשאה — שגיאת ACL (מוחזרת כ-aclError).
func (y *yemot) uploadProbe() error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("path", ivrPath("", "upload-permission-check.wav"))
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, yemotBase+"UploadFile", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", y.apiKey)
	resp, err := y.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var r yemotResp
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("תגובה לא צפויה מימות המשיח (UploadFile): %.200s", strings.TrimSpace(string(body)))
	}
	if strings.Contains(r.ResponseStatus+" "+r.Message, "ACL") {
		return &aclError{fmt.Errorf("שגיאה מימות המשיח ב-UploadFile (%s): %s", r.ResponseStatus, r.Message)}
	}
	return nil
}

// read קורא קובץ טקסט מהמערכת. exists=false כשהקובץ לא קיים.
func (y *yemot) read(ext, file string) (contents string, exists bool, err error) {
	form := url.Values{}
	form.Set("what", ivrPath(ext, file))
	r, err := y.call("GetTextFile", form)
	if err != nil {
		if r != nil && looksNotFound(r.Message) {
			return "", false, nil
		}
		return "", false, err
	}
	return r.Contents, strings.TrimSpace(r.Contents) != "", nil
}

func looksNotFound(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{"not found", "not exist", "does not", "no such", "לא נמצא", "לא קיים"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// remove מוחק קבצים (FileAction?action=delete).
func (y *yemot) remove(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	form := url.Values{}
	form.Set("action", "delete")
	for i, p := range paths {
		form.Set(fmt.Sprintf("what%d", i), p)
	}
	_, err := y.call("FileAction", form)
	return err
}

// setIniValues משנה/מוסיף שורות key=value בקובץ ext.ini קיים, בלי לגעת
// בשאר ההגדרות. ערך ריק = לא לשנות את המפתח הזה. מחזיר את הטקסט החדש
// ו-true אם משהו השתנה.
func setIniValues(ini string, values [][2]string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(ini, "\r\n", "\n"), "\n")
	changed := false
	for _, kv := range values {
		key, val := kv[0], kv[1]
		if val == "" {
			continue
		}
		want := key + "=" + val
		found := false
		for i, l := range lines {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, key+"=") {
				found = true
				if t != want {
					lines[i] = want
					changed = true
				}
			}
		}
		if !found {
			// מכניסים לפני שורות ריקות בסוף הקובץ.
			n := len(lines)
			for n > 0 && strings.TrimSpace(lines[n-1]) == "" {
				n--
			}
			lines = append(lines[:n], append([]string{want}, lines[n:]...)...)
			changed = true
		}
	}
	return strings.Join(lines, "\n"), changed
}

// iniValue מחזיר את הערך של key בקובץ ini (או "").
func iniValue(ini, key string) string {
	for _, l := range strings.Split(ini, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, key+"=") {
			return strings.TrimSpace(strings.TrimPrefix(t, key+"="))
		}
	}
	return ""
}

// dirInfo — מה יש בשלוחה, לפי GetIVR2Dir.
//
// חשוב (נבדק מול המערכת האמיתית): ימות המשיח לא מחזירים ברשימה קבצי טקסט
// אחרים (למשל bridge.txt) — רק קבצי שמע/TTS/מערכת (files) והגדרות (ini).
type dirInfo struct {
	Exists bool
	Files  []string          // files + ini
	Dirs   []string          // שלוחות-בת
	Ini    map[string]string // ההגדרות בפועל (extIni — כולל מה שעובר בירושה מהשלוחות שמעל)
}

// dir מחזיר את תוכן השלוחה. שלוחה שלא קיימת — Exists=false בלי שגיאה.
func (y *yemot) dir(ext string) (dirInfo, error) {
	form := url.Values{}
	form.Set("path", "ivr2:/"+ext)
	body, r, err := y.post("GetIVR2Dir", form)
	if err != nil {
		if r != nil && looksNotFound(r.Message) { // "extension does not exist"
			return dirInfo{}, nil
		}
		return dirInfo{}, err
	}
	var d struct {
		ExtIni json.RawMessage `json:"extIni"`
		Dirs   json.RawMessage `json:"dirs"`
		Files  json.RawMessage `json:"files"`
		Ini    json.RawMessage `json:"ini"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return dirInfo{}, fmt.Errorf("תגובה לא צפויה מ-GetIVR2Dir: %.200s", strings.TrimSpace(string(body)))
	}
	info := dirInfo{Exists: true, Ini: map[string]string{}}
	var ini map[string]any // שלוחה בלי הגדרות יכולה לחזור כ-[] ולא כ-{} — אז פשוט מדלגים
	if json.Unmarshal(d.ExtIni, &ini) == nil {
		for k, v := range ini {
			info.Ini[k] = fmt.Sprint(v)
		}
	}
	info.Dirs = names(d.Dirs)
	info.Files = append(names(d.Files), names(d.Ini)...)
	return info, nil
}

// names: שמות מתוך מערך של אובייקטים {"name": ...}. מבנה אחר — רשימה ריקה.
func names(raw json.RawMessage) []string {
	var list []struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &list)
	var out []string
	for _, x := range list {
		if x.Name != "" {
			out = append(out, x.Name)
		}
	}
	return out
}

// createExt יוצר שלוחה חדשה עם ההגדרות שב-ini (UpdateExtension).
//
// זו הפעולה היחידה ב-API שיוצרת שלוחה: UploadTextFile לשלוחה שלא קיימת מחזיר
// "OK" ולא עושה כלום (נבדק מול המערכת האמיתית — כך נוצרו תפריטים עם אפשרויות
// שלא מובילות לשום מקום). דורש הרשאה ל-UpdateExtension במפתח ה-API.
func (y *yemot) createExt(ext, ini string) error {
	form := url.Values{}
	for _, l := range strings.Split(ini, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && k != "" {
			form.Set(k, v)
		}
	}
	form.Set("path", "ivr2:/"+ext)
	_, err := y.call("UpdateExtension", form)
	return err
}

// removeIniKey מוריד שורת key=... מקובץ ext.ini. removed=true כשהייתה כזו.
func removeIniKey(ini, key string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(ini, "\r\n", "\n"), "\n")
	kept := lines[:0]
	removed := false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), key+"=") {
			removed = true
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n"), removed
}
