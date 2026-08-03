// Package canonical implements the RFC 8785 JSON and SHA-256 conventions used
// by ctx v1 records.
package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const DigestPrefix = "sha256:"

var ErrMissingContentDigest = errors.New("content_digest is missing")

// DecodeObject decodes one JSON object, preserving json.Number and rejecting
// duplicate keys at every nesting level.
func DecodeObject(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeValue(decoder, "$")
	if err != nil {
		return nil, err
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("expected one JSON object")
		}
		return nil, err
	}
	return record, nil
}

func decodeValue(decoder *json.Decoder, path string) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("expected object key at %s", path)
			}
			if _, duplicate := object[key]; duplicate {
				return nil, fmt.Errorf("duplicate JSON key %q at %s", key, path)
			}
			value, err := decodeValue(decoder, path+"."+key)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for index := 0; decoder.More(); index++ {
			value, err := decodeValue(decoder, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, path)
	}
}

// JSON returns the RFC 8785 canonical JSON encoding of value.
func JSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsoncanonicalizer.Transform(raw)
}

// Digest returns the ctx-formatted SHA-256 digest of canonical JSON.
func Digest(value any) (string, error) {
	canonical, err := JSON(value)
	if err != nil {
		return "", err
	}
	return BytesDigest(canonical), nil
}

// BytesDigest returns the ctx-formatted SHA-256 digest of bytes.
func BytesDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// HandoffBody returns the fixed v1 Markdown body whose bytes are protected by
// handoff.rendered_body_digest.
func HandoffBody(checkpointID, taskID string) []byte {
	return []byte(fmt.Sprintf(
		"# ctx handoff\n\nLoad checkpoint `%s` for task `%s` through `ctx resume`.\n",
		checkpointID,
		taskID,
	))
}

// ContentDigest calculates a record digest after excluding content_digest.
func ContentDigest(record map[string]any) (string, error) {
	withoutDigest := make(map[string]any, len(record))
	for key, value := range record {
		if key != "content_digest" {
			withoutDigest[key] = value
		}
	}
	return Digest(withoutDigest)
}

// VerifyContentDigest rejects records whose stored digest differs from the
// canonical value. It does not mutate record.
func VerifyContentDigest(record map[string]any) error {
	stored, ok := record["content_digest"].(string)
	if !ok || stored == "" {
		return ErrMissingContentDigest
	}
	actual, err := ContentDigest(record)
	if err != nil {
		return err
	}
	if stored != actual {
		return errors.New("content_digest does not match canonical record")
	}
	return nil
}
