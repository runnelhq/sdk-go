package runnel_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	runnel "github.com/runnelhq/sdk-go"
	"github.com/runnelhq/sdk-go/runneltest"
)

// A host that sends more batches than the window must not lose records silently: the
// stream ends with an error.
func TestStreamTooManyBatches(t *testing.T) {
	p := runnel.New("demo", "1.0.0")
	runnel.AddTransform(p, runnel.Transform[struct{}]{
		Type: "demo.stall", Label: "Stall", Description: "Reads nothing until cancelled",
		Run: func(ctx context.Context, _ *runnel.Stream, _ struct{}) error {
			<-ctx.Done()
			return errors.New("must not be reported, the cause is")
		},
	})
	lines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"transform.open","params":{"stream":7,"type":"demo.stall","config":{},"emitWindow":4}}`,
	}
	for range runnel.StreamWindow + 1 {
		lines = append(lines, `{"jsonrpc":"2.0","method":"transform.batch","params":{"stream":7,"fields":[{"name":"a"}],"rows":[[1]]}}`)
	}
	pr, pw := io.Pipe()
	var out safeBuffer
	done := make(chan error, 1)
	go func() { done <- p.Serve(context.Background(), pr, &out, io.Discard) }()
	_, _ = pw.Write([]byte(strings.Join(lines, "\n") + "\n"))
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "transform.closed") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := `{"jsonrpc":"2.0","method":"transform.closed","params":{"error":{"message":"more than 4 batches arrived from Runnel without waiting for $/ack; the records cannot be processed completely"},"stream":7}}`
	if !strings.Contains(out.String(), want) {
		t.Errorf("output\n%s\nwant %s", out.String(), want)
	}
}

type bigConfig struct {
	Rows int `json:"rows"`
	Size int `json:"size"`
}

func newBig() *runnel.Plugin {
	p := runnel.New("demo", "1.0.0")
	fields := runnel.Fields{{Name: "n", Type: runnel.TypeInt}, {Name: "text", Type: runnel.TypeString}}
	runnel.AddTransform(p, runnel.Transform[bigConfig]{
		Type: "demo.big", Category: runnel.CategoryInput, Label: "Big", Description: "Emits large records",
		Run: func(ctx context.Context, s *runnel.Stream, c bigConfig) error {
			rows := make([][]any, c.Rows)
			for i := range rows {
				rows[i] = []any{i, strings.Repeat("x", c.Size)}
			}
			return s.Emit(ctx, runnel.Main, fields, rows)
		},
	})
	runnel.AddAction(p, runnel.Action[bigConfig]{
		Type: "demo.big", Label: "Big", Description: "Returns a large text",
		Run: func(_ context.Context, _ *runnel.Call, c bigConfig) (any, error) {
			return strings.Repeat("x", c.Size), nil
		},
	})
	return p
}

// Records that do not fit into one message go out in several; one record too large and
// an output too large fail with a message instead of breaking the protocol.
func TestMessageLimit(t *testing.T) {
	defer runnel.SetMaxMessage(4000)()
	c := runneltest.Start(t, newBig())
	res, err := c.RunTransform("demo.big", map[string]any{"rows": 100, "size": 100})
	if err != nil || res.Error != "" {
		t.Fatal(err, res.Error)
	}
	if recs := res.Records(runnel.Main); len(recs) != 100 || recs[99]["n"] != 99.0 || len(res.Emits) < 3 {
		t.Errorf("%d records in %d emits", len(recs), len(res.Emits))
	}
	res, err = c.RunTransform("demo.big", map[string]any{"rows": 1, "size": 5000})
	if err != nil || !strings.HasPrefix(res.Error, `a record for "main" has `) || !strings.HasSuffix(res.Error, "more than the 4000 bytes a message may have") {
		t.Errorf("too large record: %v %q", err, res.Error)
	}
	_, err = c.Execute("demo.big", map[string]any{"size": 5000})
	if err == nil || !strings.Contains(err.Error(), "more than the 4000 bytes a message may have; return less") {
		t.Errorf("too large output: %v", err)
	}
	if _, err := c.Execute("demo.big", map[string]any{"size": 10}); err != nil {
		t.Errorf("small output: %v", err)
	}
}

// helperEnv starts the test binary as a plugin (see TestMain).
const helperEnv = "SDK_TEST_PLUGIN"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		p := runnel.New("noisy", "1.0.0")
		runnel.AddAction(p, runnel.Action[struct{}]{
			Type: "noisy.talk", Label: "Talk", Description: "Prints to stdout like careless code",
			Run: func(context.Context, *runnel.Call, struct{}) (any, error) {
				fmt.Println("hello from fmt.Println")
				_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":99,"result":"fake"}`)
				return "done", nil
			},
		})
		p.Main()
	}
	os.Exit(m.Run())
}

// Main keeps stdout for the protocol: what the plugin prints there goes to stderr.
func TestMainProtectsStdout(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"=1", "RUNNEL_PLUGIN=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr safeBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"+
		`{"jsonrpc":"2.0","id":2,"method":"action.execute","params":{"type":"noisy.talk","config":{}}}`+"\n")
	// Read the answers before closing stdin: its end cancels running requests.
	sc := bufio.NewScanner(stdout)
	var lines []string
	for len(lines) < 2 && sc.Scan() {
		lines = append(lines, sc.Text())
	}
	_ = stdin.Close()
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	want := []string{
		`{"id":1,"jsonrpc":"2.0","result":{"concurrency":4,"encoding":"json","name":"noisy","protocolVersion":1,"version":"1.0.0"}}`,
		`{"id":2,"jsonrpc":"2.0","result":{"output":"done"}}`,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("stdout\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(stderr.String(), "hello from fmt.Println") || !strings.Contains(stderr.String(), `"id":99`) {
		t.Errorf("stderr: %s", stderr.String())
	}
}

// Started without RUNNEL_PLUGIN=1, a plugin prints a hint and exits with 2 instead of
// waiting on stdin.
func TestMainWithoutRunnel(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"=1", "RUNNEL_PLUGIN=")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(string(out), "noisy 1.0.0 is a Runnel plugin") {
		t.Errorf("exit %v, output %q", err, out)
	}
}

// safeBuffer is a bytes.Buffer for one writer and one reader.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
