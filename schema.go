package runnel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Draft is the JSON Schema dialect of all generated schemas.
const Draft = "https://json-schema.org/draft/2020-12/schema"

// Formats of text fields that Runnel and its editor understand (docs/dev/PLUGINS.md in the
// Runnel repository). Set them with the schema option format=, e.g. format=runnel-duration.
const (
	FormatDuration   = "runnel-duration"   // a time span such as 30s, 5m or 1h30m
	FormatText       = "runnel-text"       // a text of several lines
	FormatSecret     = "runnel-secret"     // a password or token; only such fields may use ${secret:…}
	FormatConnection = "runnel-connection" // the name of a connection; see the option connection=
)

// ConfigSchema is a JSON ConfigSchema, limited to what configurations of actions and connection types
// need. Properties keep the order of the struct fields, which is the order the editor
// shows them in.
type ConfigSchema struct {
	Schema      string        `json:"$schema,omitempty"`
	Type        string        `json:"type,omitempty"`
	Description string        `json:"description,omitempty"`
	Format      string        `json:"format,omitempty"`
	Enum        []any         `json:"enum,omitempty"`
	Default     any           `json:"default,omitempty"`
	Examples    []any         `json:"examples,omitempty"`
	Minimum     *int          `json:"minimum,omitempty"`
	Items       *ConfigSchema `json:"items,omitempty"`
	Properties  Properties    `json:"properties,omitempty"`
	Required    []string      `json:"required,omitempty"`
	// Only: the field applies only when the sibling field has one of the values.
	Only map[string][]any `json:"x-runnel-only,omitempty"`
	// Connection: the connection types a field of format runnel-connection accepts.
	Connection []string `json:"x-runnel-connection,omitempty"`
	// Group names the section of a form the field is shown in.
	Group string `json:"x-runnel-group,omitempty"`
	// AdditionalProperties is false for structs (unknown fields are errors) and the
	// schema of the values for maps.
	AdditionalProperties any `json:"additionalProperties,omitempty"`
}

// Property is a named field of an object schema.
type Property struct {
	Name   string
	Schema *ConfigSchema
}

// Properties are the fields of an object schema in the order of the struct.
type Properties []Property

// MarshalJSON writes the properties as a JSON object, keeping their order.
func (ps Properties) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, p := range ps {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(p.Name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(p.Schema)
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Lookup returns the schema of a property.
func (ps Properties) Lookup(name string) (*ConfigSchema, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p.Schema, true
		}
	}
	return nil, false
}

// ConfigSchemaFor returns the JSON Schema of a configuration struct, the same way Runnel derives
// the schemas of its built-in actions (ADR 0014). config is a struct value or a pointer to
// one; nil yields an object without properties.
//
// The fields describe themselves with tags:
//
//		type checkConfig struct {
//			IBAN string `json:"iban" doc:"The IBAN to check" schema:"required,example=DE02120300000000202051"`
//		}
//
//	  - json: the name of the field in the workflow file; fields without one, with json:"-"
//	    and unexported fields are not part of the schema.
//	  - doc: a description for editor and tools (English, one sentence).
//	  - schema: comma-separated options: required, default=<value>, enum=<a>|<b>|…,
//	    example=<value>, format=<name>, group=<name>, only=<field>:<a>|<b>|…,
//	    connection=<type>|<type>|… (the name of a connection of these types; the field may be
//	    a struct, which receives the resolved connection). schema:"-" decodes the field but
//	    leaves it out of the schema (as in ConnectionInfo). Values must not contain commas.
func ConfigSchemaFor(config any) (*ConfigSchema, error) {
	s := &ConfigSchema{Type: "object", AdditionalProperties: false}
	if config != nil {
		t := reflect.TypeOf(config)
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return nil, fmt.Errorf("configuration must be a struct, not %s", t)
		}
		var err error
		if s, err = schemaOf(t, "", nil); err != nil {
			return nil, err
		}
	}
	s.Schema = Draft
	return s, nil
}

func schemaOf(t reflect.Type, path string, seen []reflect.Type) (*ConfigSchema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return &ConfigSchema{Type: "string"}, nil
	case reflect.Bool:
		return &ConfigSchema{Type: "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &ConfigSchema{Type: "integer"}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		zero := 0
		return &ConfigSchema{Type: "integer", Minimum: &zero}, nil
	case reflect.Float32, reflect.Float64:
		return &ConfigSchema{Type: "number"}, nil
	case reflect.Interface:
		return &ConfigSchema{}, nil // any value
	case reflect.Slice, reflect.Array:
		items, err := schemaOf(t.Elem(), path+"[]", seen)
		if err != nil {
			return nil, err
		}
		return &ConfigSchema{Type: "array", Items: items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%s: map keys must be strings", describePath(path))
		}
		values, err := schemaOf(t.Elem(), path+"{}", seen)
		if err != nil {
			return nil, err
		}
		return &ConfigSchema{Type: "object", AdditionalProperties: values}, nil
	case reflect.Struct:
		if slices.Contains(seen, t) {
			return nil, fmt.Errorf("%s: recursive type %s", describePath(path), t)
		}
		return schemaOfStruct(t, path, append(seen, t))
	default:
		return nil, fmt.Errorf("%s: unsupported type %s", describePath(path), t)
	}
}

