// yemot-news-bridge
//
// תוכנית קטנה שרצה כ-Cron Job (למשל פעם בדקה): שולפת את ההודעה האחרונה
// מה-API של Telegram Popup ("ערוץ חי"), ושולחת את הטקסט שלה לקו הטלפון
// בימות המשיח, לשלוחת TTS, כדי שמתקשרים ישמעו אותה מוקראת.
//
// מריצים כל דקה (Render Cron Job עם schedule "* * * * *").
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// FeedItem משקף פריט בודד שחוזר מ-/api/messages של Telegram Popup.
// רק השדות שבהם אנחנו משתמשים כאן.
type FeedItem struct {
	ID      int    `json:"id"`
	Channel string `json:"channel"`
	TS      int64  `json:"ts"`
	Text    string `json:"text"`
}

type FeedResponse struct {
	Items []FeedItem `json:"items"`
}

func main() {
	// --- קריאת הגדרות מתוך משתני סביבה ---
	feedURL := envOr("TGPOPUP_URL", "https://telegram-popup.onrender.com/api/messages")
	feedKey := os.Getenv("TGPOPUP_KEY")
	if feedKey == "" {
		log.Fatal("חסר משתנה סביבה TGPOPUP_KEY")
	}

	yemotNumber := envOr("YEMOT_NUMBER", "0772263731")
	yemotPassword := os.Getenv("YEMOT_PASSWORD")
	if yemotPassword == "" {
		log.Fatal("חסר משתנה סביבה YEMOT_PASSWORD (סיסמת הגישה שהוגדרה בהגדרות משתמש)")
	}
	yemotExt := envOr("YEMOT_EXT", "1")          // מספר השלוחה
	yemotFile := envOr("YEMOT_FILE", "001.tts")  // שם קובץ ה-TTS בתוך השלוחה

	client := &http.Client{Timeout: 20 * time.Second}

	item, err := fetchLatest(client, feedURL, feedKey)
	if err != nil {
		log.Fatalf("שגיאה בשליפת ההודעה האחרונה: %v", err)
	}
	if item == nil {
		log.Println("אין הודעות זמינות — לא נשלח כלום.")
		return
	}

	text := strings.TrimSpace(item.Text)
	if text == "" {
		log.Println("ההודעה האחרונה ריקה מטקסט (כנראה תמונה/סרטון בלי כיתוב) — לא נשלח כלום.")
		return
	}

	if err := pushToYemot(client, yemotNumber, yemotPassword, yemotExt, yemotFile, text); err != nil {
		log.Fatalf("שגיאה בשליחה לימות המשיח: %v", err)
	}

	log.Printf("נשלח בהצלחה: [%s #%d] %.60s...", item.Channel, item.ID, text)
}

// fetchLatest שולף את רשימת ההודעות ומחזיר את הראשונה (החדשה ביותר).
// (ה-API של Telegram Popup ממיין מהחדש לישן.)
func fetchLatest(client *http.Client, feedURL, key string) (*FeedItem, error) {
	u, err := url.Parse(feedURL)
	if err != nil {
		return nil, fmt.Errorf("כתובת feed לא תקינה: %w", err)
	}
	q := u.Query()
	q.Set("k", key)
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("סטטוס %d מה-feed: %s", resp.StatusCode, string(body))
	}

	var parsed FeedResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("JSON לא תקין מה-feed: %w", err)
	}
	if len(parsed.Items) == 0 {
		return nil, nil
	}
	return &parsed.Items[0], nil
}

// pushToYemot מעלה טקסט לקובץ TTS בשלוחה נתונה, דרך ה-API הקלאסי
// של ימות המשיח (www.call2all.co.il/ym/api/UploadTextFile).
func pushToYemot(client *http.Client, number, password, ext, file, text string) error {
	base := "https://www.call2all.co.il/ym/api/UploadTextFile"

	// חיתוך למקרה שהטקסט ארוך במיוחד — TTS ארוך מדי עלול להיכשל/להשתתק.
	const maxRunes = 1500
	r := []rune(text)
	if len(r) > maxRunes {
		text = string(r[:maxRunes]) + "..."
	}

	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("token", number+":"+password)
	q.Set("what", "ivr2:/"+ext+"/"+file)
	q.Set("contents", text)
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	respText := strings.TrimSpace(string(body))

	// ימות המשיח מחזיר "OK" (או משהו שמתחיל ב-OK) בהצלחה,
	// ותיאור שגיאה בעברית/קוד שגיאה אחרת.
	if !strings.HasPrefix(strings.ToUpper(respText), "OK") {
		return fmt.Errorf("תגובת שגיאה מימות המשיח: %s", respText)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
