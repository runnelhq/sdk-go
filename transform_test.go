package runnel_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	runnel "github.com/runnelhq/sdk-go"
	"github.com/runnelhq/sdk-go/runneltest"
)

type upperConfig struct {
	Field  string `json:"field" doc:"Field to write in capitals" schema:"required,format=runnel-field"`
	Bad    string `json:"bad" doc:"Target of the value route" schema:"format=runnel-target"`
	Factor int    `json:"factor" doc:"Unused number" schema:"default=1"`
}

type countConfig struct {
	Count  int      `json:"count" doc:"Number of records" schema:"default=10"`
	Server demoConn `json:"server" doc:"Connection" schema:"connection=demo"`
}

func newTransformDemo() *runnel.Plugin {
	p := runnel.New("demo", "1.0.0")
	runnel.AddTransform(p, runnel.Transform[upperConfig]{
		Type: "demo.upper", Label: "Upper", Description: "Writes a field in capitals",
		RecordErrors: true, Parallel: true,
		Run: func(ctx context.Context, s *runnel.Stream, c upperConfig) error {
			for {
				b, ok, err := s.Next(ctx)
				if err != nil || !ok {
					return err
				}
				at := b.Fields.Index(c.Field)
				if at < 0 {
					return fmt.Errorf("field %q is not in the input", c.Field)
				}
				var main, routed, bad [][]any
				for _, row := range b.Rows {
					v, _ := row[at].(string)
					switch v {
					case "bad":
						bad = append(bad, runnel.ErrorRow(row, "invalid value bad", c.Field))
					case "route":
						routed = append(routed, row)
					case "fail":
						return errors.New("cannot go on")
					case "panic":
						panic("boom")
					default:
						row[at] = strings.ToUpper(v)
						main = append(main, row)
					}
				}
				if err := s.Emit(ctx, runnel.ToMain, b.Fields, main); err != nil {
					return err
				}
				if err := s.Emit(ctx, c.Bad, b.Fields, routed); err != nil {
					return err
				}
				if err := s.Emit(ctx, runnel.ToError, runnel.ErrorFields(b.Fields), bad); err != nil {
					return err
				}
			}
		},
		Init: func(_ context.Context, in runnel.InitInput, c upperConfig) runnel.InitResult {
			res := runnel.InitResult{Output: &in.Input}
			if !in.Input.Open && in.Input.Fields.Index(c.Field) < 0 {
				res.Problems = append(res.Problems, runnel.Problem{Path: "field", Message: fmt.Sprintf("field %q is not in the input", c.Field)})
			}
			return res
		},
	})
	fields := runnel.Fields{{"n", runnel.TypeInt}, {"name", runnel.TypeString}, {"host", runnel.TypeString}}
	runnel.AddTransform(p, runnel.Transform[countConfig]{
		Type: "demo.count", Category: runnel.CategoryInput, Label: "Count", Description: "Emits numbered records",
		Run: func(ctx context.Context, s *runnel.Stream, c countConfig) error {
			var rows [][]any
			for i := 1; i <= c.Count; i++ {
				rows = append(rows, []any{i, fmt.Sprintf("row %d", i), c.Server.Host})
				if len(rows) == 10 || i == c.Count {
					if err := s.Emit(ctx, runnel.ToMain, fields, rows); err != nil {
						return err
					}
					rows = nil
				}
			}
			// An input gets transform.end at once; Next returns false.
			if _, ok, err := s.Next(ctx); ok || err != nil {
				return fmt.Errorf("input got a batch: %w", err)
			}
			return nil
		},
		Init: func(_ context.Context, _ runnel.InitInput, c countConfig) runnel.InitResult {
			res := runnel.InitResult{Output: &runnel.RecordSchema{Fields: fields}}
			if c.Server.Name != "" && c.Server.Host != "" {
				res.Problems = append(res.Problems, runnel.Problem{Message: "init must not see the connection"})
			}
			return res
		},
		DetectFields: func(_ context.Context, c countConfig, limit int) (runnel.Detection, error) {
			return runnel.Detection{
				Source: []runnel.Fact{{"Server", c.Server.Host}}, Rows: min(c.Count, limit), Complete: c.Count <= limit,
				Fields: []runnel.DetectedField{{Name: "n", Type: runnel.TypeInt, Values: []string{"1"}}, {Name: "name", Type: runnel.TypeString}},
			}, nil
		},
	})
	return p
}

