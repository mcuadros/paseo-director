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

type canonicalNumber struct {
	integer    string
	normalized normalizedNumber
	isDecimal  bool
}

type numberCanonicalizer func(json.Number, int64) (canonicalNumber, int64, error)

type normalizedNumber struct {
	negative bool
	digits   string
	power    int64
	zero     bool
}

func canonicalInteger(value json.Number, maximum int64) (canonicalNumber, int64, error) {
	integer := new(big.Int)
	if _, ok := integer.SetString(value.String(), 10); !ok {
		return canonicalNumber{}, 0, fmt.Errorf("JSON number %q is not a canonical integer", value)
	}
	canonical := integer.String()
	if int64(len(canonical)) > maximum {
		return canonicalNumber{}, 0, ErrCanonicalDocumentTooLarge
	}
	return canonicalNumber{integer: canonical}, int64(len(canonical)), nil
}

func parseNormalizedNumber(number json.Number) (normalizedNumber, error) {
	value := number.String()
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	exponentText := ""
	if position := strings.IndexAny(value, "eE"); position >= 0 {
		exponentText = value[position+1:]
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
	exponent := int64(0)
	if exponentText != "" {
		parsed, err := strconv.ParseInt(exponentText, 10, 64)
		if err != nil {
			// The JSON decoder has already accepted the exponent grammar. An
			// int64 overflow therefore means only that the rendered decimal
			// magnitude cannot fit any supported document budget.
			return normalizedNumber{}, ErrCanonicalDocumentTooLarge
		}
		exponent = parsed
	}
	if exponent < (-1<<63)+int64(fractionDigits) {
		return normalizedNumber{}, ErrCanonicalDocumentTooLarge
	}
	power := exponent - int64(fractionDigits)
	for strings.HasSuffix(digits, "0") {
		digits = strings.TrimSuffix(digits, "0")
		if power == 1<<63-1 {
			return normalizedNumber{}, ErrCanonicalDocumentTooLarge
		}
		power++
	}
	return normalizedNumber{negative: negative, digits: digits, power: power}, nil
}

func (number normalizedNumber) canonicalSizeWithin(maximum int64) (int64, error) {
	counter := canonicalSizeCounter{maximum: maximum}
	if number.zero {
		if err := counter.add(1); err != nil {
			return 0, err
		}
		return counter.current, nil
	}
	if number.negative {
		if err := counter.add(1); err != nil {
			return 0, err
		}
	}
	switch point := int64(len(number.digits)) + number.power; {
	case number.power >= 0:
		if err := counter.add(int64(len(number.digits))); err != nil {
			return 0, err
		}
		if err := counter.add(number.power); err != nil {
			return 0, err
		}
	case point > 0:
		if err := counter.add(int64(len(number.digits)) + 1); err != nil {
			return 0, err
		}
	default:
		if point == -1<<63 {
			return 0, ErrCanonicalDocumentTooLarge
		}
		if err := counter.add(2); err != nil {
			return 0, err
		}
		if err := counter.add(-point); err != nil {
			return 0, err
		}
		if err := counter.add(int64(len(number.digits))); err != nil {
			return 0, err
		}
	}
	return counter.current, nil
}

func canonicalNormalizedNumber(number json.Number, maximum int64) (canonicalNumber, int64, error) {
	normalized, err := parseNormalizedNumber(number)
	if err != nil {
		return canonicalNumber{}, 0, err
	}
	size, err := normalized.canonicalSizeWithin(maximum)
	if err != nil {
		return canonicalNumber{}, 0, err
	}
	return canonicalNumber{normalized: normalized, isDecimal: true}, size, nil
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

type canonicalValueKind uint8

const (
	canonicalScalar canonicalValueKind = iota
	canonicalNumberValue
	canonicalArray
	canonicalObject
)

type canonicalValue struct {
	kind    canonicalValueKind
	encoded []byte
	number  canonicalNumber
	array   []canonicalValue
	object  []canonicalMember
	size    int64
}

type canonicalMember struct {
	name        string
	encodedName []byte
	value       canonicalValue
}

// canonicalWork is deterministic algorithmic evidence used by the package
// regressions. It counts parsed values, sort comparisons, and bytes written to
// the one final output buffer; it is deliberately not a host-wide memory or
// wall-clock measurement.
type canonicalWork struct {
	inputBytes      int64
	values          int64
	sortComparisons int64
	renderedBytes   int64
}

func (work canonicalWork) units() int64 {
	return work.inputBytes + work.values + work.sortComparisons + work.renderedBytes
}

func parseCanonicalValue(
	decoder *json.Decoder,
	canonicalizeNumber numberCanonicalizer,
	maximum int64,
	work *canonicalWork,
) (canonicalValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return canonicalValue{}, err
	}
	work.values++
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			counter := canonicalSizeCounter{maximum: maximum}
			if err := counter.add(2); err != nil {
				return canonicalValue{}, err
			}
			var members []canonicalMember
			var seenNames map[string]struct{}
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return canonicalValue{}, err
				}
				name, ok := nameToken.(string)
				if !ok {
					return canonicalValue{}, errors.New("JSON object name is not a string")
				}
				switch {
				case len(members) == 1 && seenNames == nil:
					if members[0].name == name {
						return canonicalValue{}, fmt.Errorf("duplicate JSON object key %q", name)
					}
					seenNames = map[string]struct{}{members[0].name: {}, name: {}}
				case seenNames != nil:
					if _, duplicate := seenNames[name]; duplicate {
						return canonicalValue{}, fmt.Errorf("duplicate JSON object key %q", name)
					}
					seenNames[name] = struct{}{}
				}
				encodedName, _ := json.Marshal(name)
				memberValue, err := parseCanonicalValue(decoder, canonicalizeNumber, maximum, work)
				if err != nil {
					return canonicalValue{}, err
				}
				if len(members) > 0 {
					if err := counter.add(1); err != nil {
						return canonicalValue{}, err
					}
				}
				if err := counter.add(int64(len(encodedName) + 1)); err != nil {
					return canonicalValue{}, err
				}
				if err := counter.add(memberValue.size); err != nil {
					return canonicalValue{}, err
				}
				members = append(members, canonicalMember{name: name, encodedName: encodedName, value: memberValue})
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return canonicalValue{}, errors.New("JSON object is not closed")
			}
			if len(members) > 1 {
				sort.Slice(members, func(left, right int) bool {
					work.sortComparisons++
					return members[left].name < members[right].name
				})
				for index := 1; index < len(members); index++ {
					if members[index-1].name == members[index].name {
						return canonicalValue{}, fmt.Errorf("duplicate JSON object key %q", members[index].name)
					}
				}
			}
			return canonicalValue{kind: canonicalObject, object: members, size: counter.current}, nil
		case '[':
			counter := canonicalSizeCounter{maximum: maximum}
			if err := counter.add(2); err != nil {
				return canonicalValue{}, err
			}
			var values []canonicalValue
			for index := 0; decoder.More(); index++ {
				if index > 0 {
					if err := counter.add(1); err != nil {
						return canonicalValue{}, err
					}
				}
				item, err := parseCanonicalValue(decoder, canonicalizeNumber, maximum, work)
				if err != nil {
					return canonicalValue{}, err
				}
				if err := counter.add(item.size); err != nil {
					return canonicalValue{}, err
				}
				values = append(values, item)
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return canonicalValue{}, errors.New("JSON array is not closed")
			}
			return canonicalValue{kind: canonicalArray, array: values, size: counter.current}, nil
		default:
			return canonicalValue{}, fmt.Errorf("unexpected JSON delimiter %q", value)
		}
	case json.Number:
		number, size, err := canonicalizeNumber(value, maximum)
		if err != nil {
			return canonicalValue{}, err
		}
		return canonicalValue{kind: canonicalNumberValue, number: number, size: size}, nil
	case string, bool, nil:
		encoded, err := json.Marshal(value)
		if err != nil {
			return canonicalValue{}, err
		}
		if int64(len(encoded)) > maximum {
			return canonicalValue{}, ErrCanonicalDocumentTooLarge
		}
		return canonicalValue{kind: canonicalScalar, encoded: encoded, size: int64(len(encoded))}, nil
	default:
		return canonicalValue{}, fmt.Errorf("unsupported JSON token %T", token)
	}
}

