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
	badSignature, badIHDR := bytes.Clone(want), bytes.Clone(want)
	badSignature[0] = 0
	badIHDR[12] = 'X'
	for _, input := range [][]byte{[]byte("not a PNG"), badSignature, badIHDR, want[:24], want[:len(want)-8]} {
		if _, err := readScreenshot(bytes.NewReader(input)); !errors.Is(err, ErrScreenshot) {
			t.Fatalf("malformed PNG error = %v, want ErrScreenshot", err)
		}
	}
	if _, err := readScreenshot(strings.NewReader(strings.Repeat("x", maxScreenshotBytes+1))); !errors.Is(err, ErrScreenshotTooLarge) {
		t.Fatalf("over-limit PNG error = %v, want ErrScreenshotTooLarge", err)
	}
}

func TestReadScreenshotRejectsDecodedPixelOverflow(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewGray(image.Rect(0, 0, 4097, 1024))); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() >= maxScreenshotBytes {
		t.Fatal("test PNG must be below the byte limit")
	}
	if _, err := readScreenshot(bytes.NewReader(encoded.Bytes())); !errors.Is(err, ErrScreenshotTooLarge) {
		t.Fatalf("oversized PNG returns %v, want ErrScreenshotTooLarge", err)
	}
}
