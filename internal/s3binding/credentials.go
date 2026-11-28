// Package s3binding shares the project Secret format between binding
// verification and application container setup.
package s3binding

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrIncompleteCredentials = errors.New("S3 credentials require valid access and secret keys")

type Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
}

func DecodeCredentials(value string) (Credentials, error) {
	var credentials Credentials
	if err := json.Unmarshal([]byte(value), &credentials); err != nil {
		return Credentials{}, fmt.Errorf("decode S3 credentials: %w", err)
	}
	if credentials.AccessKeyID == "" || credentials.SecretAccessKey == "" ||
		strings.ContainsAny(credentials.AccessKeyID+credentials.SecretAccessKey+credentials.SessionToken, "\r\n\x00") {
		return Credentials{}, ErrIncompleteCredentials
	}
	return credentials, nil
}