func writeCanonicalBytes(output *bytes.Buffer, value []byte, work *canonicalWork) {
	output.Write(value)
	work.renderedBytes += int64(len(value))
}

func writeCanonicalString(output *bytes.Buffer, value string, work *canonicalWork) {
	output.WriteString(value)
	work.renderedBytes += int64(len(value))
}

func writeCanonicalByte(output *bytes.Buffer, value byte, work *canonicalWork) {
	output.WriteByte(value)
	work.renderedBytes++
}

func writeCanonicalZeroes(output *bytes.Buffer, count int64, work *canonicalWork) {
	const zeroes = "0000000000000000000000000000000000000000000000000000000000000000"
	for count > 0 {
		current := int64(len(zeroes))
		if count < current {
			current = count
		}
		writeCanonicalString(output, zeroes[:current], work)
		count -= current
	}
}

func writeCanonicalNumber(output *bytes.Buffer, number canonicalNumber, work *canonicalWork) {
	if !number.isDecimal {
		writeCanonicalString(output, number.integer, work)
		return
	}
	normalized := number.normalized
	if normalized.zero {
		writeCanonicalByte(output, '0', work)
		return
	}
	if normalized.negative {
		writeCanonicalByte(output, '-', work)
	}
	switch point := int64(len(normalized.digits)) + normalized.power; {
	case normalized.power >= 0:
		writeCanonicalString(output, normalized.digits, work)
		writeCanonicalZeroes(output, normalized.power, work)
	case point > 0:
		writeCanonicalString(output, normalized.digits[:point], work)
		writeCanonicalByte(output, '.', work)
		writeCanonicalString(output, normalized.digits[point:], work)
	default:
		writeCanonicalString(output, "0.", work)
		writeCanonicalZeroes(output, -point, work)
		writeCanonicalString(output, normalized.digits, work)
	}
}

