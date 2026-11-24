package runnel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"
)

// MaxMessageSize is the largest message of the protocol: 16 MiB per line (ADR 0019).
const MaxMessageSize = 16 << 20

// Error codes of JSON-RPC 2.0 and of the plugin protocol.
const (
	CodeParseError       = -32700
	CodeInvalidRequest   = -32600
	CodeMethodNotFound   = -32601
	CodeInvalidParams    = -32602
	CodeNotInitialized   = -32002
	CodeRequestCancelled = -32800
	CodeActionFailed     = -32000
)

// Main serves the plugin on stdin and stdout, as Runnel starts it, and exits the process:
// with 0 after Runnel closed stdin, with 1 on an error of the protocol. Started without
// RUNNEL_PLUGIN=1 (e.g. by a double click), it prints a hint and exits with 2 instead of
// waiting on stdin.
func (p *Plugin) Main() {
	if os.Getenv("RUNNEL_PLUGIN") != "1" {
		fmt.Fprintf(os.Stderr, "%s %s is a Runnel plugin: put its folder into plugins/ of a project and run \"runnel plugins trust\".\n", p.Name, p.Version)
		os.Exit(2)
	}
	if err := p.Serve(context.Background(), os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Serve speaks the plugin protocol: it reads requests from in, writes answers to out and
// the log to logw, until in ends or ctx is done. Requests run in their own goroutines, at
// most Concurrency at a time; when Serve returns, all of them have ended. Tests can call
// Serve with pipes; the package runneltest does that for them.
func (p *Plugin) Serve(ctx context.Context, in io.Reader, out io.Writer, logw io.Writer) error {
	s := &server{
		p:       p,
		out:     bufio.NewWriter(out),
		log:     NewLogger(logw),
		running: map[string]context.CancelFunc{},
		streams: map[int64]*Stream{},
	}
	concurrency := max(p.Concurrency, 1)
	s.slots = make(chan struct{}, concurrency)
	ctx, cancelAll := context.WithCancel(ctx)
	defer func() {
		cancelAll()
		s.wg.Wait()
	}()

	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 64<<10), MaxMessageSize+2)
		for sc.Scan() {
			line := bytes.Clone(bytes.TrimSuffix(sc.Bytes(), []byte("\r")))
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
		err := sc.Err()
		if errors.Is(err, bufio.ErrTooLong) {
			err = fmt.Errorf("message longer than %d bytes", MaxMessageSize)
		}
		readErr <- err
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case line := <-lines:
			if len(bytes.TrimSpace(line)) > 0 {
				s.handle(ctx, line)
			}
		}
	}
}

type server struct {
	p     *Plugin
	log   *Logger
	slots chan struct{}
	wg    sync.WaitGroup

	outMu sync.Mutex
	out   *bufio.Writer

	mu          sync.Mutex
	initialized bool
	hostVersion string
	running     map[string]context.CancelFunc
	streams     map[int64]*Stream
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *server) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		data, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{CodeActionFailed, "cannot encode the answer"}})
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = s.out.Write(append(data, '\n'))
	_ = s.out.Flush()
}

func (s *server) reply(id json.RawMessage, result any) {
	data, err := json.Marshal(result)
	if err != nil {
		s.fail(id, CodeActionFailed, fmt.Sprintf("the plugin cannot encode its result: %v", err))
		return
	}
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(data)})
}

func (s *server) fail(id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{code, msg}})
}

func (s *server) handle(ctx context.Context, line []byte) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		s.fail(nil, CodeParseError, "invalid JSON")
		return
	}
	isRequest := len(m.ID) > 0 && string(m.ID) != "null"
	if m.Method == "" {
		if isRequest {
			return // an answer; Runnel does not answer the plugin
		}
		s.fail(nil, CodeInvalidRequest, "message without method")
		return
	}
	if !isRequest {
		s.notification(m)
		return
	}
	s.mu.Lock()
	initialized := s.initialized
	s.mu.Unlock()
	if !initialized && m.Method != "initialize" && m.Method != "shutdown" {
		s.fail(m.ID, CodeNotInitialized, "initialize first")
		return
	}
	switch m.Method {
	case "initialize":
		s.initialize(m)
	case "describe":
		s.describe(m)
	case "action.execute":
		s.execute(ctx, m)
	case "connection.test":
		s.testConnection(ctx, m)
	case "transform.open":
		s.openStream(ctx, m)
	case "transform.init":
		s.initTransform(ctx, m)
	case "transform.detectFields":
		s.detectFields(ctx, m)
	case "shutdown":
		s.reply(m.ID, nil)
	default:
		s.fail(m.ID, CodeMethodNotFound, fmt.Sprintf("method %q not found", m.Method))
	}
}

