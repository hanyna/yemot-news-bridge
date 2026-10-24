package main

// אבחון זמני: בודק אילו בייטים בדיוק מגיעים מ-yt-dlp לכותרות ביוטיוב, כדי
// לדעת למה הזיהוי של "התוכנית המלאה" לא תפס. מריצים: yemot-patriots.exe debug
// — נוצר קובץ patriots-debug.txt ליד ה-exe; שולחים אותו חזרה.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func patriotsDebug() {
	loadEnvFile(filepath.Join(exeDir(), "patriots.env"))
	patriotsInsecure = envOr("PATRIOTS_INSECURE_TLS", "on") != "off"
	ytdlp := envOr("YTDLP", "yt-dlp")
	if yt, _ := findMoridTools(); ytdlp == "yt-dlp" && yt != "" {
		ytdlp = yt
	}
	playlist := envOr("PATRIOTS_PLAYLIST", patriotsDefaultPlaylist)

	var b strings.Builder
	fmt.Fprintf(&b, "yt-dlp: %s\nplaylist: %s\nc14Full: %q (bytes: % x)\n\n", ytdlp, playlist, c14Full, []byte(c14Full))

	run := func(label string, env []string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		args := []string{"--flat-playlist", "--playlist-end", "60", "--no-warnings", "--no-progress", "--print", "%(title)s"}
		if patriotsInsecure {
			args = append(args, "--no-check-certificate")
		}
		args = append(args, "https://www.youtube.com/playlist?list="+playlist)
		cmd := exec.CommandContext(ctx, ytdlp, args...)
		if env != nil {
			cmd.Env = env
		}
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		fmt.Fprintf(&b, "=== variant: %s ===\n", label)
		if err != nil {
			fmt.Fprintf(&b, "שגיאה: %v: %.200s\n\n", err, strings.TrimSpace(errb.String()))
			return
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		matched := 0
		var sample string
		for _, l := range lines {
			if c14IsFull(l) {
				matched++
				if sample == "" {
					sample = l
				}
			}
		}
		fmt.Fprintf(&b, "שורות: %d | תואמות c14IsFull: %d\n", len(lines), matched)
		// שלוש כותרות ראשונות עם תאריך (ASCII), + הקסה של הראשונה שבהן.
		shown := 0
		for _, l := range lines {
			if strings.Contains(l, "2026") {
				fmt.Fprintf(&b, "title[%d]: %s\n", shown, l)
				if shown == 0 {
					fmt.Fprintf(&b, "  hex: % x\n", []byte(l))
				}
				shown++
				if shown >= 3 {
					break
				}
			}
		}
		if sample != "" {
			fmt.Fprintf(&b, "sample matched: %s\n", sample)
		}
		fmt.Fprint(&b, "\n")
	}

	run("WITH utf8Env (PYTHONIOENCODING=utf-8)", utf8Env())
	run("WITHOUT env (ברירת מחדל)", nil)

	path := filepath.Join(exeDir(), "patriots-debug.txt")
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
	fmt.Println(b.String())
	fmt.Println("נשמר:", path)
}
