package postgresbinding

import (
	"errors"
	"net/url"
	"testing"
)

func TestConnectionURLPreservesCredentials(t *testing.T) {
	connection, err := ConnectionURL("2001:db8::2", 5433, "orders",
		`{"username":"app+writer","password":"p@ss:/?%word"}`)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := url.Parse(connection.String())
	if err != nil {
		t.Fatal(err)
	}
	password, ok := decoded.User.Password()
	if !ok || password != "p@ss:/?%word" || decoded.User.Username() != "app+writer" ||
		decoded.Host != "[2001:db8::2]:5433" || decoded.Path != "/orders" {
		t.Fatal("connection did not preserve its escaped components")
	}
}

func TestConnectionURLRejectsInvalidCredentials(t *testing.T) {
	for _, value := range []string{`{}`, `{"username":"role","password":""}`, `{"username":"role","password":"\u0000"}`} {
		if _, err := ConnectionURL("db.example", 5432, "orders", value); !errors.Is(err, ErrIncompleteCredentials) {
			t.Fatalf("expected incomplete credentials, got %v", err)
		}
	}
	if _, err := ConnectionURL("db.example", 5432, "orders", `{"username":[]}`); err == nil {
		t.Fatal("accepted invalid JSON credentials")
	}
}
