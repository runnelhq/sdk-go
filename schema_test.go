package runnel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSchemaFor(t *testing.T) {
	type inner struct {
		Port uint `json:"port" schema:"default=389"`
	}
	type config struct {
		Embedded
		Mode    string            `json:"mode" doc:"Mode" schema:"enum=a|b,default=a"`
		Only    string            `json:"only" schema:"only=mode:b,group=Advanced"`
		Fields  []string          `json:"fields" schema:"format=runnel-field"`
		Env     map[string]string `json:"env" schema:"format=runnel-secret"`
		Wait    string            `json:"wait" schema:"format=runnel-duration,example=30s"`
		Ratio   float64           `json:"ratio" schema:"example=0.5"`
		Inner   *inner            `json:"inner"`
		Any     any               `json:"any"`
		Skipped string            `json:"skipped" schema:"-"`
		Hidden  string            `json:"-"`
		private string
	}
	s, err := SchemaFor(config{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(s)
	want := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{` +
		`"base":{"type":"string"},` +
		`"mode":{"type":"string","description":"Mode","enum":["a","b"],"default":"a"},` +
		`"only":{"type":"string","x-runnel-only":{"mode":["b"]},"x-runnel-group":"Advanced"},` +
		`"fields":{"type":"array","items":{"type":"string","format":"runnel-field"}},` +
		`"env":{"type":"object","format":"runnel-secret","additionalProperties":{"type":"string","format":"runnel-secret"}},` +
		`"wait":{"type":"string","format":"runnel-duration","examples":["30s"]},` +
		`"ratio":{"type":"number","examples":[0.5]},` +
		`"inner":{"type":"object","properties":{"port":{"type":"integer","default":389,"minimum":0}},"additionalProperties":false},` +
		`"any":{}},"additionalProperties":false}`
	if string(got) != want {
		t.Errorf("schema\n got %s\nwant %s", got, want)
	}
	_ = config{}.private
}

type Embedded struct {
	Base string `json:"base"`
}

func TestSchemaForErrors(t *testing.T) {
	tests := map[string]any{
		"not a struct": 5,
		"unknown option": struct {
			X string `json:"x" schema:"size=3"`
		}{},
		"bad default": struct {
			X int `json:"x" schema:"default=abc"`
		}{},
		"format on int": struct {
			X int `json:"x" schema:"format=runnel-text"`
		}{},
		"only no sibling": struct {
			X string `json:"x" schema:"only=y:a"`
		}{},
		"twice": struct {
			Embedded
			B string `json:"base"`
		}{},
		"map key": struct {
			X map[int]string `json:"x"`
		}{},
		"chan": struct {
			X chan int `json:"x"`
		}{},
	}
	for name, config := range tests {
		if _, err := SchemaFor(config); err == nil {
			t.Errorf("%s: no error", name)
		} else if strings.Contains(err.Error(), "reflect") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
