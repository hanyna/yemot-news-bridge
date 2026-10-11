//go:build windows

package main

import (
	"log"
	"syscall"
)

// keepSystemAwake מונע מהמחשב לחזור לשינה בזמן ההרצה (חשוב כשמשימה מתוזמנת
// מעירה אותו באמצע הלילה — אחרת ווינדוס עלול להרדים אותו שוב תוך 2 דקות,
// באמצע ההורדה). המצב משתחרר לבד כשהתהליך מסתיים.
const (
	esSystemRequired = 0x00000001
	esContinuous     = 0x80000000
)

func keepSystemAwake() {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")
	if _, _, err := proc.Call(uintptr(esSystemRequired | esContinuous)); err != nil && err != syscall.Errno(0) {
		log.Printf("הערה: לא הצלחתי לבקש מווינדוס להישאר ער: %v", err)
	}
}
