package runnel_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runnel "github.com/runnelhq/sdk-go"
	"github.com/runnelhq/sdk-go/runneltest"
)

type demoConn struct {
	runnel.ConnectionInfo
	Host     string `json:"host" doc:"Name of the server" schema:"required"`
	Password string `json:"password" doc:"Password" schema:"format=runnel-secret"`
}

type echoConfig struct {
	Text   string   `json:"text" doc:"Text to return" schema:"required,example=hello"`
	Times  int      `json:"times" doc:"How often" schema:"default=1"`
	Loud   bool     `json:"loud" doc:"Upper case"`
	Server demoConn `json:"server" doc:"Connection" schema:"connection=demo"`
}

func newDemo() *runnel.Plugin {
	p := runnel.New("demo", "1.2.3")
	p.Concurrency = 2
	var active, peak atomic.Int32
	runnel.AddAction(p, runnel.Action[echoConfig]{
		Type: "demo.echo", Label: "Echo", Description: "Returns its text",
		Run: func(ctx context.Context, call *runnel.Call, c echoConfig) (any, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			switch c.Text {
			case "fail":
				return nil, errors.New("cannot echo")
			case "panic":
				panic("boom")
			case "hang":
				<-ctx.Done()
				return nil, ctx.Err()
			case "sleep":
				time.Sleep(100 * time.Millisecond)
				return peak.Load(), nil
			}
			call.Log.Warn("echo %s\nsecond line", c.Text)
			return map[string]any{"text": strings.Repeat(c.Text, c.Times), "loud": c.Loud, "host": c.Server.Host, "conn": c.Server.Name, "run": call.RunID}, nil
		},
	})
	runnel.AddConnectionType(p, runnel.ConnectionType[demoConn]{
		Type: "demo", Label: "Demo", Description: "A demo service",
		Test: func(_ context.Context, c demoConn) (runnel.TestResult, error) {
			if c.Host == "down" {
				return runnel.TestResult{}, errors.New("cannot reach down")
			}
			return runnel.TestResult{OK: true, Message: "connected to " + c.Host, Version: "demo 1"}, nil
		},
	})
	return p
}

func TestInitializeAndDescribe(t *testing.T) {
	c := runneltest.Start(t, newDemo())
	if got := c.Initialized; got.ProtocolVersion != 1 || got.Encoding != "json" || got.Name != "demo" || got.Version != "1.2.3" || got.Concurrency != 2 {
		t.Fatalf("initialize = %+v", got)
	}
	d, err := c.Describe()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Actions) != 1 || d.Actions[0].Type != "demo.echo" || d.Actions[0].Kind != "action" || d.Actions[0].Category != "general" {
		t.Fatalf("actions = %+v", d.Actions)
	}
	wantAction := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{` +
		`"text":{"type":"string","description":"Text to return","examples":["hello"]},` +
		`"times":{"type":"integer","description":"How often","default":1},` +
		`"loud":{"type":"boolean","description":"Upper case"},` +
		`"server":{"type":"string","description":"Connection","format":"runnel-connection","x-runnel-connection":["demo"]}},` +
		`"required":["text"],"additionalProperties":false}`
	if string(d.Actions[0].Schema) != wantAction {
		t.Errorf("action schema\n got %s\nwant %s", d.Actions[0].Schema, wantAction)
	}
	if len(d.ConnectionTypes) != 1 || d.ConnectionTypes[0].Type != "demo" || !d.ConnectionTypes[0].Test {
		t.Fatalf("connection types = %+v", d.ConnectionTypes)
	}
	wantConn := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{` +
		`"host":{"type":"string","description":"Name of the server"},` +
		`"password":{"type":"string","description":"Password","format":"runnel-secret"}},` +
		`"required":["host"],"additionalProperties":false}`
	if string(d.ConnectionTypes[0].Schema) != wantConn {
		t.Errorf("connection schema\n got %s\nwant %s", d.ConnectionTypes[0].Schema, wantConn)
	}
}

