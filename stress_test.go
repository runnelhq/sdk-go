package runnel_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	runnel "github.com/runnelhq/sdk-go"
	"github.com/runnelhq/sdk-go/runneltest"
)

// noLeaks fails the test if goroutines started during it are still running at its end
// (after the cleanups registered later, e.g. by runneltest.Start, have run).
func noLeaks(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if n := runtime.NumGoroutine(); n > before {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Errorf("%d goroutines still running after the test (before: %d)\n%s", n, before, buf)
		}
	})
}

// Many streams, actions and cancellations at the same time on one plugin: every stream
// gets all its records, every action an answer, and nothing keeps running afterwards.
func TestStress(t *testing.T) {
	noLeaks(t)
	p := newTransformDemo()
	runnel.AddAction(p, runnel.Action[struct {
		Wait int `json:"wait"`
	}]{
		Type: "demo.wait", Label: "Wait", Description: "Waits some milliseconds or until cancelled",
		Run: func(ctx context.Context, _ *runnel.Call, c struct {
			Wait int `json:"wait"`
		}) (any, error) {
			select {
			case <-time.After(time.Duration(c.Wait) * time.Millisecond):
				return c.Wait, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	c := runneltest.Start(t, p)
	fields := runnel.Fields{{"id", runnel.TypeInt}, {"name", runnel.TypeString}}
	var wg sync.WaitGroup
	for s := range 12 {
		wg.Go(func() {
			var batches []runnel.Batch
			for b := range 25 {
				batches = append(batches, runnel.Batch{Fields: fields, Rows: [][]any{{s*1000 + b, "x"}, {s*1000 + b, "bad"}}})
			}
			res, err := c.RunTransform("demo.upper", map[string]any{"field": "name"}, batches...)
			switch {
			case err != nil || res.Error != "":
				t.Errorf("stream %d: %v %q", s, err, res.Error)
			case len(res.Records(runnel.ToMain)) != 25 || len(res.Records(runnel.ToError)) != 25:
				t.Errorf("stream %d: %d records, %d errors", s, len(res.Records(runnel.ToMain)), len(res.Records(runnel.ToError)))
			}
		})
	}
	for a := range 40 {
		wg.Go(func() {
			wait := rand.IntN(40)
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(rand.IntN(40))*time.Millisecond)
			defer cancel()
			out, err := c.ExecuteContext(ctx, "demo.wait", map[string]any{"wait": wait}, runneltest.Context{RunID: fmt.Sprint(a)})
			var rerr *runneltest.Error
			if err != nil && (!errors.As(err, &rerr) || rerr.Code != runnel.CodeRequestCancelled) {
				t.Errorf("action %d: %v", a, err)
			}
			if err == nil && string(out) != fmt.Sprint(wait) {
				t.Errorf("action %d: output %s, want %d", a, out, wait)
			}
		})
	}
	for range 12 {
		wg.Go(func() {
			if _, err := c.RunTransform("demo.count", map[string]any{"count": 150}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

// When Runnel ends (stdin closes) while streams and actions run, Serve stops them and
// returns, and no goroutine is left.
func TestStdinEndsWhileRunning(t *testing.T) {
	noLeaks(t)
	p := runnel.New("demo", "1.0.0")
	runnel.AddTransform(p, runnel.Transform[struct{}]{
		Type: "demo.read", Label: "Read", Description: "Reads until the end",
		Run: func(ctx context.Context, s *runnel.Stream, _ struct{}) error {
			for {
				if _, ok, err := s.Next(ctx); err != nil || !ok {
					return err
				}
			}
		},
	})
	runnel.AddAction(p, runnel.Action[struct{}]{
		Type: "demo.block", Label: "Block", Description: "Waits until cancelled",
		Run: func(ctx context.Context, _ *runnel.Call, _ struct{}) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	inR, inW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- p.Serve(context.Background(), inR, io.Discard, io.Discard) }()
	lines := []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`}
	for i := range 5 {
		lines = append(lines,
			fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"transform.open","params":{"stream":%d,"type":"demo.read","config":{}}}`, 10+i, i),
			fmt.Sprintf(`{"jsonrpc":"2.0","method":"transform.batch","params":{"stream":%d,"fields":[{"name":"a"}],"rows":[[1]]}}`, i),
			fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"action.execute","params":{"type":"demo.block","config":{}}}`, 20+i))
	}
	if _, err := io.WriteString(inW, strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after stdin ended")
	}
}
