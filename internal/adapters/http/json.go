package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// encoding/json otherwise silently chooses the last duplicate field and matches
// struct field names without regard to case. Reject those ambiguities before
// decoding gateway DTOs; allow new, nonconflicting fields for service evolution.
func decodeObject(data []byte, out any) error {
	if !utf8.Valid(data) {
		return errors.New("invalid UTF-8 in service response")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("service response must be a JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := jsonValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing service response data")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("malformed service response")
	}
	return nil
}

func jsonValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("service JSON exceeds nesting limit")
	}
	token, err := d.Token()
	if err != nil {
		return errors.New("malformed service JSON")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return errors.New("malformed JSON key")
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid JSON key")
			}
			name = strings.ToLower(name)
			if seen[name] {
				return errors.New("duplicate service JSON field")
			}
			seen[name] = true
			if err := jsonValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for d.More() {
			if err := jsonValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}
