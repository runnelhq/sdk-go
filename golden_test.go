package runnel_test

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runnel "github.com/runnelhq/sdk-go"
)

var update = flag.Bool("update", false, "rewrite the golden transcripts in testdata/protocol")

// goldenConfig, goldenConn and newGolden are the plugin of the transcripts; changing them
// changes the transcripts.
type goldenConn struct {
	runnel.ConnectionInfo
	Host     string `json:"host" doc:"Name of the server" schema:"required"`
	Password string `json:"password" doc:"Password" schema:"format=runnel-secret"`
}

type goldenConfig struct {
	Text   string     `json:"text" doc:"Text to return" schema:"required,example=hello"`
	Times  int        `json:"times" doc:"How often" schema:"default=1"`
	Server goldenConn `json:"server" doc:"Connection" schema:"connection=golden"`
}

type goldenFields struct {
	Field string `json:"field" doc:"Field to write in capitals" schema:"required,format=runnel-field"`
	To    string `json:"to" doc:"Target for the value route" schema:"format=runnel-target"`
}

func newGolden() *runnel.Plugin {
	p := runnel.New("golden", "1.0.0")
	runnel.AddAction(p, runnel.Action[goldenConfig]{
		Type: "golden.echo", Label: "Echo", Description: "Returns its text", Example: "type: golden.echo\nwith:\n  text: hello\n",
		Reference: runnel.Reference{
			Outputs: []runnel.OutputField{
				{Name: "output.text", Type: "string", Description: "The text, times times"},
				{Name: "output.host", Type: "string", Description: "Host of the connection server"},
			},
			Errors: "Fails when text is fail.",
			Limits: []string{"times repeats the text at most 1000 times."},
		},
		Run: func(_ context.Context, call *runnel.Call, c goldenConfig) (any, error) {
			switch c.Text {
			case "fail":
				return nil, errors.New("cannot echo")
			case "panic":
				panic("boom")
			}
			return map[string]any{"text": strings.Repeat(c.Text, c.Times), "host": c.Server.Host, "run": call.RunID}, nil
		},
	})
	runnel.AddTransform(p, runnel.Transform[goldenFields]{
		Type: "golden.upper", Label: "Upper", Description: "Writes a field in capitals", RecordErrors: true, Parallel: true,
		Reference: runnel.Reference{
			Outputs: []runnel.OutputField{{Name: "<field>", Type: runnel.TypeString, Description: "The value in capitals"}},
			Records: "Passes every record to the data links; records with the value route go to the target to.",
			Errors:  "Records with the value bad go to the error link.",
		},
		Run: func(ctx context.Context, s *runnel.Stream, c goldenFields) error {
			for {
				b, ok, err := s.Next(ctx)
				if err != nil || !ok {
					return err
				}
				at := b.Fields.Index(c.Field)
				var main, routed, bad [][]any
				for _, row := range b.Rows {
					v, _ := row[at].(string)
					switch v {
					case "bad":
						bad = append(bad, runnel.ErrorRow(row, "invalid value bad", c.Field))
					case "route":
						routed = append(routed, row)
					default:
						row[at] = strings.ToUpper(v)
						main = append(main, row)
					}
				}
				for _, e := range []struct {
					to     string
					fields runnel.Fields
					rows   [][]any
				}{{runnel.ToMain, b.Fields, main}, {c.To, b.Fields, routed}, {runnel.ToError, runnel.ErrorFields(b.Fields), bad}} {
					if err := s.Emit(ctx, e.to, e.fields, e.rows); err != nil {
						return err
					}
				}
			}
		},
		Init: func(_ context.Context, in runnel.InitInput, c goldenFields) runnel.InitResult {
			res := runnel.InitResult{Output: &in.Input}
			if !in.Input.Open && in.Input.Fields.Index(c.Field) < 0 {
				res.Problems = append(res.Problems, runnel.Problem{Path: "field", Message: fmt.Sprintf("field %q is not in the input", c.Field)})
			}
			return res
		},
		DetectFields: func(context.Context, goldenFields, int) (runnel.Detection, error) {
			return runnel.Detection{Rows: 1, Complete: true, Fields: []runnel.DetectedField{{Name: "name", Type: runnel.TypeString, Values: []string{"anna"}}}}, nil
		},
	})
	runnel.AddConnectionType(p, runnel.ConnectionType[goldenConn]{
		Type: "golden", Label: "Golden", Description: "A test service",
		Test: func(_ context.Context, c goldenConn) (runnel.TestResult, error) {
			if c.Host == "down" {
				return runnel.TestResult{}, errors.New("cannot reach down")
			}
			return runnel.TestResult{OK: true, Message: "connected to " + c.Host, Version: "golden 1"}, nil
		},
	})
	return p
}

// TestGoldenTranscripts plays the requests of each transcript in testdata/protocol
// ("→ " lines) to the plugin and compares its answers ("← " lines) with the file, so that
// any change of what goes over the wire shows in the review. Answers that belong to a
// request are what the plugin writes until it is quiet for a moment. With -update the
// answers are written anew; check the diff before committing.
func TestGoldenTranscripts(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "protocol", "*.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no transcripts: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			got := play(t, string(data))
			if *update {
				if err := os.WriteFile(file, []byte(got), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			if want := strings.ReplaceAll(string(data), "\r\n", "\n"); got != want {
				t.Errorf("transcript differs (go test -run TestGoldenTranscripts -update, then check the diff)\n--- got\n%s\n--- want\n%s", got, want)
			}
		})
	}
}

// With -update, the answers to a request are what the plugin writes until it is quiet for
// updateQuiet. Comparing, the test waits for as many answers as the transcript has (each
// within answerTimeout, enough for slow CI machines) and then extraQuiet for more.
const (
	updateQuiet   = 300 * time.Millisecond
	answerTimeout = 10 * time.Second
	extraQuiet    = 50 * time.Millisecond
)

// play sends the "→ " lines of a transcript and returns the transcript with the answers
// the plugin gave; other lines (comments starting with #) stay as they are.
func play(t *testing.T, transcript string) string {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- newGolden().Serve(context.Background(), inR, outW, io.Discard)
		_ = outW.Close()
	}()
	answers := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			answers <- sc.Text()
		}
		close(answers)
	}()
	next := func(wait time.Duration) (string, bool) {
		select {
		case a, ok := <-answers:
			if !ok {
				t.Fatal("the plugin ended")
			}
			return a, true
		case <-time.After(wait):
			return "", false
		}
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(transcript, "\r\n", "\n"), "\n"), "\n")
	var out strings.Builder
	for i, line := range lines {
		if strings.HasPrefix(line, "← ") {
			continue // replaced by the answers the plugin gives now
		}
		out.WriteString(line + "\n")
		if !strings.HasPrefix(line, "→ ") {
			continue
		}
		if _, err := io.WriteString(inW, strings.TrimPrefix(line, "→ ")+"\n"); err != nil {
			t.Fatal(err)
		}
		expected := 0
		for _, l := range lines[i+1:] {
			if !strings.HasPrefix(l, "← ") {
				break
			}
			expected++
		}
		if *update {
			for a, ok := next(updateQuiet); ok; a, ok = next(updateQuiet) {
				out.WriteString("← " + a + "\n")
			}
			continue
		}
		for range expected {
			a, ok := next(answerTimeout)
			if !ok {
				break // the comparison shows what is missing
			}
			out.WriteString("← " + a + "\n")
		}
		for a, ok := next(extraQuiet); ok; a, ok = next(extraQuiet) {
			out.WriteString("← " + a + "\n")
		}
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return out.String()
}
