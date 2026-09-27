//go:build diag

package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiagExt4(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	ini, _, err := y.read("4", "ext.ini")
	i6, _ := y.dir("6")
	fmt.Printf("::notice title=ext4::%s%%0Aerr=%v%%0Aext6 files=%v\n", strings.ReplaceAll(ini, "\n", " | "), err, i6.Files)
}