func TestTransformDescribe(t *testing.T) {
	c := runneltest.Start(t, newTransformDemo())
	raw, err := c.Call("describe", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"type":"demo.upper","kind":"transform","label":"Upper"`,
		`"recordErrors":true,"parallel":true,"init":true}`,
		`"field":{"type":"string","description":"Field to write in capitals","format":"runnel-field"}`,
		`"bad":{"type":"string","description":"Target of the value route","format":"runnel-target"}`,
		`"type":"demo.count","kind":"transform","category":"input"`,
		`"init":true,"detectFields":true}`,
		`"actions":[]`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("describe lacks %s:\n%s", want, raw)
		}
	}
}

func TestTransformStream(t *testing.T) {
	c := runneltest.Start(t, newTransformDemo())
	fields := runnel.Fields{{"id", runnel.TypeInt}, {"name", runnel.TypeString}, {"amount", runnel.TypeDecimal}}
	var batches []runnel.Batch
	for i := range 20 { // more batches than the window, so credits must flow back
		batches = append(batches, runnel.Batch{Fields: fields, Rows: [][]any{{i, "a", "1.50"}, {i, "bad", "2"}, {i, "route", "3"}}})
	}
	res, err := c.RunTransform("demo.upper", map[string]any{"field": "name", "bad": "rejected"}, batches...)
	if err != nil || res.Error != "" {
		t.Fatalf("stream: %v %q", err, res.Error)
	}
	main, errs, routed := res.Records(runnel.ToMain), res.Records(runnel.ToError), res.Records("rejected")
	if len(main) != 20 || len(errs) != 20 || len(routed) != 20 {
		t.Fatalf("main %d, error %d, routed %d", len(main), len(errs), len(routed))
	}
	if main[3]["name"] != "A" || main[3]["id"] != 3.0 || main[3]["amount"] != "1.50" {
		t.Errorf("main = %v", main[3])
	}
	if errs[0]["_error"] != "invalid value bad" || errs[0]["_errorField"] != "name" || errs[0]["name"] != "bad" {
		t.Errorf("error = %v", errs[0])
	}

	res, err = c.RunTransform("demo.upper", map[string]any{"field": "name"}, runnel.Batch{Fields: fields, Rows: [][]any{{1, "fail", "1"}}})
	if err != nil || res.Error != "cannot go on" {
		t.Errorf("failing stream: %v %q", err, res.Error)
	}
	res, err = c.RunTransform("demo.upper", map[string]any{"field": "name"}, runnel.Batch{Fields: fields, Rows: [][]any{{1, "panic", "1"}}})
	if err != nil || res.Error != "internal error of the plugin in demo.upper: boom" {
		t.Errorf("panicking stream: %v %q", err, res.Error)
	}
	res, err = c.RunTransform("demo.upper", map[string]any{"field": "other"}, runnel.Batch{Fields: fields, Rows: [][]any{{1, "x", "1"}}})
	if err != nil || res.Error != `field "other" is not in the input` {
		t.Errorf("stream: %v %q", err, res.Error)
	}
	if _, err := c.RunTransform("demo.none", map[string]any{}); err == nil || !strings.Contains(err.Error(), `unknown transform type "demo.none"`) {
		t.Errorf("unknown type: %v", err)
	}
}

func TestTransformInput(t *testing.T) {
	c := runneltest.Start(t, newTransformDemo())
	res, err := c.RunTransform("demo.count", map[string]any{"count": "95", "server": map[string]any{"name": "s", "type": "demo", "host": "example.org"}})
	if err != nil || res.Error != "" {
		t.Fatalf("input: %v %q", err, res.Error)
	}
	recs := res.Records(runnel.ToMain)
	if len(recs) != 95 || recs[94]["n"] != 95.0 || recs[94]["host"] != "example.org" || len(res.Emits) != 10 {
		t.Errorf("records %d, emits %d, last %v", len(recs), len(res.Emits), recs[len(recs)-1])
	}
	if res.Emits[1].Fields == nil {
		t.Error("fields of the second emit lost")
	}
}

func TestTransformParallelStreams(t *testing.T) {
	c := runneltest.Start(t, newTransformDemo())
	done := make(chan error, 3)
	for range 3 {
		go func() {
			res, err := c.RunTransform("demo.count", map[string]any{"count": 200})
			if err == nil && len(res.Records(runnel.ToMain)) != 200 {
				err = fmt.Errorf("%d records", len(res.Records(runnel.ToMain)))
			}
			done <- err
		}()
	}
	for range 3 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func TestTransformInitAndDetect(t *testing.T) {
	c := runneltest.Start(t, newTransformDemo())
	in := runnel.InitInput{Input: runnel.RecordSchema{Fields: runnel.Fields{{"id", runnel.TypeInt}}}}
	res, err := c.Init("demo.upper", map[string]any{"field": "name", "factor": "${FACTOR}"}, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || res.Problems[0].Path != "field" || res.Output == nil || len(res.Output.Fields) != 1 {
		t.Errorf("init = %+v", res)
	}
	// Connections come by name only and secrets empty; the configuration is still decoded.
	res, err = c.Init("demo.count", map[string]any{"count": 5, "server": "s"}, runnel.InitInput{})
	if err != nil || len(res.Problems) != 0 || len(res.Output.Fields) != 3 {
		t.Errorf("init = %+v, %v", res, err)
	}
	d, err := c.DetectFields("demo.count", map[string]any{"count": 5, "server": map[string]any{"name": "s", "type": "demo", "host": "h"}}, 3)
	if err != nil || d.Rows != 3 || d.Complete || d.Source[0].Value != "h" || len(d.Fields) != 2 || d.Fields[1].Values == nil {
		t.Errorf("detect = %+v, %v", d, err)
	}
	if _, err := c.DetectFields("demo.upper", map[string]any{}, 3); err == nil || !strings.Contains(err.Error(), "cannot detect fields") {
		t.Errorf("detect without support: %v", err)
	}
	if _, err := c.Init("demo.none", map[string]any{}, runnel.InitInput{}); err == nil {
		t.Error("init of unknown type")
	}
}

// BenchmarkTransformStream sends batches of 500 records with five fields through
// demo.upper and back (pipes in the test process, without Runnel).
func BenchmarkTransformStream(b *testing.B) {
	c := runneltest.Start(b, newTransformDemo())
	fields := runnel.Fields{{"id", runnel.TypeInt}, {"name", runnel.TypeString}, {"amount", runnel.TypeDecimal}, {"day", runnel.TypeDate}, {"ok", runnel.TypeBool}}
	rows := make([][]any, 500)
	for i := range rows {
		rows[i] = []any{i, "anna", "12.50", "2026-11-24", true}
	}
	batches := make([]runnel.Batch, 20)
	for i := range batches {
		batches[i] = runnel.Batch{Fields: fields, Rows: rows}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		res, err := c.RunTransform("demo.upper", map[string]any{"field": "name"}, batches...)
		if err != nil || res.Error != "" {
			b.Fatal(err, res.Error)
		}
	}
	b.ReportMetric(float64(b.N*20*500)/b.Elapsed().Seconds(), "records/s")
}

// Next returns the values in the Go types of their fields; Emit takes them back.
func TestTransformValueTypes(t *testing.T) {
	p := runnel.New("demo", "1.0.0")
	runnel.AddTransform(p, runnel.Transform[struct{}]{
		Type: "demo.types", Label: "Types", Description: "Writes the Go types of the values",
		Run: func(ctx context.Context, s *runnel.Stream, _ struct{}) error {
			for {
				b, ok, err := s.Next(ctx)
				if err != nil || !ok {
					return err
				}
				var out [][]any
				for _, row := range b.Rows {
					var types []any
					for _, v := range row {
						types = append(types, fmt.Sprintf("%T", v))
					}
					out = append(out, types, row) // the values go back unchanged as well
				}
				if err := s.Emit(ctx, runnel.ToMain, b.Fields, out); err != nil {
					return err
				}
			}
		},
	})
	c := runneltest.Start(t, p)
	at := time.Date(2026, 11, 24, 10, 30, 0, 500, time.FixedZone("", 3600))
	fields := runnel.Fields{{"i", runnel.TypeInt}, {"f", runnel.TypeFloat}, {"d", runnel.TypeDecimal}, {"day", runnel.TypeDate},
		{"at", runnel.TypeDatetime}, {"raw", runnel.TypeBinary}, {"doc", runnel.TypeJSON}, {"any", ""}, {"none", runnel.TypeDatetime}}
	res, err := c.RunTransform("demo.types", map[string]any{}, runnel.Batch{Fields: fields, Rows: [][]any{
		{7, 2, "12.50", "2026-11-24", at, []byte{0, 1, 255}, map[string]any{"k": 1}, 3, nil},
	}})
	if err != nil || res.Error != "" {
		t.Fatal(err, res.Error)
	}
	recs := res.Records(runnel.ToMain)
	want := map[string]any{"i": "int64", "f": "float64", "d": "string", "day": "string", "at": "time.Time", "raw": "[]uint8",
		"doc": "map[string]interface {}", "any": "int64", "none": "<nil>"}
	for k, v := range want {
		if recs[0][k] != v {
			t.Errorf("%s: %v, want %v", k, recs[0][k], v)
		}
	}
	// Back on the wire: datetime as RFC 3339 with offset, binary as Base64.
	if recs[1]["at"] != "2026-11-24T10:30:00.0000005+01:00" || recs[1]["raw"] != "AAH/" || recs[1]["d"] != "12.50" {
		t.Errorf("values back: %v", recs[1])
	}
	for _, tt := range []struct {
		field runnel.Field
		value any
		want  string
	}{
		{runnel.Field{Name: "at", Type: runnel.TypeDatetime}, "yesterday", `row 1 of a batch: field "at": "yesterday" is not a date and time (RFC 3339)`},
		{runnel.Field{Name: "raw", Type: runnel.TypeBinary}, "not base64!", `row 1 of a batch: field "raw": the value is not Base64`},
	} {
		res, err := c.RunTransform("demo.types", map[string]any{}, runnel.Batch{Fields: runnel.Fields{tt.field}, Rows: [][]any{{tt.value}}})
		if err != nil || res.Error != tt.want {
			t.Errorf("%s: %v %q, want %q", tt.field.Name, err, res.Error, tt.want)
		}
	}
}
