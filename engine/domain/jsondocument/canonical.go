// SPDX-License-Identifier: Apache-2.0

// Package jsondocument provides strict, deterministic JSON document helpers
// for engine-owned contracts. It performs no I/O and accepts no duplicate
// object keys or trailing values.
package jsondocument

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maximumCanonicalNumberBytes = 1 << 20

var (
	// ErrInvalidUTF8 rejects byte sequences which encoding/json would
	// otherwise replace with U+FFFD.
	ErrInvalidUTF8 = errors.New("JSON document is not valid UTF-8")
	// ErrInvalidUnicodeSurrogate rejects escaped UTF-16 surrogate halves which
	// encoding/json would otherwise replace with U+FFFD.
	ErrInvalidUnicodeSurrogate = errors.New("JSON string contains an unpaired Unicode surrogate")
	// ErrCanonicalDocumentTooLarge rejects normalized output before exponent
	// expansion can allocate beyond the caller's document-wide byte budget.
	ErrCanonicalDocumentTooLarge = errors.New("canonical JSON document is too large")
)

func hexValue(value byte) (uint16, bool) {
	switch {
	case value >= '0' && value <= '9':
		return uint16(value - '0'), true
	case value >= 'a' && value <= 'f':
		return uint16(value-'a') + 10, true
	case value >= 'A' && value <= 'F':
		return uint16(value-'A') + 10, true
	default:
		return 0, false
	}
}

func unicodeEscape(document []byte, start int) (uint16, bool) {
	if start+4 > len(document) {
		return 0, false
	}
	var value uint16
	for _, digit := range document[start : start+4] {
		part, ok := hexValue(digit)
		if !ok {
			return 0, false
		}
		value = value*16 + part
	}
	return value, true
}

func validateStringEncoding(document []byte) error {
	if !utf8.Valid(document) {
		return ErrInvalidUTF8
	}
	inString := false
	for index := 0; index < len(document); index++ {
		switch document[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(document) {
				continue
			}
			if document[index+1] != 'u' {
				index++
				continue
			}
			first, ok := unicodeEscape(document, index+2)
			if !ok {
				continue
			}
			switch {
			case first >= 0xd800 && first <= 0xdbff:
				if index+12 > len(document) || document[index+6] != '\\' || document[index+7] != 'u' {
					return ErrInvalidUnicodeSurrogate
				}
				second, ok := unicodeEscape(document, index+8)
				if !ok || second < 0xdc00 || second > 0xdfff {
					return ErrInvalidUnicodeSurrogate
				}
				index += 11
			case first >= 0xdc00 && first <= 0xdfff:
				return ErrInvalidUnicodeSurrogate
			default:
				index += 5
			}
		}
	}
	return nil
}

type numberCanonicalizer func(json.Number) (string, error)

type normalizedNumber struct {
	negative bool
	digits   string
	power    int64
	zero     bool
}

func canonicalInteger(value json.Number) (string, error) {
	integer := new(big.Int)
	if _, ok := integer.SetString(value.String(), 10); !ok {
		return "", fmt.Errorf("JSON number %q is not a canonical integer", value)
	}
	return integer.String(), nil
}

func parseNormalizedNumber(number json.Number) (normalizedNumber, error) {
	value := number.String()
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	exponent := int64(0)
	if position := strings.IndexAny(value, "eE"); position >= 0 {
		parsed, err := strconv.ParseInt(value[position+1:], 10, 32)
		if err != nil {
			return normalizedNumber{}, errors.New("JSON number exponent is outside the canonical bound")
		}
		exponent = parsed
		value = value[:position]
	}
	fractionDigits := 0
	if point := strings.IndexByte(value, '.'); point >= 0 {
		fractionDigits = len(value) - point - 1
		value = value[:point] + value[point+1:]
	}
	digits := strings.TrimLeft(value, "0")
	if digits == "" {
		return normalizedNumber{zero: true}, nil
	}
	power := exponent - int64(fractionDigits)
	for strings.HasSuffix(digits, "0") {
		digits = strings.TrimSuffix(digits, "0")
		power++
	}
	return normalizedNumber{negative: negative, digits: digits, power: power}, nil
}

func (number normalizedNumber) canonicalSize() int64 {
	if number.zero {
		return 1
	}
	var size int64
	switch point := int64(len(number.digits)) + number.power; {
	case number.power >= 0:
		size = int64(len(number.digits)) + number.power
	case point > 0:
		size = int64(len(number.digits)) + 1
	default:
		size = 2 - point + int64(len(number.digits))
	}
	if number.negative {
		size++
	}
	return size
}

func canonicalNormalizedNumberSize(number json.Number) (int64, error) {
	normalized, err := parseNormalizedNumber(number)
	if err != nil {
		return 0, err
	}
	return normalized.canonicalSize(), nil
}

func canonicalNormalizedNumberWithin(number json.Number, maximum int64) (string, error) {
	normalized, err := parseNormalizedNumber(number)
	if err != nil {
		return "", err
	}
	if normalized.canonicalSize() > maximum {
		return "", ErrCanonicalDocumentTooLarge
	}
	if normalized.zero {
		return "0", nil
	}
	var canonical string
	switch point := int64(len(normalized.digits)) + normalized.power; {
	case normalized.power >= 0:
		canonical = normalized.digits + strings.Repeat("0", int(normalized.power))
	case point > 0:
		canonical = normalized.digits[:point] + "." + normalized.digits[point:]
	default:
		canonical = "0." + strings.Repeat("0", int(-point)) + normalized.digits
	}
	if normalized.negative {
		canonical = "-" + canonical
	}
	return canonical, nil
}

