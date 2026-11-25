package runnel_test

import (
	"context"
	"strings"
	"testing"

	runnel "github.com/runnelhq/sdk-go"
)

// mustPanic runs fn and checks that it panics with a message that contains want.
func mustPanic(t *testing.T, name, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("%s: no panic", name)
			return
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Errorf("%s: panic %v, want %q", name, r, want)
		}
	}()
	fn()
}

func TestNewMistakes(t *testing.T) {
	mustPanic(t, "invalid name", `invalid plugin name "Demo"`, func() { runnel.New("Demo", "1.0.0") })
	mustPanic(t, "no version", "needs a version", func() { runnel.New("demo", "") })
	if p := runnel.New("demo", "1.0.0"); p.Namespace != "demo" || p.Concurrency != runnel.DefaultConcurrency {
		t.Errorf("New = %+v", p)
	}
}

func TestAddTransformMistakes(t *testing.T) {
	run := func(context.Context, *runnel.Stream, struct{}) error { return nil }
	tr := func(typ string) runnel.Transform[struct{}] {
		return runnel.Transform[struct{}]{Type: typ, Label: "x", Description: "x", Run: run}
	}
	tests := []struct {
		name, want string
		fn         func(p *runnel.Plugin)
	}{
		{"invalid type", `invalid transform type "demo"`, func(p *runnel.Plugin) { runnel.AddTransform(p, tr("demo")) }},
		{"outside namespace", `"core.x" is outside the namespace "demo"`, func(p *runnel.Plugin) { runnel.AddTransform(p, tr("core.x")) }},
		{"twice", `"demo.x" added twice`, func(p *runnel.Plugin) {
			runnel.AddTransform(p, tr("demo.x"))
			runnel.AddTransform(p, tr("demo.x"))
		}},
		{"category", `unknown category "misc"`, func(p *runnel.Plugin) {
			d := tr("demo.x")
			d.Category = "misc"
			runnel.AddTransform(p, d)
		}},
		{"no run", "needs a label, a description and Run", func(p *runnel.Plugin) {
			d := tr("demo.x")
			d.Run = nil
			runnel.AddTransform(p, d)
		}},
		{"bad tag", `unknown schema option "bogus"`, func(p *runnel.Plugin) {
			runnel.AddTransform(p, runnel.Transform[struct {
				X int `json:"x" schema:"bogus"`
			}]{Type: "demo.x", Label: "x", Description: "x", Run: func(context.Context, *runnel.Stream, struct {
				X int `json:"x" schema:"bogus"`
			}) error {
				return nil
			}})
		}},
	}
	for _, tt := range tests {
		mustPanic(t, tt.name, tt.want, func() { tt.fn(runnel.New("demo", "1.0.0")) })
	}
}

func TestAddConnectionTypeMistakes(t *testing.T) {
	ct := func(typ string) runnel.ConnectionType[struct{}] {
		return runnel.ConnectionType[struct{}]{Type: typ, Label: "x", Description: "x"}
	}
	tests := []struct {
		name, want string
		fn         func(p *runnel.Plugin)
	}{
		{"outside namespace", `connection type "sql" must be "demo" or start with "demo-"`, func(p *runnel.Plugin) { runnel.AddConnectionType(p, ct("sql")) }},
		{"invalid name", `connection type "demo-X" must be`, func(p *runnel.Plugin) { runnel.AddConnectionType(p, ct("demo-X")) }},
		{"twice", `connection type "demo-a" added twice`, func(p *runnel.Plugin) {
			runnel.AddConnectionType(p, ct("demo-a"))
			runnel.AddConnectionType(p, ct("demo-a"))
		}},
		{"no label", "needs a label and a description", func(p *runnel.Plugin) {
			runnel.AddConnectionType(p, runnel.ConnectionType[struct{}]{Type: "demo"})
		}},
		{"bad tag", `unknown schema option "bogus"`, func(p *runnel.Plugin) {
			runnel.AddConnectionType(p, runnel.ConnectionType[struct {
				Host string `json:"host" schema:"bogus"`
			}]{Type: "demo", Label: "x", Description: "x"})
		}},
	}
	for _, tt := range tests {
		mustPanic(t, tt.name, tt.want, func() { tt.fn(runnel.New("demo", "1.0.0")) })
	}
	// Without Test, describe does not announce connection.test.
	p := runnel.New("demo", "1.0.0")
	runnel.AddConnectionType(p, ct("demo"))
}
