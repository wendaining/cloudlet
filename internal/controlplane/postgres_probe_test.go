package controlplane

import (
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
)

func TestPostgresBindingInput(t *testing.T) {
	valid := apigen.PostgresBindingInput{Name: "orders-db", Host: "db.example.org", Port: 5432, Database: "orders", SecretVersionId: uuid.New()}
	cases := []struct {
		name   string
		change func(*apigen.PostgresBindingInput)
	}{
		{"invalid name", func(input *apigen.PostgresBindingInput) { input.Name = "db/other" }},
		{"Unix socket", func(input *apigen.PostgresBindingInput) { input.Host = "/tmp/pg" }},
		{"multiple hosts", func(input *apigen.PostgresBindingInput) { input.Host = "one,two" }},
		{"port overflow", func(input *apigen.PostgresBindingInput) { input.Port = 65536 }},
		{"invalid database", func(input *apigen.PostgresBindingInput) { input.Database = "test?option=value" }},
		{"missing secret", func(input *apigen.PostgresBindingInput) { input.SecretVersionId = uuid.Nil }},
	}
	if !validPostgresBindingInput(valid) {
		t.Fatal("rejected valid binding")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.change(&input)
			if validPostgresBindingInput(input) {
				t.Fatal("accepted invalid binding")
			}
		})
	}
	valid.Host = "::1"
	valid.Port = 65535
	if !validPostgresBindingInput(valid) {
		t.Fatal("rejected IPv6 or maximum port")
	}
}

func TestPostgresBindingRejectsMalformedCredentials(t *testing.T) {
	for _, value := range []string{`not-json`, `{"username":"app"}`, `{"username":"app","password":"\u0000"}`} {
		if diagnostic, err := verifyPostgresBinding(t.Context(), apigen.PostgresBindingInput{}, value, false); err == nil || diagnostic == "" {
			t.Fatal("expected a credential diagnostic before connection")
		}
	}
}
