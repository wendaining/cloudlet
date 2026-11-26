package browser

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestArtifactUploadValidation(t *testing.T) {
	valid := ArtifactUpload{"screenshot", uuid.New(), uuid.New(), "#file", 1000}
	if !valid.valid() {
		t.Fatal("valid screenshot upload rejected")
	}
	download := valid
	download.Kind = "download"
	if !download.valid() {
		t.Fatal("valid download upload rejected")
	}
	for name, change := range map[string]func(*ArtifactUpload){
		"kind":     func(input *ArtifactUpload) { input.Kind = "path" },
		"source":   func(input *ArtifactUpload) { input.SourceSessionID = uuid.Nil },
		"artifact": func(input *ArtifactUpload) { input.ArtifactID = uuid.Nil },
		"selector": func(input *ArtifactUpload) { input.Selector = strings.Repeat("x", 1025) },
		"timeout":  func(input *ArtifactUpload) { input.Timeout = 30001 },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			change(&input)
			if input.valid() {
				t.Fatalf("accepted invalid upload: %+v", input)
			}
		})
	}
}
