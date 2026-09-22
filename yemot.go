package main

// פונקציות מול ה-API של ימות המשיח.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	req, err := http.NewRequest(http.MethodPost, yemotBase+method, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Authorization", y.apiKey)
	resp, err := y.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r yemotResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("תגובה לא צפויה מימות המשיח (%s): %.200s", method, strings.TrimSpace(string(body)))
	}
	if r.ResponseStatus != "OK" {
		return &r, fmt.Errorf("שגיאה מימות המשיח ב-%s (%s): %s", method, r.ResponseStatus, r.Message)
	}
	return &r, nil
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

// listDir מחזיר את שמות הקבצים בשלוחה (GetIVR2Dir) — לאבחון בלוג.
func (y *yemot) listDir(ext string) ([]string, error) {
	form := url.Values{}
	p := "ivr2:/"
	if ext != "" {
		p = "ivr2:/" + ext
	}
	form.Set("path", p)
	req, err := http.NewRequest(http.MethodPost, yemotBase+"GetIVR2Dir", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Authorization", y.apiKey)
	resp, err := y.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		ResponseStatus string `json:"responseStatus"`
		Message        string `json:"message"`
		Files          []struct {
			Name string `json:"name"`
		} `json:"files"`
		Ini []struct {
			Name string `json:"name"`
		} `json:"ini"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("תגובה לא צפויה: %.200s", strings.TrimSpace(string(body)))
	}
	if r.ResponseStatus != "OK" {
		return nil, fmt.Errorf("%s: %s", r.ResponseStatus, r.Message)
	}
	var names []string
	for _, f := range r.Files {
		names = append(names, f.Name)
	}
	for _, f := range r.Ini {
		names = append(names, f.Name)
	}
	return names, nil
}
