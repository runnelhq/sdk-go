package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

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
}
