package runnel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// FuzzDecodeRow reads rows of a batch as Runnel sends them: whatever the input, decodeRow
// must not panic, and what it accepts has the Go types of the fields.
func FuzzDecodeRow(f *testing.F) {
	f.Add(`[1,"anna","2026-11-24T10:30:00+01:00","AAH/",1.5,{"k":[1]}]`)
	f.Add(`[9223372036854775808,null,"yesterday","%%",1e400]`)
	f.Add(`[]`)
	f.Add(`[1,2,3,4,5,6,7,8]`)
	fields := Fields{{"i", TypeInt}, {"s", TypeString}, {"at", TypeDatetime}, {"raw", TypeBinary}, {"f", TypeFloat}, {"doc", TypeJSON}}
	f.Fuzz(func(t *testing.T, raw string) {
		row, err := decodeRow(json.RawMessage(raw), fields)
		if err != nil {
			return
		}
		for i, v := range row {
			if v == nil || i >= len(fields) {
				continue
			}
			switch fields[i].Type {
			case TypeDatetime:
				if _, ok := v.(time.Time); !ok {
					if _, isText := v.(string); isText {
						t.Fatalf("datetime %q was accepted as text", v)
					}
				}
			case TypeBinary:
				if _, isText := v.(string); isText {
					t.Fatalf("binary %q was accepted as text", v)
				}
			case TypeFloat:
				if _, isNumber := v.(json.Number); isNumber {
					t.Fatalf("number left as json.Number: %v", v)
				}
			}
		}
	})
}

type fuzzConfig struct {
	Name   string            `json:"name" schema:"required"`
	Count  int               `json:"count"`
	Ratio  float64           `json:"ratio"`
	On     *bool             `json:"on"`
	Tags   []string          `json:"tags"`
	Env    map[string]string `json:"env"`
	Nested struct {
		Port uint `json:"port"`
	} `json:"nested"`
	Conn struct {
		ConnectionInfo
		Host string `json:"host"`
	} `json:"conn" schema:"connection=demo"`
}

// FuzzDecodeConfig decodes configurations as Runnel sends them (with texts from variables
// in number fields) and as transform.init sends them (lenient): no input may panic, and an
// error must not name Go types.
func FuzzDecodeConfig(f *testing.F) {
	f.Add(`{"name":"a","count":"5","ratio":"0.5","on":"true","tags":["x"],"env":{"A":"1"},"nested":{"port":"8080"},"conn":{"name":"c","host":"h"}}`)
	f.Add(`{"count":"${N}","conn":"by-name"}`)
	f.Add(`[1,2]`)
	f.Add(`null`)
	s, err := ConfigSchemaFor(fuzzConfig{})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		var c fuzzConfig
		if err := decode(json.RawMessage(raw), s, &c); err != nil {
			for _, goName := range []string{"main.", "runnel.", "reflect", "*json."} {
				if strings.Contains(err.Error(), goName) {
					t.Fatalf("error names Go types: %v", err)
				}
			}
		}
		var lenient fuzzConfig
		decodeLenient(json.RawMessage(raw), s, &lenient)
	})
}