func writeCanonicalValue(value canonicalValue, output *bytes.Buffer, work *canonicalWork) {
	switch value.kind {
	case canonicalScalar:
		writeCanonicalBytes(output, value.encoded, work)
	case canonicalNumberValue:
		writeCanonicalNumber(output, value.number, work)
	case canonicalArray:
		writeCanonicalByte(output, '[', work)
		for index, item := range value.array {
			if index > 0 {
				writeCanonicalByte(output, ',', work)
			}
			writeCanonicalValue(item, output, work)
		}
		writeCanonicalByte(output, ']', work)
	case canonicalObject:
		writeCanonicalByte(output, '{', work)
		for index, member := range value.object {
			if index > 0 {
				writeCanonicalByte(output, ',', work)
			}
			writeCanonicalBytes(output, member.encodedName, work)
			writeCanonicalByte(output, ':', work)
			writeCanonicalValue(member.value, output, work)
		}
		writeCanonicalByte(output, '}', work)
	}
}

func canonical(
	document []byte,
	canonicalizeNumber numberCanonicalizer,
	maximum int,
) ([]byte, canonicalWork, error) {
	work := canonicalWork{inputBytes: int64(len(document))}
	if maximum < 0 {
		return nil, work, ErrCanonicalDocumentTooLarge
	}
	if err := validateStringEncoding(document); err != nil {
		return nil, work, err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	value, err := parseCanonicalValue(decoder, canonicalizeNumber, int64(maximum), &work)
	if err != nil {
		return nil, work, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, work, errors.New("trailing JSON value")
		}
		return nil, work, err
	}
	var output bytes.Buffer
	output.Grow(int(value.size))
	writeCanonicalValue(value, &output, &work)
	if int64(output.Len()) != value.size {
		return nil, work, errors.New("canonical JSON size changed during rendering")
	}
	return output.Bytes(), work, nil
}

// Canonical parses one complete JSON value, rejects invalid UTF-8, unpaired
// escaped surrogates, duplicate keys, and non-integer numeric spellings, and
// sorts object keys recursively. Array order remains significant.
func Canonical(document []byte) ([]byte, error) {
	maximum := int(^uint(0) >> 1)
	canonical, _, err := canonical(document, canonicalInteger, maximum)
	return canonical, err
}

// CanonicalLimit applies Canonical's integer-only contract and refuses a
// canonical representation larger than the caller's document-wide byte
// budget before allocating the final output buffer.
func CanonicalLimit(document []byte, maximum int) ([]byte, error) {
	canonical, _, err := canonical(document, canonicalInteger, maximum)
	return canonical, err
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
	canonical, _, err := canonical(document, canonicalNormalizedNumber, maximum)
	return canonical, err
}