func (s *server) notification(m message) {
	switch m.Method {
	case "transform.batch", "transform.end", "$/ack":
		s.streamMessage(m)
		return
	case "$/cancelRequest":
	default:
		return // unknown notifications are ignored
	}
	var p struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	s.mu.Lock()
	cancel := s.running[string(p.ID)]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *server) initialize(m message) {
	var params struct {
		Runnel struct {
			Version string `json:"version"`
		} `json:"runnel"`
	}
	_ = json.Unmarshal(m.Params, &params)
	s.mu.Lock()
	s.initialized = true
	s.hostVersion = params.Runnel.Version
	s.mu.Unlock()
	s.reply(m.ID, map[string]any{
		"protocolVersion": ProtocolVersion,
		"encoding":        "json",
		"name":            s.p.Name,
		"version":         s.p.Version,
		"concurrency":     max(s.p.Concurrency, 1),
	})
}

func (s *server) describe(m message) {
	actions := make([]actionDescriptor, 0, len(s.p.actions))
	for _, a := range s.p.actions {
		actions = append(actions, a.descriptor)
	}
	result := map[string]any{"actions": actions}
	if len(s.p.transforms) > 0 {
		ts := make([]transformDescriptor, 0, len(s.p.transforms))
		for _, t := range s.p.transforms {
			ts = append(ts, t.descriptor)
		}
		result["transforms"] = ts
	}
	if len(s.p.connections) > 0 {
		conns := make([]connectionDescriptor, 0, len(s.p.connections))
		for _, c := range s.p.connections {
			conns = append(conns, c.descriptor)
		}
		result["connectionTypes"] = conns
	}
	s.reply(m.ID, result)
}

func (s *server) execute(ctx context.Context, m message) {
	var params struct {
		Type    string          `json:"type"`
		Config  json.RawMessage `json:"config"`
		Context struct {
			RunID       string `json:"runId"`
			ActionID    string `json:"actionId"`
			ProjectDir  string `json:"projectDir"`
			Environment string `json:"environment"`
		} `json:"context"`
	}
	if err := json.Unmarshal(m.Params, &params); err != nil {
		s.fail(m.ID, CodeInvalidParams, "invalid parameters of action.execute")
		return
	}
	var entry *actionEntry
	for _, a := range s.p.actions {
		if a.descriptor.Type == params.Type {
			entry = a
		}
	}
	if entry == nil {
		s.fail(m.ID, CodeInvalidParams, fmt.Sprintf("unknown action type %q", params.Type))
		return
	}
	call := &Call{
		RunID:       params.Context.RunID,
		ActionID:    params.Context.ActionID,
		ProjectDir:  params.Context.ProjectDir,
		Environment: params.Context.Environment,
		Log:         s.log,
	}
	s.run(ctx, m.ID, params.Type, func(ctx context.Context) (any, error) {
		output, err := entry.run(ctx, call, params.Config)
		return map[string]any{"output": output}, err
	})
}

func (s *server) testConnection(ctx context.Context, m message) {
	var params struct {
		Type       string          `json:"type"`
		Connection json.RawMessage `json:"connection"`
	}
	if err := json.Unmarshal(m.Params, &params); err != nil {
		s.fail(m.ID, CodeInvalidParams, "invalid parameters of connection.test")
		return
	}
	var entry *connectionEntry
	for _, c := range s.p.connections {
		if c.descriptor.Type == params.Type {
			entry = c
		}
	}
	if entry == nil || entry.test == nil {
		s.fail(m.ID, CodeMethodNotFound, fmt.Sprintf("connection type %q has no test", params.Type))
		return
	}
	s.run(ctx, m.ID, params.Type, func(ctx context.Context) (any, error) {
		res, err := entry.test(ctx, params.Connection)
		if err != nil && ctx.Err() == nil {
			// A failing test is a result, not an error of the protocol.
			return TestResult{OK: false, Message: err.Error()}, nil
		}
		return res, err
	})
}

// run executes a request in its own goroutine once a slot is free, so that the reader
// keeps reading (e.g. $/cancelRequest) while requests run.
func (s *server) run(ctx context.Context, id json.RawMessage, what string, fn func(context.Context) (any, error)) {
	rctx, cancel := context.WithCancel(ctx)
	key := string(id)
	s.mu.Lock()
	s.running[key] = cancel
	s.mu.Unlock()
	s.wg.Go(func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, key)
			s.mu.Unlock()
			cancel()
		}()
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-rctx.Done():
			s.fail(id, CodeRequestCancelled, "request cancelled")
			return
		}
		result, err := safely(rctx, s.log, what, fn)
		switch {
		case rctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
			s.fail(id, CodeRequestCancelled, "request cancelled")
		case err != nil:
			s.fail(id, CodeActionFailed, err.Error())
		default:
			s.reply(id, result)
		}
	})
}

// safely runs fn and turns a panic into an error, so that one faulty action does not end
// the plugin and with it the other actions of the run.
func safely(ctx context.Context, log *Logger, what string, fn func(context.Context) (any, error)) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic in %s: %v\n%s", what, r, debug.Stack())
			result, err = nil, fmt.Errorf("internal error of the plugin in %s: %v", what, r)
		}
	}()
	return fn(ctx)
}
