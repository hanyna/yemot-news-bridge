package main

import (
	"os"
	"testing"
)

// TestMain: הבדיקות הישנות נכתבו למספור רצוף (10001, 10003, ...). הן בודקות דברים
// אחרים, אז רצות עם צעד 2; המספור עם הרווחים נבדק בבדיקות שמגדירות archStride בעצמן.
func TestMain(m *testing.M) {
	archStride = 2
	os.Exit(m.Run())
}
