package runnel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestByteSize(t *testing.T) {
	for n, want := range map[int]string{
		0:            "0 bytes",
		4000:         "4000 bytes",
		1 << 20:      "1 MiB",
		16 << 20:     "16 MiB",
		3 << 19:      "1.5 MiB",
		16<<20 + 300: "16.0 MiB",
	} {
		if got := byteSize(n); got != want {
			t.Errorf("byteSize(%d) = %q, want %q", n, got, want)
		}
	}
}

// Wrong types in a configuration get messages without Go type names, for every kind of
// field.
func TestDecodeTypeMessages(t *testing.T) {
	type nested struct {
		Port int `json:"port"`
	}
	type config struct {
		On     bool              `json:"on"`
		Name   string            `json:"name"`
		Count  uint              `json:"count"`
		Ratio  float64           `json:"ratio"`
		Tags   []string          `json:"tags"`
		Env    map[string]string `json:"env"`
		Nested nested            `json:"nested"`
		Ptr    *bool             `json:"ptr"`
	}
	s, err := ConfigSchemaFor(config{})
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		`{"on":[1]}`:              `field "on": expected true or false, got array`,
		`{"name":{"a":1}}`:        `field "name": expected a text, got object`,
		`{"count":"many"}`:        `field "count": expected a whole number, got string`,
		`{"ratio":"half"}`:        `field "ratio": expected a number, got string`,
		`{"tags":"x"}`:            `field "tags": expected a list, got string`,
		`{"env":["x"]}`:           `field "env": expected a mapping, got array`,
		`{"nested":5}`:            `field "nested": expected a mapping, got number`,
		`{"ptr":"maybe"}`:         `field "ptr": expected true or false, got string`,
		`[1,2]`:                   `configuration: expected a mapping, got array`,
		`{"nested":{"port":"x"}}`: `field "nested.port": expected a whole number, got string`,
	} {
		var c config
		err := decode(json.RawMessage(in), s, &c)
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
	if got := jsonKind(reflect.TypeFor[chan int]()); got != "another value" {
		t.Errorf("jsonKind(chan) = %q", got)
	}
}

func TestLogger(t *testing.T) {
	var b strings.Builder
	l := NewLogger(&b)
	l.Debug("d %d", 1)
	l.Info("i")
	l.Warn("first\r\nsecond\nthird\rfourth")
	l.Error("e")
	want := "DEBUG d 1\nINFO i\nWARN first | second | third | fourth\nERROR e\n"
	if b.String() != want {
		t.Errorf("log\n%q\nwant\n%q", b.String(), want)
	}

	// A line is cut at maxLogLine without breaking a UTF-8 character.
	b.Reset()
	l.Info("%s", strings.Repeat("ä", maxLogLine))
	line := strings.TrimSuffix(b.String(), "\n")
	if !strings.HasSuffix(line, "…") || len(line) > maxLogLine+len("INFO ")+len("…") || !utf8.ValidString(line) {
		t.Errorf("long line: %d bytes, valid %v", len(line), utf8.ValidString(line))
	}

	// A nil Logger or one without writer does nothing.
	var none *Logger
	none.Info("x")
	(&Logger{}).Error("y")
}
