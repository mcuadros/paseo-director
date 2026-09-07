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
)

func writeCanonicalValue(decoder *json.Decoder, output *bytes.Buffer) error {
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
				if err := writeCanonicalValue(decoder, &member); err != nil {
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
				if err := writeCanonicalValue(decoder, output); err != nil {
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
		integer := new(big.Int)
		if _, ok := integer.SetString(value.String(), 10); !ok {
			return fmt.Errorf("JSON number %q is not a canonical integer", value)
		}
		output.WriteString(integer.String())
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

// Canonical parses one complete JSON value, rejects duplicate keys and
// non-integer numeric spellings, and sorts object keys recursively. Array order
// remains significant.
func Canonical(document []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var canonical bytes.Buffer
	if err := writeCanonicalValue(decoder, &canonical); err != nil {
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
