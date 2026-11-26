package controlplane

import (
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
)

func TestS3BindingInput(t *testing.T) {
	valid := apigen.S3BindingInput{Name: "media", Endpoint: "https://s3.example.org", Region: "us-east-1", Bucket: "test.media", SecretVersionId: uuid.New()}
	cases := []struct {
		name   string
		change func(*apigen.S3BindingInput)
	}{
		{"invalid name", func(input *apigen.S3BindingInput) { input.Name = "a/b" }},
		{"user info", func(input *apigen.S3BindingInput) { input.Endpoint = "https://user:password@s3.example.org" }},
		{"path", func(input *apigen.S3BindingInput) { input.Endpoint = "https://s3.example.org/other" }},
		{"port overflow", func(input *apigen.S3BindingInput) { input.Endpoint = "https://s3.example.org:65536" }},
		{"empty region", func(input *apigen.S3BindingInput) { input.Region = "" }},
		{"empty bucket label", func(input *apigen.S3BindingInput) { input.Bucket = "test..media" }},
		{"numeric IP bucket", func(input *apigen.S3BindingInput) { input.Bucket = "127.0.0.1" }},
		{"missing secret", func(input *apigen.S3BindingInput) { input.SecretVersionId = uuid.Nil }},
	}
	if !validS3BindingInput(valid) {
		t.Fatal("rejected valid binding")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.change(&input)
			if validS3BindingInput(input) {
				t.Fatal("accepted invalid binding")
			}
		})
	}
}

func TestS3BindingRejectsMalformedCredentialsAndPlaintext(t *testing.T) {
	input := apigen.S3BindingInput{Endpoint: "https://s3.example.org"}
	for _, value := range []string{`not-json`, `{"access_key_id":"key"}`, `{"access_key_id":"key","secret_access_key":"secret","session_token":"\r\nheader:value"}`} {
		if diagnostic, err := verifyS3Binding(t.Context(), input, value, false); err == nil || diagnostic == "" {
			t.Fatal("expected a credential diagnostic before connection")
		}
	}
	input.Endpoint = "http://127.0.0.1:8333"
	if diagnostic, err := verifyS3Binding(t.Context(), input, "", false); err == nil || diagnostic != "S3 端点需要使用 HTTPS" {
		t.Fatal("expected production plaintext to fail before connection")
	}
}
