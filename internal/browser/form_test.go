package browser

import (
	"strings"
	"testing"
)

func TestFormActionInput(t *testing.T) {
	valid := FormAction{Action: "fill", Selector: "#name", Timeout: 10000}
	if !valid.valid() {
		t.Fatal("an empty value should clear the input")
	}
	for _, invalid := range []FormAction{
		{Action: "submit", Selector: "#name", Timeout: 10000},
		{Action: "fill", Selector: " ", Timeout: 10000},
		{Action: "select", Selector: "#team", Value: "x\x00y", Timeout: 10000},
		{Action: "fill", Selector: "#name", Value: strings.Repeat("😀", 513), Timeout: 10000},
		{Action: "select", Selector: "#team", Value: "browser", Timeout: 30001},
	} {
		if invalid.valid() {
			t.Fatalf("invalid form action accepted: %+v", invalid)
		}
	}
}
