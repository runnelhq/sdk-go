package runneltest

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	runnel "github.com/runnelhq/sdk-go"
)

// EmitWindow is the number of transform.emit the client takes before it acknowledges
// them, like Runnel.
const EmitWindow = 4

// Emitted is one transform.emit of a plugin, with the fields that apply to its rows.
type Emitted struct {
	To     string
	Fields runnel.Fields
	Rows   [][]any
}

// StreamResult is everything a transform wrote, and the error it closed with.
type StreamResult struct {
	Emits []Emitted
	// Error is the message of transform.closed, empty if the transform succeeded.
	Error string
}

// Records returns the rows sent to a target (runnel.ToMain, runnel.ToError or an id) as maps
// from field name to value; numbers are float64 as in JSON.
func (r StreamResult) Records(to string) []map[string]any {
	var out []map[string]any
	for _, e := range r.Emits {
		if e.To != to {
			continue
		}
		for _, row := range e.Rows {
			m := map[string]any{}
			for i, f := range e.Fields {
				if i < len(row) {
					m[f.Name] = row[i]
				}
			}
			out = append(out, m)
		}
	}
	return out
}

type clientStream struct {
	window  chan struct{} // credits for batches, from $/ack of the plugin
	emits   chan emitMsg
	pending int // emits not acknowledged yet
}

type emitMsg struct {
	closed bool
	err    string
	e      Emitted
	fields bool // the emit had fields
}

func (c *Client) streamNotification(method string, params json.RawMessage) {
	var p struct {
		Stream  int64                     `json:"stream"`
		Batches int                       `json:"batches"`
		To      string                    `json:"to"`
		Fields  *runnel.Fields            `json:"fields"`
		Rows    [][]any                   `json:"rows"`
		Error   *struct{ Message string } `json:"error"`
	}
	if json.Unmarshal(params, &p) != nil {
		c.t.Errorf("invalid %s: %s", method, params)
		return
	}
	c.mu.Lock()
	st := c.streams[p.Stream]
	c.mu.Unlock()
	if st == nil {
		c.t.Errorf("%s for unknown stream %d", method, p.Stream)
		return
	}
	switch method {
	case "$/ack":
		for range p.Batches {
			st.window <- struct{}{}
		}
	case "transform.emit":
		m := emitMsg{e: Emitted{To: p.To, Rows: p.Rows}}
		if p.Fields != nil {
			m.e.Fields, m.fields = *p.Fields, true
		}
		st.emits <- m
	case "transform.closed":
		m := emitMsg{closed: true}
		if p.Error != nil {
			m.err = p.Error.Message
		}
		st.emits <- m
	default:
		c.t.Errorf("unexpected notification %s", method)
	}
}

// RunTransform runs a transform like Runnel: transform.open, the batches as long as the
// plugin grants credits, transform.end, and collects transform.emit until
// transform.closed. For an input, pass no batches. It fails the test if the plugin breaks
// the protocol (more emits than credits, rows without fields).
func (c *Client) RunTransform(typ string, config any, batches ...runnel.Batch) (StreamResult, error) {
	c.mu.Lock()
	c.stream++
	id := c.stream
	st := &clientStream{window: make(chan struct{}, 1024), emits: make(chan emitMsg, 1024)}
	c.streams[id] = st
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.streams, id)
		c.mu.Unlock()
	}()
	raw, err := c.Call("transform.open", map[string]any{"stream": id, "type": typ, "config": config,
		"context": map[string]any{"runId": "01TESTRUN", "transformId": "test", "projectDir": "."}, "emitWindow": EmitWindow})
	if err != nil {
		return StreamResult{}, err
	}
	var open struct {
		Window int `json:"window"`
	}
	if err := json.Unmarshal(raw, &open); err != nil || open.Window < 1 || open.Window > 64 {
		return StreamResult{}, fmt.Errorf("transform.open answered window %d (1 to 64)", open.Window)
	}
	for range open.Window {
		st.window <- struct{}{}
	}
	sendErr := make(chan error, 1)
	go func() {
		var last runnel.Fields
		for _, b := range batches {
			select {
			case <-st.window:
			case <-time.After(Timeout):
				sendErr <- errors.New("the plugin grants no credits for batches ($/ack)")
				return
			}
			msg := map[string]any{"stream": id, "rows": b.Rows}
			if !slices.Equal(b.Fields, last) {
				msg["fields"], last = b.Fields, b.Fields
			}
			if err := c.write(map[string]any{"jsonrpc": "2.0", "method": "transform.batch", "params": msg}); err != nil {
				sendErr <- err
				return
			}
		}
		sendErr <- c.write(map[string]any{"jsonrpc": "2.0", "method": "transform.end", "params": map[string]any{"stream": id}})
	}()
	var res StreamResult
	var fields runnel.Fields
	timeout := time.NewTimer(Timeout)
	defer timeout.Stop()
	for {
		select {
		case m := <-st.emits:
			if m.closed {
				res.Error = m.err
				return res, nil
			}
			st.pending++
			if st.pending > EmitWindow {
				c.t.Errorf("stream %d: more transform.emit than credits", id)
			}
			if m.fields {
				fields = m.e.Fields
			}
			if fields == nil {
				c.t.Errorf("stream %d: transform.emit without fields", id)
			}
			m.e.Fields = fields
			res.Emits = append(res.Emits, m.e)
			st.pending--
			if err := c.write(map[string]any{"jsonrpc": "2.0", "method": "$/ack", "params": map[string]any{"stream": id, "batches": 1}}); err != nil {
				return res, err
			}
		case err := <-sendErr:
			if err != nil {
				return res, err
			}
		case <-c.ended:
			return res, errors.New("the plugin ended")
		case <-timeout.C:
			return res, fmt.Errorf("stream %d: no transform.closed within %s", id, Timeout)
		}
	}
}

// Init calls transform.init with the configuration (as Runnel sends it for checks) and
// the input schema.
func (c *Client) Init(typ string, config any, in runnel.InitInput) (runnel.InitResult, error) {
	if in.Targets == nil {
		in.Targets = []string{}
	}
	raw, err := c.Call("transform.init", map[string]any{"type": typ, "config": config, "input": in.Input, "targets": in.Targets})
	if err != nil {
		return runnel.InitResult{}, err
	}
	var res runnel.InitResult
	err = json.Unmarshal(raw, &res)
	return res, err
}

// DetectFields calls transform.detectFields.
func (c *Client) DetectFields(typ string, config any, limit int) (runnel.Detection, error) {
	raw, err := c.Call("transform.detectFields", map[string]any{"type": typ, "config": config, "limit": limit})
	if err != nil {
		return runnel.Detection{}, err
	}
	var d runnel.Detection
	err = json.Unmarshal(raw, &d)
	return d, err
}