func TestExecute(t *testing.T) {
	c := runneltest.Start(t, newDemo())
	var out map[string]any
	err := c.ExecuteInto("demo.echo", map[string]any{
		"text": "ab", "times": "2", "loud": "true", // texts from variables are converted
		"server": map[string]any{"name": "srv", "type": "demo", "host": "example.org", "password": "x"},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out["text"] != "abab" || out["loud"] != true || out["host"] != "example.org" || out["conn"] != "srv" || out["run"] != "01TESTRUN" {
		t.Errorf("output = %v", out)
	}
	if !c.LogContains("WARN echo ab | second line") {
		t.Errorf("log = %q", c.Log())
	}
}

func TestExecuteErrors(t *testing.T) {
	c := runneltest.Start(t, newDemo())
	tests := []struct {
		name   string
		typ    string
		config any
		code   int
		msg    string
	}{
		{"action error", "demo.echo", map[string]any{"text": "fail"}, runnel.CodeActionFailed, "cannot echo"},
		{"panic", "demo.echo", map[string]any{"text": "panic"}, runnel.CodeActionFailed, "internal error of the plugin in demo.echo: boom"},
		{"wrong type", "demo.echo", map[string]any{"text": "a", "times": "many"}, runnel.CodeActionFailed, `field "times": expected a whole number, got string`},
		{"unknown type", "demo.other", map[string]any{}, runnel.CodeInvalidParams, `unknown action type "demo.other"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.Execute(tt.typ, tt.config)
			var rerr *runneltest.Error
			if !errors.As(err, &rerr) || rerr.Code != tt.code || rerr.Message != tt.msg {
				t.Fatalf("err = %v, want %q (%d)", err, tt.msg, tt.code)
			}
		})
	}
	// The plugin still works after the panic.
	if _, err := c.Execute("demo.echo", map[string]any{"text": "ok"}); err != nil {
		t.Fatal(err)
	}
	if !c.LogContains("ERROR panic in demo.echo: boom") {
		t.Errorf("log = %q", c.Log())
	}
}

func TestCancel(t *testing.T) {
	c := runneltest.Start(t, newDemo())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.ExecuteContext(ctx, "demo.echo", map[string]any{"text": "hang"}, runneltest.Context{})
	var rerr *runneltest.Error
	if !errors.As(err, &rerr) || rerr.Code != runnel.CodeRequestCancelled {
		t.Fatalf("err = %v, want code -32800", err)
	}
}

func TestConcurrency(t *testing.T) {
	c := runneltest.Start(t, newDemo())
	results := make(chan json.RawMessage, 4)
	for range 4 {
		go func() {
			out, err := c.Execute("demo.echo", map[string]any{"text": "sleep"})
			if err != nil {
				t.Error(err)
			}
			results <- out
		}()
	}
	for range 4 {
		if out := <-results; string(out) == "3" || string(out) == "4" {
			t.Errorf("more than 2 actions at the same time: %s", out)
		}
	}
}

func TestConnectionTest(t *testing.T) {
	c := runneltest.Start(t, newDemo())
	res, err := c.TestConnection(map[string]any{"name": "srv", "type": "demo", "host": "example.org"})
	if err != nil || !res.OK || res.Message != "connected to example.org" || res.Version != "demo 1" {
		t.Errorf("test = %+v, %v", res, err)
	}
	res, err = c.TestConnection(map[string]any{"name": "srv", "type": "demo", "host": "down"})
	if err != nil || res.OK || res.Message != "cannot reach down" {
		t.Errorf("test = %+v, %v", res, err)
	}
}

// serveLines runs Serve with the given input lines and returns the answers.
func serveLines(t *testing.T, p *runnel.Plugin, lines ...string) ([]map[string]any, error) {
	t.Helper()
	var out bytes.Buffer
	err := p.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, io.Discard)
	var answers []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		if jerr := json.Unmarshal(sc.Bytes(), &m); jerr != nil {
			t.Fatalf("invalid answer %q", sc.Text())
		}
		answers = append(answers, m)
	}
	return answers, err
}

func errorCode(m map[string]any) float64 {
	e, _ := m["error"].(map[string]any)
	code, _ := e["code"].(float64)
	return code
}

func TestProtocolRules(t *testing.T) {
	answers, err := serveLines(t, newDemo(),
		`{"jsonrpc":"2.0","id":1,"method":"describe","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":1}}`+"\r",
		`not json`,
		`{"jsonrpc":"2.0","method":"$/unknown","params":{}}`,
		``,
		`{"jsonrpc":"2.0","id":3,"method":"transform.unknown","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"shutdown","params":{}}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 5 {
		t.Fatalf("answers = %v", answers)
	}
	if errorCode(answers[0]) != runnel.CodeNotInitialized {
		t.Errorf("before initialize: %v", answers[0])
	}
	if answers[1]["result"] == nil {
		t.Errorf("initialize: %v", answers[1])
	}
	if errorCode(answers[2]) != runnel.CodeParseError || answers[2]["id"] != nil {
		t.Errorf("invalid JSON: %v", answers[2])
	}
	if errorCode(answers[3]) != runnel.CodeMethodNotFound || answers[3]["id"] != 3.0 {
		t.Errorf("unknown method: %v", answers[3])
	}
	if r, ok := answers[4]["result"]; !ok || r != nil || answers[4]["id"] != 4.0 {
		t.Errorf("shutdown: %v", answers[4])
	}
}

func TestMessageTooLong(t *testing.T) {
	_, err := serveLines(t, newDemo(), strings.Repeat("x", runnel.MaxMessageSize+10))
	if err == nil || !strings.Contains(err.Error(), "message longer than") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefinitionMistakesPanic(t *testing.T) {
	run := func(context.Context, *runnel.Call, struct{}) (any, error) { return nil, nil }
	tests := map[string]func(p *runnel.Plugin){
		"outside namespace": func(p *runnel.Plugin) {
			runnel.AddAction(p, runnel.Action[struct{}]{Type: "core.echo", Label: "x", Description: "x", Run: run})
		},
		"twice": func(p *runnel.Plugin) {
			runnel.AddAction(p, runnel.Action[struct{}]{Type: "demo.x", Label: "x", Description: "x", Run: run})
			runnel.AddAction(p, runnel.Action[struct{}]{Type: "demo.x", Label: "x", Description: "x", Run: run})
		},
		"category": func(p *runnel.Plugin) {
			runnel.AddAction(p, runnel.Action[struct{}]{Type: "demo.x", Category: "misc", Label: "x", Description: "x", Run: run})
		},
		"no run": func(p *runnel.Plugin) {
			runnel.AddAction(p, runnel.Action[struct{}]{Type: "demo.x", Label: "x", Description: "x"})
		},
		"bad tag": func(p *runnel.Plugin) {
			runnel.AddAction(p, runnel.Action[struct {
				X int `json:"x" schema:"bogus"`
			}]{Type: "demo.x", Label: "x", Description: "x", Run: func(context.Context, *runnel.Call, struct {
				X int `json:"x" schema:"bogus"`
			}) (any, error) {
				return nil, nil
			}})
		},
		"connection type name": func(p *runnel.Plugin) {
			runnel.AddConnectionType(p, runnel.ConnectionType[struct{}]{Type: "sql", Label: "x", Description: "x"})
		},
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			fn(runnel.New("demo", "1.0.0"))
		})
	}
}

func FuzzServe(f *testing.F) {
	f.Add(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"action.execute","params":{"type":"demo.echo","config":{"text":"x","times":"3"}}}`)
	f.Add(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":[1]}}`)
	f.Add(`{"id":{},"method":5}`)
	f.Fuzz(func(t *testing.T, input string) {
		p := newDemo()
		var out bytes.Buffer
		init := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}` + "\n"
		_ = p.Serve(context.Background(), strings.NewReader(init+input), &out, io.Discard)
		sc := bufio.NewScanner(&out)
		sc.Buffer(nil, runnel.MaxMessageSize*2)
		for sc.Scan() {
			if !json.Valid(sc.Bytes()) {
				t.Fatalf("invalid answer %q", sc.Text())
			}
		}
	})
}
