// SPDX-License-Identifier: Apache-2.0

package jsondocument

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCanonicalSortsObjectsAndPreservesArrayOrder(t *testing.T) {
	input := []byte(` { "z": [3, 2, 1], "a": {"second": 2, "first": 1}, "zero": -0 } `)
	want := []byte(`{"a":{"first":1,"second":2},"z":[3,2,1],"zero":0}`)
	actual, err := Canonical(input)
	if err != nil {
		t.Fatalf("Canonical() error = %v", err)
	}
	if !bytes.Equal(actual, want) {
		t.Fatalf("Canonical() = %s, want %s", actual, want)
	}
	second, err := Canonical(actual)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second, actual) {
		t.Fatalf("canonicalization is not idempotent:\n%s\n%s", actual, second)
	}
}

func TestCanonicalRejectsMalformedOrAmbiguousDocuments(t *testing.T) {
	tests := map[string][]byte{
		"empty":               nil,
		"duplicate root":      []byte(`{"same":1,"same":2}`),
		"duplicate nested":    []byte(`{"nested":{"same":1,"same":2}}`),
		"trailing value":      []byte(`{}[]`),
		"fraction":            []byte(`{"number":1.0}`),
		"exponent":            []byte(`{"number":1e3}`),
		"plus sign":           []byte(`{"number":+1}`),
		"invalid escape":      []byte(`{"value":"\uZZZZ"}`),
		"unterminated string": []byte(`{"value":"missing}`),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Canonical(input); err == nil {
				t.Fatal("Canonical() accepted invalid input")
			}
		})
	}
}

func TestCanonicalRejectsInvalidUTF8WithoutReplacement(t *testing.T) {
	input := append([]byte(`{"value":"before`), 0xff)
	input = append(input, []byte(`after"}`)...)
	if _, err := Canonical(input); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("Canonical() error = %v, want ErrInvalidUTF8", err)
	}
	for _, valid := range [][]byte{
		[]byte(`{"value":"replacement �"}`),
		[]byte("{\"value\":\"replacement �\"}"),
	} {
		if _, err := Canonical(valid); err != nil {
			t.Fatalf("Canonical(valid U+FFFD) error = %v", err)
		}
	}
}

func TestCanonicalRejectsUnpairedEscapedSurrogates(t *testing.T) {
	for name, input := range map[string][]byte{
		"lone high":          []byte(`{"value":"\ud800"}`),
		"lone low":           []byte(`{"value":"\udc00"}`),
		"high then high":     []byte(`{"value":"\ud800\ud801"}`),
		"high then ordinary": []byte(`{"value":"\ud800\u0041"}`),
		"separated pair":     []byte(`{"value":"\ud800 \udc00"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Canonical(input); !errors.Is(err, ErrInvalidUnicodeSurrogate) {
				t.Fatalf("Canonical() error = %v, want ErrInvalidUnicodeSurrogate", err)
			}
		})
	}
	actual, err := Canonical([]byte(`{"value":"\ud83d\ude00"}`))
	if err != nil {
		t.Fatalf("Canonical(valid pair) error = %v", err)
	}
	if string(actual) != `{"value":"😀"}` {
		t.Fatalf("Canonical(valid pair) = %s", actual)
	}
}

func TestCanonicalEscapingExpansionAndNestedValues(t *testing.T) {
	actual, err := Canonical([]byte(`{"value":"<>&","escaped":"quote\" slash\/ backslash\\"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"escaped":"quote\" slash/ backslash\\","value":"\u003c\u003e\u0026"}`)
	if !bytes.Equal(actual, want) {
		t.Fatalf("Canonical() = %s, want %s", actual, want)
	}
	if len(actual) <= len(`{"value":"<>&","escaped":"quote\" slash\/ backslash\\"}`) {
		t.Fatal("test did not exercise HTML-escaping expansion")
	}

	nested := strings.Repeat("[", 1024) + "0" + strings.Repeat("]", 1024)
	canonical, err := Canonical([]byte(nested))
	if err != nil {
		t.Fatalf("Canonical(nested) error = %v", err)
	}
	if string(canonical) != nested {
		t.Fatal("Canonical(nested) changed array structure")
	}
}
