package browser

import (
	"strings"
	"testing"
)

func TestBrowserDownloadFilename(t *testing.T) {
	for name, want := range map[string]string{
		"../../sample.txt":       "sample.txt",
		"C:\\tmp\\你好.txt":        "你好.txt",
		"bad\r\nname.txt":        "badname.txt",
		"..":                     "browser-download",
		"/":                      "browser-download",
		strings.Repeat("界", 130): strings.Repeat("界", 128),
	} {
		if got := downloadFilename(name); got != want {
			t.Errorf("downloadFilename(%q)=%q, want %q", name, got, want)
		}
	}
}