func schemaOfStruct(t reflect.Type, path string, seen []reflect.Type) (*ConfigSchema, error) {
	s := &ConfigSchema{Type: "object", AdditionalProperties: false}
	for _, f := range fieldsOf(t) {
		fieldPath := joinPath(path, f.name)
		if f.options.err != nil {
			return nil, fmt.Errorf("%s: %w", describePath(fieldPath), f.options.err)
		}
		var fs *ConfigSchema
		if len(f.options.connection) > 0 {
			// The file names the connection; the plugin receives it resolved.
			fs = &ConfigSchema{Type: "string"}
		} else {
			var err error
			if fs, err = schemaOf(f.typ, fieldPath, seen); err != nil {
				return nil, err
			}
		}
		fs.Description = f.doc
		if err := applyOptions(fs, f.options, fieldPath); err != nil {
			return nil, err
		}
		if _, dup := s.Properties.Lookup(f.name); dup {
			return nil, fmt.Errorf("%s: field name used twice", describePath(fieldPath))
		}
		s.Properties = append(s.Properties, Property{f.name, fs})
		if f.options.required {
			s.Required = append(s.Required, f.name)
		}
	}
	for _, p := range s.Properties {
		for sibling := range p.Schema.Only {
			if _, ok := s.Properties.Lookup(sibling); !ok || sibling == p.Name {
				return nil, fmt.Errorf("%s: only= names %q, which is not another field", describePath(joinPath(path, p.Name)), sibling)
			}
		}
	}
	return s, nil
}

func applyOptions(s *ConfigSchema, o fieldOptions, path string) error {
	s.Group = o.group
	if o.only != "" {
		field, values, ok := strings.Cut(o.only, ":")
		if !ok || field == "" || values == "" {
			return fmt.Errorf("%s: only must be <field>:<value>|<value>…", describePath(path))
		}
		s.Only = map[string][]any{}
		for v := range strings.SplitSeq(values, "|") {
			s.Only[field] = append(s.Only[field], v)
		}
	}
	if len(o.connection) > 0 {
		if o.format != "" && o.format != FormatConnection {
			return fmt.Errorf("%s: connection= does not go with another format", describePath(path))
		}
		s.Connection, o.format = o.connection, FormatConnection
	}
	if o.format != "" {
		switch {
		case s.Type == "string":
			s.Format = o.format
		case s.Type == "array" && s.Items != nil && s.Items.Type == "string":
			s.Items.Format = o.format
		case s.Type == "object" && isTextMap(s.AdditionalProperties):
			s.Format = o.format
			s.AdditionalProperties.(*ConfigSchema).Format = o.format
		default:
			return fmt.Errorf("%s: format is only allowed for strings, lists of strings and mappings of strings", describePath(path))
		}
	}
	if o.defaultValue != nil {
		v, err := parseValue(s.Type, *o.defaultValue)
		if err != nil {
			return fmt.Errorf("%s: default: %w", describePath(path), err)
		}
		s.Default = v
	}
	if o.example != nil {
		v, err := parseValue(s.Type, *o.example)
		if err != nil {
			return fmt.Errorf("%s: example: %w", describePath(path), err)
		}
		s.Examples = []any{v}
	}
	for _, e := range o.enum {
		v, err := parseValue(s.Type, e)
		if err != nil {
			return fmt.Errorf("%s: enum: %w", describePath(path), err)
		}
		s.Enum = append(s.Enum, v)
	}
	return nil
}

func isTextMap(additional any) bool {
	s, ok := additional.(*ConfigSchema)
	return ok && s.Type == "string"
}

// parseValue converts a tag value to the JSON type of the field.
func parseValue(typ, text string) (any, error) {
	switch typ {
	case "string":
		return text, nil
	case "boolean":
		return strconv.ParseBool(text)
	case "integer":
		return strconv.ParseInt(text, 10, 64)
	case "number":
		return strconv.ParseFloat(text, 64)
	}
	return nil, fmt.Errorf("values are only supported for strings, numbers and booleans, not %q", typ)
}

type field struct {
	name    string
	doc     string
	typ     reflect.Type
	options fieldOptions
}

type fieldOptions struct {
	required     bool
	defaultValue *string
	example      *string
	enum         []string
	format       string
	only         string
	group        string
	connection   []string
	err          error
}

// fieldsOf returns the fields of a struct that belong into its schema, including those of
// embedded structs, in declaration order.
func fieldsOf(t reflect.Type) []field {
	var out []field
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				out = append(out, fieldsOf(et)...)
				continue
			}
		}
		if !f.IsExported() || name == "" || name == "-" || f.Tag.Get("schema") == "-" {
			continue
		}
		out = append(out, field{name: name, doc: f.Tag.Get("doc"), typ: f.Type, options: parseOptions(f.Tag.Get("schema"))})
	}
	return out
}

func parseOptions(tag string) fieldOptions {
	var o fieldOptions
	if tag == "" {
		return o
	}
	for opt := range strings.SplitSeq(tag, ",") {
		key, value, hasValue := strings.Cut(opt, "=")
		switch {
		case key == "required" && !hasValue:
			o.required = true
		case key == "default" && hasValue:
			o.defaultValue = &value
		case key == "example" && hasValue:
			o.example = &value
		case key == "enum" && hasValue:
			o.enum = strings.Split(value, "|")
		case key == "format" && hasValue:
			o.format = value
		case key == "only" && hasValue:
			o.only = value
		case key == "group" && hasValue:
			o.group = value
		case key == "connection" && hasValue:
			o.connection = strings.Split(value, "|")
		default:
			o.err = fmt.Errorf("unknown schema option %q", opt)
		}
	}
	return o
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func describePath(path string) string {
	if path == "" {
		return "configuration"
	}
	return fmt.Sprintf("field %q", path)
}
