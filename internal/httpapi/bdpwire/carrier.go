package bdpwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"unicode/utf8"
)

// validateCarrier runs before encoding/json can replace malformed Unicode.
// It also rejects duplicate decoded names inside open properties and extensions.
func validateUnicodeCarrier(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("bdpwire: invalid UTF-8 JSON carrier")
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		i++
		for i < len(data) && data[i] != '"' {
			if data[i] == '\\' {
				i++
				if i >= len(data) {
					break
				}
				if data[i] == 'u' {
					if i+4 >= len(data) {
						return fmt.Errorf("bdpwire: incomplete Unicode escape")
					}
					n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
					if err != nil {
						return err
					}
					i += 4
					if n >= 0xd800 && n <= 0xdbff {
						if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
							return fmt.Errorf("bdpwire: unpaired high surrogate")
						}
						low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
						if err != nil || low < 0xdc00 || low > 0xdfff {
							return fmt.Errorf("bdpwire: unpaired high surrogate")
						}
						i += 6
					} else if n >= 0xdc00 && n <= 0xdfff {
						return fmt.Errorf("bdpwire: unpaired low surrogate")
					}
				}
			}
			i++
		}
	}
	return nil
}

func validateCarrier(data []byte) error {
	if err := validateUnicodeCarrier(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value func() error
	value = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			names := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("bdpwire: non-string member")
				}
				if names[name] {
					return fmt.Errorf("bdpwire: duplicate member %q", name)
				}
				names[name] = true
				if err := value(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := value(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("bdpwire: unexpected delimiter")
		}
		_, err = dec.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("bdpwire: trailing JSON data")
	}
	return nil
}

// validateGoCarrier rejects invalid strings before Marshal can repair them.
func validateGoCarrier(v any) error {
	var walk func(reflect.Value, int) error
	walk = func(v reflect.Value, depth int) error {
		if depth > 1000 {
			return fmt.Errorf("bdpwire: cyclic or excessively nested value")
		}
		if !v.IsValid() {
			return nil
		}
		if v.Type() == rawMessageType {
			if v.IsNil() {
				return nil
			}
			return validateCarrier(v.Bytes())
		}
		switch v.Kind() {
		case reflect.String:
			if !utf8.ValidString(v.String()) {
				return fmt.Errorf("bdpwire: invalid UTF-8 string")
			}
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				return walk(v.Elem(), depth+1)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).IsExported() {
					if err := walk(v.Field(i), depth+1); err != nil {
						return err
					}
				}
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				if err := walk(iter.Key(), depth+1); err != nil {
					return err
				}
				if err := walk(iter.Value(), depth+1); err != nil {
					return err
				}
			}
		case reflect.Array, reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(reflect.ValueOf(v), 0)
}
