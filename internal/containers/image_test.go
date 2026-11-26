package containers

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/image"
	"github.com/opencontainers/go-digest"
)

const testImageDigest = "sha256:5aea7cc516b419e3526f47dd1531be31a56a046cfe44754d94f9383e13e2ee99"

func TestParseImageReference(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantName  string
		wantError bool
	}{
		{"familiar name", "nginx@" + testImageDigest, "docker.io/library/nginx@" + testImageDigest, false},
		{"familiar name with tag", "nginx:1.29.3-alpine@" + testImageDigest, "docker.io/library/nginx@" + testImageDigest, false},
		{"qualified name", "docker.io/library/nginx@" + testImageDigest, "docker.io/library/nginx@" + testImageDigest, false},
		{"nested repository", "nginxinc/nginx-unprivileged:1.29.3-alpine@" + testImageDigest, "docker.io/nginxinc/nginx-unprivileged@" + testImageDigest, false},
		{"registry with port", "127.0.0.1:5000/cloudlet/image@" + testImageDigest, "127.0.0.1:5000/cloudlet/image@" + testImageDigest, false},
		{"sha512 digest", "nginx@sha512:" + strings.Repeat("a", 128), "docker.io/library/nginx@sha512:" + strings.Repeat("a", 128), false},
		{"empty", "", "", true},
		{"tag only", "nginx:1.29.3-alpine", "", true},
		{"name only", "nginx", "", true},
		{"whitespace", "nginx @" + testImageDigest, "", true},
		{"nul byte", "nginx\x00@" + testImageDigest, "", true},
		{"uppercase repository", "Nginx@" + testImageDigest, "", true},
		{"missing digest separator", "nginx@", "", true},
		{"double digest separator", "nginx@@sha256:abc", "", true},
		{"short digest", "nginx@sha256:abc", "", true},
		{"uppercase digest", "nginx@sha256:5AEA7CC516B419E3526F47DD1531BE31A56A046CFE44754D94F9383E13E2EE99", "", true},
		{"unsupported digest algorithm", "nginx@md5:d41d8cd98f00b204e9800998ecf8427e", "", true},
		{"too long", strings.Repeat("a", maxImageLength+1) + "@" + testImageDigest, "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name, got, err := parseImageReference(test.value)
			if test.wantError {
				if err == nil || !errors.Is(err, fs.ErrInvalid) {
					t.Fatalf("parseImageReference(%q) = (%q, %q, %v), want fs.ErrInvalid", test.value, name, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name != test.wantName {
				t.Fatalf("parseImageReference(%q) name = %q, want %q", test.value, name, test.wantName)
			}
			_, wantDigest, _ := strings.Cut(test.wantName, "@")
			if got.String() != wantDigest {
				t.Fatalf("parseImageReference(%q) digest = %q, want %q", test.value, got, wantDigest)
			}
		})
	}
}

func TestParseImageReferenceRequiresDigest(t *testing.T) {
	_, _, err := parseImageReference("nginx:latest")
	if !errors.Is(err, fs.ErrInvalid) || !strings.Contains(err.Error(), "immutable digest is required") {
		t.Fatalf("parseImageReference() = %v, want immutable digest error", err)
	}
}

func TestPrepareImageRejectsInvalidReferenceBeforeDocker(t *testing.T) {
	// The adapter has no Docker client. An invalid reference must be rejected
	// before any Docker operation, so these calls must not panic.
	adapter := &Adapter{}
	for _, value := range []string{"", "nginx", "nginx:latest", "nginx@sha256:abc"} {
		if _, err := adapter.PrepareImage(context.Background(), value); err == nil || !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("PrepareImage(%q) = %v, want fs.ErrInvalid", value, err)
		}
	}
}

func TestVerifyImage(t *testing.T) {
	canonical := "docker.io/library/nginx@" + testImageDigest
	want := digest.Digest(testImageDigest)
	imageID := "sha256:" + strings.Repeat("b", 64)
	prepared, err := verifyImage(image.InspectResponse{
		ID:          imageID,
		RepoDigests: []string{canonical},
	}, canonical, want)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Reference != canonical || prepared.Digest != testImageDigest || prepared.ID != imageID {
		t.Fatalf("prepared image = %+v", prepared)
	}
}

func TestVerifyImageAcceptsMirrorRepositoryName(t *testing.T) {
	canonical := "docker.io/library/nginx@" + testImageDigest
	mirror := "mirror.example.com/library/nginx@" + testImageDigest
	prepared, err := verifyImage(image.InspectResponse{
		ID:          "sha256:" + strings.Repeat("b", 64),
		RepoDigests: []string{mirror, canonical},
	}, canonical, digest.Digest(testImageDigest))
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Reference != canonical {
		t.Fatalf("reference = %q, want %q", prepared.Reference, canonical)
	}
}

func TestVerifyImageRejectsMismatchedIdentity(t *testing.T) {
	canonical := "docker.io/library/nginx@" + testImageDigest
	want := digest.Digest(testImageDigest)
	other := "sha256:" + strings.Repeat("c", 64)
	validID := "sha256:" + strings.Repeat("b", 64)
	tests := []struct {
		name string
		info image.InspectResponse
	}{
		{"missing image ID", image.InspectResponse{RepoDigests: []string{canonical}}},
		{"invalid image ID", image.InspectResponse{ID: "not-a-digest", RepoDigests: []string{canonical}}},
		{"empty repo digests", image.InspectResponse{ID: validID}},
		{"different digest", image.InspectResponse{ID: validID, RepoDigests: []string{"docker.io/library/nginx@" + other}}},
		{"digest without repository", image.InspectResponse{ID: validID, RepoDigests: []string{testImageDigest}}},
		{"malformed digest", image.InspectResponse{ID: validID, RepoDigests: []string{"docker.io/library/nginx@sha256:abc"}}},
		{"empty entry", image.InspectResponse{ID: validID, RepoDigests: []string{""}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := verifyImage(test.info, canonical, want)
			if err == nil || !errors.Is(err, ErrImageDigestMismatch) {
				t.Fatalf("verifyImage() = (%+v, %v), want ErrImageDigestMismatch", prepared, err)
			}
		})
	}
}

func TestValidateImageReferenceDoesNotContactDocker(t *testing.T) {
	if err := ValidateImageReference("nginx@" + testImageDigest); err != nil {
		t.Fatal(err)
	}
	if err := ValidateImageReference("nginx:latest"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("ValidateImageReference() = %v, want fs.ErrInvalid", err)
	}
	canonical, got, err := ParseImageReference("nginx:1.29.3-alpine@" + testImageDigest)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "docker.io/library/nginx@"+testImageDigest || got != testImageDigest {
		t.Fatalf("ParseImageReference() = (%q, %q)", canonical, got)
	}
}
