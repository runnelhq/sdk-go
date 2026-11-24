// Package runneltest tests plugins of the Go SDK without Runnel: Start serves a plugin in
// the test process over pipes and returns a Client that speaks the protocol like Runnel
// does (initialize, describe, action.execute, connection.test, $/cancelRequest, shutdown).
//
//	func TestCheck(t *testing.T) {
//		c := runneltest.Start(t, newPlugin())
//		out, err := c.Execute("iban.check", map[string]any{"iban": "DE02120300000000202051"})
//		…
//	}
//
// The client does not check configurations against their schema, as Runnel does before a
// run; tests of required fields belong into the tests against Runnel itself.
package runneltest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	runnel "github.com/runnelhq/sdk-go"
)

// Timeout is how long the client waits for an answer.
var Timeout = 10 * time.Second

// Client talks to a plugin served by Start.
type Client struct {
	t   testing.TB
	in  *io.PipeWriter
	enc sync.Mutex

	mu      sync.Mutex
	nextID  int
	waiting map[int]chan response
	log     []string
	streams map[int64]*clientStream
	stream  int64
	ended   chan struct{}
	err     error

	// Initialized is the answer to initialize.
	Initialized Initialized
}

// Initialized is the answer of a plugin to initialize.
type Initialized struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Encoding        string `json:"encoding"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	Concurrency     int    `json:"concurrency"`
}

// Error is an error answer of the plugin.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// Context is the context of action.execute; Execute uses a fixed one.
type Context struct {
	RunID       string `json:"runId"`
	ActionID    string `json:"actionId"`
	ProjectDir  string `json:"projectDir"`
	Environment string `json:"environment,omitempty"`
}

// Start serves p in the background, sends initialize and returns the client. At the end of
// the test it sends shutdown, closes stdin and waits until the plugin ended.
func Start(t testing.TB, p *runnel.Plugin) *Client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	logR, logW := io.Pipe()
	c := &Client{t: t, in: inW, waiting: map[int]chan response{}, streams: map[int64]*clientStream{}, ended: make(chan struct{})}
	go func() {
		err := p.Serve(context.Background(), inR, outW, logW)
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		_ = outW.Close()
		_ = logW.Close()
		close(c.ended)
	}()
	go c.readLog(logR)
	go c.readAnswers(outR)
	t.Cleanup(func() {
		_, _ = c.Call("shutdown", map[string]any{})
		_ = inW.Close()
		select {
		case <-c.ended:
		case <-time.After(Timeout):
			t.Errorf("plugin did not end within %s after stdin was closed", Timeout)
		}
		if c.err != nil {
			t.Errorf("plugin ended with error: %v", c.err)
		}
	})
	raw, err := c.Call("initialize", map[string]any{"protocolVersion": 1, "encodings": []string{"json"}, "runnel": map[string]any{"version": "0.0.0-test"}})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := json.Unmarshal(raw, &c.Initialized); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return c
}

func (c *Client) readLog(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		c.mu.Lock()
		c.log = append(c.log, sc.Text())
		c.mu.Unlock()
	}
}

func (c *Client) readAnswers(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), runnel.MaxMessageSize+2)
	for sc.Scan() {
		var m struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int            `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			response
		}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil || m.JSONRPC != "2.0" {
			c.t.Errorf("plugin wrote something other than JSON-RPC on stdout: %q", sc.Text())
			continue
		}
		if m.ID == nil {
			c.streamNotification(m.Method, m.Params)
			continue
		}
		c.mu.Lock()
		ch := c.waiting[*m.ID]
		delete(c.waiting, *m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- m.response
		}
	}
}

// Call sends a request and waits for its answer: the result, or *Error for an error answer.
func (c *Client) Call(method string, params any) (json.RawMessage, error) {
	return c.CallContext(context.Background(), method, params)
}

// CallContext is Call that sends $/cancelRequest when ctx ends and then waits for the
// answer of the plugin, which should be the error -32800.
func (c *Client) CallContext(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan response, 1)
	c.waiting[id] = ch
	c.mu.Unlock()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	timeout := time.NewTimer(Timeout)
	defer timeout.Stop()
	done := ctx.Done()
	for {
		select {
		case r := <-ch:
			if r.Error != nil {
				return nil, r.Error
			}
			return r.Result, nil
		case <-done:
			done = nil
			if err := c.write(map[string]any{"jsonrpc": "2.0", "method": "$/cancelRequest", "params": map[string]any{"id": id}}); err != nil {
				return nil, err
			}
		case <-c.ended:
			return nil, errors.New("the plugin ended")
		case <-timeout.C:
			return nil, fmt.Errorf("no answer to %s within %s", method, Timeout)
		}
	}
}

func (c *Client) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.enc.Lock()
	defer c.enc.Unlock()
	_, err = c.in.Write(append(data, '\n'))
	return err
}

// Describe returns the answer to describe.
func (c *Client) Describe() (Description, error) {
	raw, err := c.Call("describe", map[string]any{})
	if err != nil {
		return Description{}, err
	}
	var d Description
	err = json.Unmarshal(raw, &d)
	return d, err
}

// Description is the answer to describe; schemas stay JSON.
type Description struct {
	Actions []struct {
		Type        string          `json:"type"`
		Kind        string          `json:"kind"`
		Category    string          `json:"category"`
		Label       string          `json:"label"`
		Description string          `json:"description"`
		Schema      json.RawMessage `json:"schema"`
		Example     string          `json:"example"`
	} `json:"actions"`
	ConnectionTypes []struct {
		Type        string          `json:"type"`
		Label       string          `json:"label"`
		Description string          `json:"description"`
		Schema      json.RawMessage `json:"schema"`
		Test        bool            `json:"test"`
	} `json:"connectionTypes"`
}

// Execute runs an action with config (as Runnel resolved it) and returns its output as
// JSON; an error of the action is *Error with its message.
func (c *Client) Execute(typ string, config any) (json.RawMessage, error) {
	return c.ExecuteContext(context.Background(), typ, config, Context{RunID: "01TESTRUN", ActionID: "test", ProjectDir: "."})
}

// ExecuteContext is Execute with a context for cancellation and the context of the run.
func (c *Client) ExecuteContext(ctx context.Context, typ string, config any, run Context) (json.RawMessage, error) {
	raw, err := c.CallContext(ctx, "action.execute", map[string]any{"type": typ, "config": config, "context": run})
	if err != nil {
		return nil, err
	}
	var res struct {
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res.Output, nil
}

// ExecuteInto is Execute that decodes the output into out.
func (c *Client) ExecuteInto(typ string, config, out any) error {
	raw, err := c.Execute(typ, config)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// TestConnection calls connection.test with a resolved connection (name, type and fields).
func (c *Client) TestConnection(conn map[string]any) (runnel.TestResult, error) {
	raw, err := c.Call("connection.test", map[string]any{"type": conn["type"], "connection": conn})
	if err != nil {
		return runnel.TestResult{}, err
	}
	var res runnel.TestResult
	err = json.Unmarshal(raw, &res)
	return res, err
}

// Log returns the lines the plugin wrote to stderr so far.
func (c *Client) Log() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.log...)
}

// LogContains waits briefly until a line of the log contains text.
func (c *Client) LogContains(text string) bool {
	deadline := time.Now().Add(time.Second)
	for {
		for _, l := range c.Log() {
			if strings.Contains(l, text) {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
