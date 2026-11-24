package browser

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestReadScreenshot(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	want := encoded.Bytes()
	data, err := readScreenshot(bytes.NewReader(want))
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("valid PNG was not preserved: %v", err)
	}
	for _, input := range [][]byte{[]byte("not a PNG"), want[:len(want)-8]} {
		if _, err := readScreenshot(bytes.NewReader(input)); !errors.Is(err, ErrScreenshot) {
			t.Fatalf("malformed PNG error = %v, want ErrScreenshot", err)
		}
	}
	if _, err := readScreenshot(strings.NewReader(strings.Repeat("x", maxScreenshotBytes+1))); !errors.Is(err, ErrScreenshotTooLarge) {
		t.Fatalf("over-limit PNG error = %v, want ErrScreenshotTooLarge", err)
	}
}