type canonicalSizeCounter struct {
	maximum int64
	current int64
}

func (counter *canonicalSizeCounter) add(size int64) error {
	if size < 0 || size > counter.maximum-counter.current {
		return ErrCanonicalDocumentTooLarge
	}
	counter.current += size
	return nil
}

func countCanonicalValue(decoder *json.Decoder, counter *canonicalSizeCounter) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			if err := counter.add(1); err != nil {
				return err
			}
			members := make(map[string]struct{})
			for index := 0; decoder.More(); index++ {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return errors.New("JSON object name is not a string")
				}
				if _, duplicate := members[name]; duplicate {
					return fmt.Errorf("duplicate JSON object key %q", name)
				}
				members[name] = struct{}{}
				encodedName, _ := json.Marshal(name)
				separatorSize := int64(len(encodedName) + 1)
				if index > 0 {
					separatorSize++
				}
				if err := counter.add(separatorSize); err != nil {
					return err
				}
				if err := countCanonicalValue(decoder, counter); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("JSON object is not closed")
			}
			return counter.add(1)
		case '[':
			if err := counter.add(1); err != nil {
				return err
			}
			for index := 0; decoder.More(); index++ {
				if index > 0 {
					if err := counter.add(1); err != nil {
						return err
					}
				}
				if err := countCanonicalValue(decoder, counter); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("JSON array is not closed")
			}
			return counter.add(1)
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", value)
		}
	case json.Number:
		size, err := canonicalNormalizedNumberSize(value)
		if err != nil {
			return err
		}
		return counter.add(size)
	case string, bool, nil:
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return counter.add(int64(len(encoded)))
	default:
		return fmt.Errorf("unsupported JSON token %T", token)
	}
}

func validateCanonicalSize(document []byte, maximum int) error {
	if maximum < 0 {
		return ErrCanonicalDocumentTooLarge
	}
	if err := validateStringEncoding(document); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	counter := canonicalSizeCounter{maximum: int64(maximum)}
	if err := countCanonicalValue(decoder, &counter); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func writeCanonicalValue(
	decoder *json.Decoder,
	output *bytes.Buffer,
	canonicalizeNumber numberCanonicalizer,
) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			members := make(map[string][]byte)
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return errors.New("JSON object name is not a string")
				}
				if _, duplicate := members[name]; duplicate {
					return fmt.Errorf("duplicate JSON object key %q", name)
				}
				var member bytes.Buffer
				if err := writeCanonicalValue(decoder, &member, canonicalizeNumber); err != nil {
					return err
				}
				members[name] = member.Bytes()
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("JSON object is not closed")
			}
			names := make([]string, 0, len(members))
			for name := range members {
				names = append(names, name)
			}
			sort.Strings(names)
			output.WriteByte('{')
			for index, name := range names {
				if index > 0 {
					output.WriteByte(',')
				}
				encodedName, _ := json.Marshal(name)
				output.Write(encodedName)
				output.WriteByte(':')
				output.Write(members[name])
			}
			output.WriteByte('}')
			return nil
		case '[':
			output.WriteByte('[')
			for index := 0; decoder.More(); index++ {
				if index > 0 {
					output.WriteByte(',')
				}
				if err := writeCanonicalValue(decoder, output, canonicalizeNumber); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("JSON array is not closed")
			}
			output.WriteByte(']')
			return nil
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", value)
		}
	case json.Number:
		canonical, err := canonicalizeNumber(value)
		if err != nil {
			return err
		}
		output.WriteString(canonical)
		return nil
	case string, bool, nil:
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		output.Write(encoded)
		return nil
	default:
		return fmt.Errorf("unsupported JSON token %T", token)
	}
}

func canonical(document []byte, canonicalizeNumber numberCanonicalizer) ([]byte, error) {
	if err := validateStringEncoding(document); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var canonical bytes.Buffer
	if err := writeCanonicalValue(decoder, &canonical, canonicalizeNumber); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return canonical.Bytes(), nil
}

// Canonical parses one complete JSON value, rejects invalid UTF-8, unpaired
// escaped surrogates, duplicate keys, and non-integer numeric spellings, and
// sorts object keys recursively. Array order remains significant.
func Canonical(document []byte) ([]byte, error) {
	return canonical(document, canonicalInteger)
}

// CanonicalWithNormalizedNumbers applies the same strict string, duplicate-key,
// and document validation as Canonical while accepting every valid JSON number
// spelling and normalizing equal arbitrary-precision decimal values identically.
// It is used for durable command payload identity, where 1.50 and 15e-1 are the
// same value. Canonical retains its integer-only configuration contract.
func CanonicalWithNormalizedNumbers(document []byte) ([]byte, error) {
	return CanonicalWithNormalizedNumbersLimit(document, maximumCanonicalNumberBytes)
}

// CanonicalWithNormalizedNumbersLimit verifies the complete canonical output
// size before rendering any exponent expansion. The maximum is a document-wide
// byte budget, not a per-number allowance.
func CanonicalWithNormalizedNumbersLimit(document []byte, maximum int) ([]byte, error) {
	if err := validateCanonicalSize(document, maximum); err != nil {
		return nil, err
	}
	canonical, err := canonical(document, func(number json.Number) (string, error) {
		return canonicalNormalizedNumberWithin(number, int64(maximum))
	})
	if err != nil {
		return nil, err
	}
	if len(canonical) > maximum {
		return nil, ErrCanonicalDocumentTooLarge
	}
	return canonical, nil
}
