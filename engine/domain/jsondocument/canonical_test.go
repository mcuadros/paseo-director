// SPDX-License-Identifier: Apache-2.0

package jsondocument

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

var benchmarkCanonicalDocument []byte

type canonicalBoundary struct {
	name       string
	maximum    int
	normalized bool
}

var canonicalBoundaries = []canonicalBoundary{
	{name: "TaskStore", maximum: 64 * 1024, normalized: true},
	{name: "configuration", maximum: 1 << 20},
}

func maximallyNestedObjectDocument(maximum int) []byte {
	const prefix = `{"a":`
	depth := (maximum - 1) / (len(prefix) + 1)
	return []byte(strings.Repeat(prefix, depth) + "0" + strings.Repeat("}", depth))
}

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

func TestCanonicalWithNormalizedNumbersPreservesStrictEncoding(t *testing.T) {
	left, err := CanonicalWithNormalizedNumbers([]byte(`{"z":1.2300e2,"a":[-0,0.0010]}`))
	if err != nil {
		t.Fatalf("CanonicalWithNormalizedNumbers(first) error = %v", err)
	}
	right, err := CanonicalWithNormalizedNumbers([]byte(` { "a" : [ 0.0, 1e-3 ], "z" : 123 } `))
	if err != nil {
		t.Fatalf("CanonicalWithNormalizedNumbers(second) error = %v", err)
	}
	if string(left) != `{"a":[0,0.001],"z":123}` || !bytes.Equal(left, right) {
		t.Fatalf("equal decimal values differ: %s != %s", left, right)
	}
	for name, input := range map[string][]byte{
		"invalid UTF-8":       {0xff},
		"lone high surrogate": []byte(`{"value":"\ud800"}`),
		"lone low surrogate":  []byte(`{"value":"\udc00"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalWithNormalizedNumbers(input); err == nil {
				t.Fatal("strict normalized-number canonicalization accepted invalid Unicode")
			}
		})
	}
}

func TestCanonicalWithNormalizedNumbersBoundsExpansion(t *testing.T) {
	for name, input := range map[string][]byte{
		"large exponent":             []byte(`1e2000000`),
		"int64 exponent overflow":    []byte(`1e99999999999999999999`),
		"negative exponent overflow": []byte(`1e-99999999999999999999`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalWithNormalizedNumbers(input); !errors.Is(err, ErrCanonicalDocumentTooLarge) {
				t.Fatalf("oversize canonical number was not bounded: %v", err)
			}
		})
	}
	zero, err := CanonicalWithNormalizedNumbers([]byte(`0e99999999999999999999`))
	if err != nil || string(zero) != "0" {
		t.Fatalf("large zero exponent = %q, %v", zero, err)
	}
	input := []byte("[" + strings.TrimSuffix(strings.Repeat("1e8191,", 129), ",") + "]")
	if _, err := CanonicalWithNormalizedNumbersLimit(input, 1<<20); !errors.Is(err, ErrCanonicalDocumentTooLarge) {
		t.Fatalf("document-wide numeric expansion was not bounded: %v", err)
	}
	if _, err := CanonicalWithNormalizedNumbersLimit(
		[]byte(`{"same":0,"same":1e99999999999999999999}`), 64*1024,
	); err == nil || errors.Is(err, ErrCanonicalDocumentTooLarge) {
		t.Fatalf("duplicate-key rejection lost precedence over value sizing: %v", err)
	}
}

func TestCanonicalNestedObjectsUseNearLinearWorkAtAdmittedLimits(t *testing.T) {
	for _, boundary := range canonicalBoundaries {
		t.Run(boundary.name, func(t *testing.T) {
			input := maximallyNestedObjectDocument(boundary.maximum)
			canonicalizeNumber := canonicalInteger
			if boundary.normalized {
				canonicalizeNumber = canonicalNormalizedNumber
			}
			canonical, work, err := canonical(input, canonicalizeNumber, boundary.maximum)
			if err != nil {
				t.Fatalf("canonical(maximally nested) error = %v", err)
			}
			if !bytes.Equal(canonical, input) || len(canonical) > boundary.maximum || len(canonical) < boundary.maximum-6 {
				t.Fatalf("canonical bytes = %d, input = %d, maximum = %d", len(canonical), len(input), boundary.maximum)
			}
			if work.renderedBytes != int64(len(canonical)) {
				t.Fatalf("rendered work = %d, want %d", work.renderedBytes, len(canonical))
			}
			if work.units() > int64(3*len(input)) {
				t.Fatalf("deterministic work = %d for %d bytes, want at most 3x", work.units(), len(input))
			}
		})
	}
}

func TestCanonicalNestedObjectAllocationsScaleNearLinearly(t *testing.T) {
	for _, boundary := range canonicalBoundaries {
		t.Run(boundary.name, func(t *testing.T) {
			measure := func(size int) float64 {
				input := maximallyNestedObjectDocument(size)
				var callErr error
				allocations := testing.AllocsPerRun(1, func() {
					if boundary.normalized {
						_, callErr = CanonicalWithNormalizedNumbersLimit(input, boundary.maximum)
					} else {
						_, callErr = CanonicalLimit(input, boundary.maximum)
					}
				})
				if callErr != nil {
					t.Fatalf("canonical(%d bytes) error = %v", len(input), callErr)
				}
				return allocations
			}
			half := measure(boundary.maximum / 2)
			full := measure(boundary.maximum)
			if full > half*2.25 {
				t.Fatalf("allocations grew faster than near-linearly: half=%.0f full=%.0f ratio=%.2f", half, full, full/half)
			}
		})
	}
}

func BenchmarkCanonicalNestedObjectBoundaries(b *testing.B) {
	for _, boundary := range canonicalBoundaries {
		for _, size := range []int{boundary.maximum / 2, boundary.maximum} {
			input := maximallyNestedObjectDocument(size)
			b.Run(fmt.Sprintf("%s/%d-bytes", boundary.name, len(input)), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(input)))
				for b.Loop() {
					var err error
					if boundary.normalized {
						benchmarkCanonicalDocument, err = CanonicalWithNormalizedNumbersLimit(input, boundary.maximum)
					} else {
						benchmarkCanonicalDocument, err = CanonicalLimit(input, boundary.maximum)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
