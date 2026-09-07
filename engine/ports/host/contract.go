// SPDX-License-Identifier: Apache-2.0

// Package host owns the single versioned interface implemented by host connectors.
package host

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"sort"
)

//go:embed host-interface.v1.json
var embeddedSchema []byte

// Definition is the generator-facing portion of the engine-owned host schema.
type Definition struct {
	SchemaVersion   int      `json:"schemaVersion"`
	ContractVersion string   `json:"contractVersion"`
	CredentialScope string   `json:"credentialScope"`
	Capabilities    []string `json:"capabilities"`
}

// Descriptor is the complete information a connector may advertise at handshake.
type Descriptor struct {
	CredentialScope string   `json:"credentialScope"`
	ContractVersion string   `json:"contractVersion"`
	ContractHash    string   `json:"contractHash"`
	Capabilities    []string `json:"capabilities"`
}

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
			return fmt.Errorf("contract JSON number %q is not a canonicalizable integer", value)
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

// CanonicalJSON parses a contract document, rejects duplicate keys, and emits
// a whitespace- and object-key-order-independent representation.
func CanonicalJSON(schema []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(schema))
	decoder.UseNumber()
	var canonical bytes.Buffer
	if err := writeCanonicalValue(decoder, &canonical); err != nil {
		return nil, fmt.Errorf("canonicalize host contract: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("canonicalize host contract: trailing JSON value")
		}
		return nil, fmt.Errorf("canonicalize host contract: %w", err)
	}
	return canonical.Bytes(), nil
}

// CanonicalSHA256 hashes the parsed, duplicate-safe canonical contract.
func CanonicalSHA256(schema []byte) (string, error) {
	canonical, err := CanonicalJSON(schema)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// ParseDefinition validates the closed metadata needed by the engine and generators.
func ParseDefinition(schema []byte) (Definition, error) {
	canonical, err := CanonicalJSON(schema)
	if err != nil {
		return Definition{}, err
	}
	var definition Definition
	if err := json.Unmarshal(canonical, &definition); err != nil {
		return Definition{}, fmt.Errorf("decode host contract: %w", err)
	}
	if definition.SchemaVersion != 1 {
		return Definition{}, fmt.Errorf("unsupported host schema version %d", definition.SchemaVersion)
	}
	if definition.ContractVersion == "" || definition.CredentialScope == "" {
		return Definition{}, errors.New("host contract version and credential scope are required")
	}
	if len(definition.Capabilities) == 0 {
		return Definition{}, errors.New("host contract must declare capabilities")
	}
	seen := make(map[string]struct{}, len(definition.Capabilities))
	for _, capability := range definition.Capabilities {
		if capability == "" {
			return Definition{}, errors.New("host capability cannot be empty")
		}
		if _, exists := seen[capability]; exists {
			return Definition{}, fmt.Errorf("duplicate host capability %q", capability)
		}
		seen[capability] = struct{}{}
	}
	return definition, nil
}

// EmbeddedDefinition returns a defensive copy of the embedded contract metadata.
func EmbeddedDefinition() (Definition, error) {
	definition, err := ParseDefinition(embeddedSchema)
	if err != nil {
		return Definition{}, err
	}
	definition.Capabilities = slices.Clone(definition.Capabilities)
	return definition, nil
}

// SchemaSHA256 returns the canonical lowercase SHA-256 of the embedded schema.
func SchemaSHA256() (string, error) {
	return CanonicalSHA256(embeddedSchema)
}

// ExpectedDescriptor builds the only descriptor accepted by this contract version.
func ExpectedDescriptor() (Descriptor, error) {
	definition, err := EmbeddedDefinition()
	if err != nil {
		return Descriptor{}, err
	}
	contractHash, err := SchemaSHA256()
	if err != nil {
		return Descriptor{}, err
	}
	return Descriptor{
		CredentialScope: definition.CredentialScope,
		ContractVersion: definition.ContractVersion,
		ContractHash:    contractHash,
		Capabilities:    slices.Clone(definition.Capabilities),
	}, nil
}

// ValidateDescriptor fails closed on a missing, stale, extra, or reordered field.
func ValidateDescriptor(actual Descriptor) error {
	expected, err := ExpectedDescriptor()
	if err != nil {
		return err
	}
	if actual.CredentialScope != expected.CredentialScope {
		return errors.New("host credential scope mismatch")
	}
	if actual.ContractVersion != expected.ContractVersion {
		return errors.New("host contract version mismatch")
	}
	if actual.ContractHash != expected.ContractHash {
		return errors.New("host contract hash mismatch")
	}
	if !slices.Equal(actual.Capabilities, expected.Capabilities) {
		return errors.New("host capability set mismatch")
	}
	return nil
}
