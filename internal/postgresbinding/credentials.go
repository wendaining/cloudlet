// Package postgresbinding shares the project Secret format between connection
// verification and container publication.
package postgresbinding

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

var ErrIncompleteCredentials = errors.New("PostgreSQL credentials require a username and password without NUL bytes")

// ConnectionURL decodes a project Secret and prepares a PostgreSQL URI. The
// caller sets TLS parameters appropriate for its connection.
func ConnectionURL(host string, port int, database, value string) (url.URL, error) {
	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(value), &credentials); err != nil {
		return url.URL{}, fmt.Errorf("decode PostgreSQL credentials: %w", err)
	}
	if credentials.Username == "" || credentials.Password == "" ||
		strings.ContainsRune(credentials.Username, 0) || strings.ContainsRune(credentials.Password, 0) {
		return url.URL{}, ErrIncompleteCredentials
	}
	return url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(credentials.Username, credentials.Password),
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/" + database,
	}, nil
}
