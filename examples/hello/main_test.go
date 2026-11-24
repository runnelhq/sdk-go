package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	runnel "github.com/runnelhq/sdk-go"
	"github.com/runnelhq/sdk-go/runneltest"
)

func TestGreet(t *testing.T) {
	c := runneltest.Start(t, newPlugin())
	var out greetOutput
	if err := c.ExecuteInto("hello.greet", map[string]any{"name": "World", "greeting": "Moin"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Message != "Moin, World!" || out.Length != 12 {
		t.Errorf("output = %+v", out)
	}
	if _, err := c.Execute("hello.greet", map[string]any{"name": " "}); err == nil || !strings.Contains(err.Error(), "name is empty") {
		t.Errorf("err = %v", err)
	}
}

func TestGreetRecords(t *testing.T) {
	c := runneltest.Start(t, newPlugin())
	fields := runnel.Fields{{Name: "id", Type: runnel.TypeInt}, {Name: "name", Type: runnel.TypeString}}
	res, err := c.RunTransform("hello.greet", map[string]any{"field": "name", "greeting": "Moin"},
		runnel.Batch{Fields: fields, Rows: [][]any{{1, "Anna"}, {2, ""}, {3, "Ben"}}})
	if err != nil || res.Error != "" {
		t.Fatal(err, res.Error)
	}
	main, errs := res.Records(runnel.Main), res.Records(runnel.Error)
	if len(main) != 2 || main[1]["greeting"] != "Moin, Ben!" || len(errs) != 1 || errs[0]["_errorField"] != "name" {
		t.Errorf("main %v, errors %v", main, errs)
	}
	init, err := c.Init("hello.greet", map[string]any{"field": "nom"}, runnel.InitInput{Input: runnel.RecordSchema{Fields: fields}})
	if err != nil || len(init.Problems) != 1 || init.Problems[0].Path != "field" || len(init.Output.Fields) != 3 {
		t.Errorf("init = %+v, %v", init, err)
	}
}

func TestVersionMatchesPluginYAML(t *testing.T) {
	data, err := os.ReadFile("plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^version:\s*(\S+)`).FindSubmatch(data)
	if m == nil || string(m[1]) != version {
		t.Errorf("plugin.yaml has version %q, the program %q", m, version)
	}
}

// TestWithRunnel runs the plugin in a workflow of Runnel (needs RUNNEL_BIN).
func TestWithRunnel(t *testing.T) {
	p := runneltest.NewProject(t, ".", "plugin.yaml")
	p.WriteFile("workflows/greet.workflow.yaml", `kind: Workflow
apiVersion: runnel/v1
name: greet
outputs:
  - name: MESSAGE
    value: "{{ results.greet.output.message }}"
actions:
  - id: start
    type: core.start
  - id: greet
    type: hello.greet
    with:
      name: Runnel
      shout: "${SHOUT}"
links:
  - { from: start, to: greet }
`)
	if out, code := p.Validate(); code != 0 {
		t.Fatalf("validate: exit %d\n%s", code, out)
	}
	out, code := p.Run("workflows/greet.workflow.yaml", "SHOUT=true")
	if code != 0 || !strings.Contains(out, `output MESSAGE = "HELLO, RUNNEL!"`) || !strings.Contains(out, "hello: greeting Runnel") {
		t.Fatalf("run: exit %d\n%s", code, out)
	}

	// The transform in a pipeline: 2,000 records through the plugin and its error link.
	p.WriteFile("pipelines/greet.pipeline.yaml", `kind: Pipeline
apiVersion: runnel/v1
name: greet
transforms:
  - id: rows
    type: core.generate-rows
    with:
      rows: "2000"
      rowNumberField: n
      fields:
        - { name: name, value: Ada }
  - id: greet
    type: hello.greet
    with:
      field: name
  - id: write
    type: csv.output
    with:
      path: greetings.csv
      fields: [{ name: n }, { name: greeting }]
links:
  - { from: rows, to: greet }
  - { from: greet, to: write }
`)
	if out, code := p.Validate(); code != 0 {
		t.Fatalf("validate: exit %d\n%s", code, out)
	}
	out, code = p.Run("pipelines/greet.pipeline.yaml")
	if code != 0 {
		t.Fatalf("pipeline: exit %d\n%s", code, out)
	}
	csv, err := os.ReadFile(filepath.Join(p.Dir, "greetings.csv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(string(csv), "\ufeff")), "\n")
	if len(lines) != 2001 || strings.TrimSpace(lines[2000]) != "2000,\"Hello, Ada!\"" {
		t.Fatalf("csv has %d lines, last %q", len(lines), lines[len(lines)-1])
	}
	// init: a field that is not in the input is an error of the check.
	p.WriteFile("pipelines/greet.pipeline.yaml", strings.Replace(mustRead(t, filepath.Join(p.Dir, "pipelines", "greet.pipeline.yaml")), "field: name", "field: nom", 1))
	if out, code := p.Validate(); code == 0 || !strings.Contains(out, `field "nom" is not in the input`) {
		t.Fatalf("validate with wrong field: exit %d\n%s", code, out)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
