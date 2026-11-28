package s3binding

import (
	"errors"
	"testing"
)

func TestDecodeCredentials(t *testing.T) {
	actual, err := DecodeCredentials(`{"access_key_id":"key","secret_access_key":"secret","session_token":"session"}`)
	if err != nil || actual != (Credentials{AccessKeyID: "key", SecretAccessKey: "secret", SessionToken: "session"}) {
		t.Fatalf("valid S3 credentials were not decoded: %v", err)
	}
	for _, value := range []string{`{}`, `{"access_key_id":"key","secret_access_key":"line\nfeed"}`, `{"access_key_id":"key","secret_access_key":"secret","session_token":"\u0000"}`} {
		if _, err := DecodeCredentials(value); !errors.Is(err, ErrIncompleteCredentials) {
			t.Fatalf("expected invalid S3 credentials, got %v", err)
		}
	}
	if _, err := DecodeCredentials(`{"access_key_id":17}`); err == nil {
		t.Fatal("accepted an invalid access key type")
	}
}
