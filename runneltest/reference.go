package runneltest

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	runnel "github.com/runnelhq/sdk-go"
)

// CheckReference checks the references of all actions and transforms of a plugin with the
// rules Runnel applies to its own types (ADR 0014, E15b): Errors for every type, Records for
// every transform and only there, Records, Errors and Limits as English sentences with a
// final period, descriptions of outputs as one phrase without, outputs of actions named by
// their path below output (output, output.valid, output[].dn) and names from the
// configuration ("<countryField>", "<fields[].name>") that exist in its schema. Whether an
// action with a result describes it in Outputs is up to the test of the plugin. Calling it
// is optional; Runnel shows references without checking them.
func CheckReference(t testing.TB, p *runnel.Plugin) {
	t.Helper()
	raw, err := Start(t, p).Call("describe", map[string]any{})
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	type entry struct {
		Type   string         `json:"type"`
		Schema map[string]any `json:"schema"`
		runnel.Reference
	}
	var d struct {
		Actions    []entry `json:"actions"`
		Transforms []entry `json:"transforms"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, a := range d.Actions {
		checkReference(t, "action", a.Type, a.Reference, a.Schema)
	}
	for _, tr := range d.Transforms {
		checkReference(t, "transform", tr.Type, tr.Reference, tr.Schema)
	}
}

func checkReference(t testing.TB, kind, typ string, ref runnel.Reference, schema map[string]any) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf("%s %s: "+format, append([]any{kind, typ}, args...)...)
	}
	sentence := func(what, text string) {
		t.Helper()
		if strings.TrimSpace(text) == "" || !strings.HasSuffix(text, ".") {
			fail("%s %q must be English sentences ending with a period", what, text)
		}
	}
	if ref.Errors == "" {
		fail("missing Errors: when does it fail (and what goes to the error link)?")
	} else {
		sentence("Errors", ref.Errors)
	}
	switch {
	case kind == "transform" && ref.Records == "":
		fail("missing Records: which records does it pass on, over which links?")
	case kind == "transform":
		sentence("Records", ref.Records)
	case ref.Records != "":
		fail("Records is for transforms only")
	}
	for _, l := range ref.Limits {
		sentence("Limits", l)
	}
	types := runnel.OutputTypes()
	if kind == "transform" {
		types = []string{runnel.TypeString, runnel.TypeInt, runnel.TypeFloat, runnel.TypeDecimal, runnel.TypeBool,
			runnel.TypeDate, runnel.TypeDatetime, runnel.TypeBinary, runnel.TypeJSON, "any"}
	}
	var names []string
	for _, o := range ref.Outputs {
		if slices.Contains(names, o.Name) {
			fail("output %q is described twice", o.Name)
		}
		names = append(names, o.Name)
		if !slices.Contains(types, o.Type) {
			fail("output %q: type %q is not one of %v", o.Name, o.Type, types)
		}
		if strings.TrimSpace(o.Description) == "" || strings.HasSuffix(o.Description, ".") {
			fail("output %q: description %q must be one phrase without a final period", o.Name, o.Description)
		}
		if kind == "action" && o.Name != "output" && !strings.HasPrefix(o.Name, "output.") && !strings.HasPrefix(o.Name, "output[]") {
			fail("output %q must be named by its path, e.g. output, output.valid or output[].dn", o.Name)
		}
		for rest := o.Name; strings.Contains(rest, "<"); {
			_, after, _ := strings.Cut(rest, "<")
			path, after, ok := strings.Cut(after, ">")
			if !ok || !hasField(schema, path) {
				fail("output %q: %q is no field of the configuration", o.Name, path)
			}
			rest = after
		}
	}
}

// hasField reports whether a path like "fields[].name" names a field of a JSON Schema.
func hasField(s map[string]any, path string) bool {
	for part := range strings.SplitSeq(path, ".") {
		name, list := strings.CutSuffix(part, "[]")
		props, _ := s["properties"].(map[string]any)
		next, _ := props[name].(map[string]any)
		if next == nil {
			return false
		}
		if list {
			if next, _ = next["items"].(map[string]any); next == nil {
				return false
			}
		}
		s = next
	}
	return true
}
