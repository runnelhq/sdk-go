package runnel

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// decode converts the configuration Runnel sent (JSON) into the struct of the action or
// connection type. Texts in fields of type integer, number or boolean are converted first:
// after Runnel resolves ${…} and {{ … }}, a number from a variable may arrive as text.
func decode(data json.RawMessage, s *ConfigSchema, target any) error {
	if len(data) == 0 || string(data) == "null" {
		data = json.RawMessage("{}")
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	raw = coerce(s, raw)
	norm, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if err := json.Unmarshal(norm, target); err != nil {
		return explain(err)
	}
	return nil
}

func coerce(s *ConfigSchema, v any) any {
	if s == nil {
		return v
	}
	switch val := v.(type) {
	case string:
		if s.Format == FormatConnection {
			// transform.init sends connections by name only.
			return map[string]any{"name": val}
		}
		text := strings.TrimSpace(val)
		switch s.Type {
		case "integer":
			if n, err := strconv.ParseInt(text, 10, 64); err == nil {
				return n
			}
		case "number":
			if n, err := strconv.ParseFloat(text, 64); err == nil {
				return n
			}
		case "boolean":
			if b, err := strconv.ParseBool(text); err == nil {
				return b
			}
		}
	case map[string]any:
		if s.Format == FormatConnection {
			return val // a resolved connection: its fields belong to the connection type
		}
		for k, item := range val {
			if ps, ok := s.Properties.Lookup(k); ok {
				val[k] = coerce(ps, item)
			} else if as, ok := s.AdditionalProperties.(*ConfigSchema); ok {
				val[k] = coerce(as, item)
			}
		}
	case []any:
		for i, item := range val {
			val[i] = coerce(s.Items, item)
		}
	}
	return v
}

// decodeLenient is decode for transform.init, where values may still be ${…}: a field
// whose value does not fit is left at its zero value.
func decodeLenient(data json.RawMessage, s *ConfigSchema, target any) {
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return
	}
	coerce(s, raw)
	for range len(raw) + 1 {
		norm, err := json.Marshal(raw)
		if err != nil {
			return
		}
		err = json.Unmarshal(norm, target)
		te, ok := errors.AsType[*json.UnmarshalTypeError](err)
		if err == nil || !ok || te.Field == "" {
			return
		}
		field, _, _ := strings.Cut(te.Field, ".")
		if _, found := raw[field]; !found {
			return
		}
		delete(raw, field)
		reflect.ValueOf(target).Elem().SetZero()
	}
}

// explain turns errors of encoding/json into messages without Go type names.
func explain(err error) error {
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		where := "configuration"
		if te.Field != "" {
			where = fmt.Sprintf("field %q", te.Field)
		}
		return fmt.Errorf("%s: expected %s, got %s", where, jsonKind(te.Type), te.Value)
	}
	return fmt.Errorf("invalid configuration: %w", err)
}

func jsonKind(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "a text"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map, reflect.Struct:
		return "a mapping"
	}
	return "another value"
}
