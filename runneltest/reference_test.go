package runneltest_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	runnel "github.com/runnelhq/sdk-go"
	"github.com/runnelhq/sdk-go/runneltest"
)

// recorder collects the errors of CheckReference instead of failing the test.
type recorder struct {
	testing.TB
	errors []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

type config struct {
	Field  string `json:"field" doc:"Field to read"`
	Fields []struct {
		Name string `json:"name" doc:"Name of the new field"`
	} `json:"fields" doc:"New fields"`
}

func plugin(action, transform runnel.Reference) *runnel.Plugin {
	p := runnel.New("demo", "1.0.0")
	runnel.AddAction(p, runnel.Action[config]{
		Type: "demo.act", Label: "Act", Description: "Acts", Reference: action,
		Run: func(context.Context, *runnel.Call, config) (any, error) { return nil, nil },
	})
	runnel.AddTransform(p, runnel.Transform[config]{
		Type: "demo.map", Label: "Map", Description: "Maps", Reference: transform,
		Run: func(context.Context, *runnel.Stream, config) error { return nil },
	})
	return p
}

func TestCheckReferenceGood(t *testing.T) {
	r := &recorder{TB: t}
	runneltest.CheckReference(r, plugin(
		runnel.Reference{
			Outputs: []runnel.OutputField{
				{Name: "output.valid", Type: "boolean", Description: "Whether it worked"},
				{Name: "output[].<field>", Type: "string", Description: "Value of the field"},
			},
			Errors: "Fails when the field is empty.",
			Limits: []string{"At most 100 entries."},
		},
		runnel.Reference{
			Outputs: []runnel.OutputField{{Name: "<fields[].name>", Type: runnel.TypeString, Description: "A new field"}},
			Records: "Passes every record to the data links.",
			Errors:  "Records without the field go to the error link.",
		},
	))
	if len(r.errors) > 0 {
		t.Errorf("errors for a good reference:\n%s", strings.Join(r.errors, "\n"))
	}
}

func TestCheckReferenceMistakes(t *testing.T) {
	r := &recorder{TB: t}
	runneltest.CheckReference(r, plugin(
		runnel.Reference{
			Outputs: []runnel.OutputField{
				{Name: "valid", Type: "boolean", Description: "Whether it worked."},
				{Name: "output.<nothing>", Type: "string", Description: "Value"},
				{Name: "output.<nothing>", Type: "string", Description: "Value"},
			},
			Limits: []string{"no period"},
		},
		runnel.Reference{
			Outputs: []runnel.OutputField{{Name: "<fields[].nope>", Type: "any", Description: ""}},
			Errors:  "Never fails",
		},
	))
	want := []string{
		`action demo.act: missing Errors`,
		`action demo.act: Limits "no period" must be English sentences ending with a period`,
		`action demo.act: output "valid": description "Whether it worked." must be one phrase without a final period`,
		`action demo.act: output "valid" must be named by its path`,
		`action demo.act: output "output.<nothing>": "nothing" is no field of the configuration`,
		`action demo.act: output "output.<nothing>" is described twice`,
		`transform demo.map: Errors "Never fails" must be English sentences ending with a period`,
		`transform demo.map: missing Records`,
		`transform demo.map: output "<fields[].nope>": description "" must be one phrase`,
		`transform demo.map: output "<fields[].nope>": "fields[].nope" is no field of the configuration`,
	}
	all := strings.Join(r.errors, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("missing error %q in:\n%s", w, all)
		}
	}
}
