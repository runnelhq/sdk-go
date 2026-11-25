package runnel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Categories of transforms in the palette of the editor.
const (
	CategoryInput     = "input"
	CategoryTransform = "transform"
	CategoryFlow      = "flow"
	CategoryCombine   = "combine"
	CategoryOutput    = "output"
)

var transformCategories = []string{CategoryInput, CategoryTransform, CategoryFlow, CategoryExecution, CategoryCombine, CategoryOutput}

// Targets of Emit besides the ids of transforms named in fields with format runnel-target.
const (
	ToMain  = "main"  // all data links that do not lead to a target of the configuration
	ToError = "error" // the error link: records with _error and _errorField (see ErrorFields)
)

// Field types of records (ADR 0007 of Runnel). Values in rows, as Next returns them and
// as Emit takes them: string; int as int64 (Emit: any integer); float as float64; decimal
// as text ("1234.56", exact); bool; date as text ("2026-11-24"); datetime as time.Time
// (Emit also takes RFC 3339 text); binary as []byte; json as any JSON value. An empty
// type means any JSON value. nil is null in every type.
const (
	TypeString   = "string"
	TypeInt      = "int"
	TypeFloat    = "float"
	TypeDecimal  = "decimal"
	TypeBool     = "bool"
	TypeDate     = "date"
	TypeDatetime = "datetime"
	TypeBinary   = "binary"
	TypeJSON     = "json"
)

// Field is a field of records: name and type.
type Field struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// Fields are the fields of rows, in the order of their values.
type Fields []Field

// Index returns the position of a field, or -1.
func (fs Fields) Index(name string) int {
	return slices.IndexFunc(fs, func(f Field) bool { return f.Name == name })
}

// ErrorFields returns fs with the fields _error and _errorField, which records on the
// error link carry (Runnel adds _errorTransform).
func ErrorFields(fs Fields) Fields {
	return append(slices.Clip(fs), Field{"_error", TypeString}, Field{"_errorField", TypeString})
}

// ErrorRow returns a copy of row with the message and the name of the field at fault, for
// rows with ErrorFields.
func ErrorRow(row []any, message, field string) []any {
	return append(slices.Clone(row), message, field)
}

// Batch is a batch of records as Runnel sends them: Rows follow the order of Fields.
type Batch struct {
	Fields Fields
	Rows   [][]any
}

// Transform describes a transform of the plugin with its configuration type C (see
// ConfigSchemaFor). The same type name may also be an action.
type Transform[C any] struct {
	// Type is "<namespace>.<name>", e.g. "iban.validate".
	Type string
	// Category is optional: one of CategoryInput, CategoryTransform, CategoryFlow,
	// CategoryExecution, CategoryCombine and CategoryOutput puts the transform into that
	// group of the palette; empty puts it into a group of the plugin ("Plugin: <name>",
	// Runnel after runnel#236; older versions show it under transform). Without category
	// it behaves like one of CategoryTransform. An input (no incoming links) needs
	// CategoryInput, so that the editor offers no link into it.
	Category    string
	Label       string
	Description string
	Example     string
	// RecordErrors: the transform may have an error link (Emit to ToError).
	RecordErrors bool
	// Parallel: the transform may run in several copies (copies: in the pipeline).
	Parallel bool
	// Run processes one stream: it reads batches with Stream.Next until there are no more
	// and writes records with Stream.Emit. An input (no incoming links) does not call Next
	// and only emits. Returning ends the stream; an error fails the transform and the
	// pipeline. Run must stop when ctx ends.
	Run func(ctx context.Context, s *Stream, config C) error
	// Init is optional: it checks the configuration and tells the fields of the output
	// (transform.init), when Runnel checks the pipeline and before the run. Fields with
	// secrets are empty, connections only have their name and variables may still be
	// ${…} – Init checks, it must not connect. Values that do not fit the struct are left
	// at their zero value.
	Init func(ctx context.Context, in InitInput, config C) InitResult
	// DetectFields is optional: it reads the first records of an input for "Detect fields"
	// in the editor (transform.detectFields), at most limit, within a minute. Connections
	// are resolved as in a run.
	DetectFields func(ctx context.Context, config C, limit int) (Detection, error)
}

// RecordSchema describes records on a link: the known fields and whether others may occur.
type RecordSchema struct {
	Fields Fields `json:"fields"`
	Open   bool   `json:"open,omitempty"`
}

// InitInput is what Init learns about the pipeline.
type InitInput struct {
	// Input is the schema of the incoming records; empty and closed for an input.
	Input RecordSchema `json:"input"`
	// Targets are the ids of the transforms that data links lead to.
	Targets []string `json:"targets"`
}

// InitResult is the answer of Init.
type InitResult struct {
	// Output is the schema of the outgoing records; nil means open (unknown).
	Output *RecordSchema `json:"output,omitempty"`
	// Problems are mistakes of the configuration; the editor shows them at Path.
	Problems []Problem `json:"problems,omitempty"`
}

// Problem is a mistake of the configuration at a path in with:, e.g. "field" or
// "fields[2].name"; with Warning it does not stop the run.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
	Warning bool   `json:"warning,omitempty"`
}

// Detection is what DetectFields found: facts about the source, the number of records
// read, whether that was all, and the proposed fields.
type Detection struct {
	Source   []Fact          `json:"source"`
	Rows     int             `json:"rows"`
	Complete bool            `json:"complete"`
	Fields   []DetectedField `json:"fields"`
}

// Fact is a line of the description of the source, e.g. Filter: (objectClass=user).
type Fact struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// DetectedField is a proposed field with its first values as text.
type DetectedField struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Format  string   `json:"format,omitempty"`
	Values  []string `json:"values"`
	Note    string   `json:"note,omitempty"`
	Warning bool     `json:"warning,omitempty"`
}

type transformEntry struct {
	descriptor transformDescriptor
	run        func(ctx context.Context, s *Stream, config json.RawMessage) error
	init       func(ctx context.Context, in InitInput, config json.RawMessage) InitResult
	detect     func(ctx context.Context, config json.RawMessage, limit int) (Detection, error)
}

type transformDescriptor struct {
	Type         string        `json:"type"`
	Kind         string        `json:"kind"`
	Category     string        `json:"category,omitempty"`
	Label        string        `json:"label"`
	Description  string        `json:"description"`
	Schema       *ConfigSchema `json:"schema"`
	Example      string        `json:"example,omitempty"`
	RecordErrors bool          `json:"recordErrors,omitempty"`
	Parallel     bool          `json:"parallel,omitempty"`
	Init         bool          `json:"init,omitempty"`
	DetectFields bool          `json:"detectFields,omitempty"`
}

// AddTransform adds a transform to the plugin. Like AddAction, it panics on mistakes in
// the definition.
func AddTransform[C any](p *Plugin, t Transform[C]) {
	if !typePattern.MatchString(t.Type) {
		panic(fmt.Sprintf("runnel: invalid transform type %q (<namespace>.<name>)", t.Type))
	}
	if ns, _, _ := strings.Cut(t.Type, "."); ns != p.Namespace {
		panic(fmt.Sprintf("runnel: transform type %q is outside the namespace %q", t.Type, p.Namespace))
	}
	if slices.ContainsFunc(p.transforms, func(e *transformEntry) bool { return e.descriptor.Type == t.Type }) {
		panic(fmt.Sprintf("runnel: transform type %q added twice", t.Type))
	}
	if t.Category != "" && !slices.Contains(transformCategories, t.Category) {
		panic(fmt.Sprintf("runnel: transform %q: unknown category %q", t.Type, t.Category))
	}
	if t.Label == "" || t.Description == "" || t.Run == nil {
		panic(fmt.Sprintf("runnel: transform %q needs a label, a description and Run", t.Type))
	}
	var zero C
	s, err := ConfigSchemaFor(zero)
	if err != nil {
		panic(fmt.Sprintf("runnel: transform %q: %v", t.Type, err))
	}
	e := &transformEntry{
		descriptor: transformDescriptor{Type: t.Type, Kind: "transform", Category: t.Category, Label: t.Label, Description: t.Description,
			Schema: s, Example: t.Example, RecordErrors: t.RecordErrors, Parallel: t.Parallel, Init: t.Init != nil, DetectFields: t.DetectFields != nil},
		run: func(ctx context.Context, st *Stream, raw json.RawMessage) error {
			var config C
			if err := decode(raw, s, &config); err != nil {
				return err
			}
			return t.Run(ctx, st, config)
		},
	}
	if t.Init != nil {
		e.init = func(ctx context.Context, in InitInput, raw json.RawMessage) InitResult {
			var config C
			decodeLenient(raw, s, &config)
			return t.Init(ctx, in, config)
		}
	}
	if t.DetectFields != nil {
		e.detect = func(ctx context.Context, raw json.RawMessage, limit int) (Detection, error) {
			var config C
			if err := decode(raw, s, &config); err != nil {
				return Detection{}, err
			}
			return t.DetectFields(ctx, config, limit)
		}
	}
	p.transforms = append(p.transforms, e)
}

// StreamWindow is how many batches the SDK takes from Runnel before Run has read them.
const StreamWindow = 4

// Stream is one running transform: the batches it reads and the records it writes, with
// the credits of the protocol in both directions (ADR 0019): Runnel sends at most
// StreamWindow batches that Next has not returned yet, and Emit waits until Runnel took
// the earlier records, so a slow transform behind the plugin slows the plugin down.
type Stream struct {
	RunID       string
	TransformID string // id of the transform in the pipeline
	ProjectDir  string
	Environment string
	// Log writes to the log of the run.
	Log *Logger

	id      int64
	srv     *server
	batches chan json.RawMessage // transform.batch; nil for transform.end

	mu       sync.Mutex
	credits  int           // emits Runnel still takes
	more     chan struct{} // signalled when credits grow
	inFields Fields        // fields of the last batch
	broken   error         // set when Runnel did not keep to the protocol; ends the stream
	cancel   context.CancelFunc
}

// fail ends the stream with an error of the protocol: Run's context is cancelled, and
// transform.closed reports err instead of what Run returns.
func (s *Stream) fail(err error) {
	s.mu.Lock()
	if s.broken == nil {
		s.broken = err
	}
	s.mu.Unlock()
	s.cancel()
}

func (s *Stream) brokenErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broken
}

// Next returns the next batch, or false after the last one. A batch's Rows belong to the
// caller.
func (s *Stream) Next(ctx context.Context) (Batch, bool, error) {
	var raw json.RawMessage
	select {
	case raw = <-s.batches:
	case <-ctx.Done():
		if err := s.brokenErr(); err != nil {
			return Batch{}, false, err
		}
		return Batch{}, false, ctx.Err()
	}
	if raw == nil {
		s.batches <- nil // Next keeps answering false
		return Batch{}, false, nil
	}
	var m struct {
		Fields Fields            `json:"fields"`
		Rows   []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return Batch{}, false, fmt.Errorf("invalid transform.batch: %w", err)
	}
	// The batch has left the buffer: Runnel may send the next one.
	s.srv.notify("$/ack", map[string]any{"stream": s.id, "batches": 1})
	if m.Fields != nil {
		s.inFields = m.Fields
	}
	rows := make([][]any, len(m.Rows))
	for i, r := range m.Rows {
		row, err := decodeRow(r, s.inFields)
		if err != nil {
			return Batch{}, false, fmt.Errorf("row %d of a batch: %w", i+1, err)
		}
		rows[i] = row
	}
	return Batch{Fields: s.inFields, Rows: rows}, true, nil
}

// decodeRow decodes the values of a row by the types of the fields: int as int64, float
// as float64, datetime as time.Time, binary as []byte; untyped numbers as int64 if they
// are whole, else float64. Other values stay as JSON gives them (decimal and date as
// text). A datetime or binary value that Runnel did not encode as the protocol says is an
// error.
func decodeRow(raw json.RawMessage, fields Fields) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var row []any
	if err := dec.Decode(&row); err != nil {
		return nil, err
	}
	for i, v := range row {
		typ := ""
		if i < len(fields) {
			typ = fields[i].Type
		}
		switch x := v.(type) {
		case json.Number:
			if typ == TypeFloat {
				row[i], _ = x.Float64()
			} else if n, err := x.Int64(); err == nil {
				row[i] = n
			} else {
				row[i], _ = x.Float64()
			}
		case string:
			switch typ {
			case TypeDatetime:
				t, err := time.Parse(time.RFC3339Nano, x)
				if err != nil {
					return nil, fmt.Errorf("field %q: %q is not a date and time (RFC 3339)", fields[i].Name, x)
				}
				row[i] = t
			case TypeBinary:
				b, err := base64.StdEncoding.DecodeString(x)
				if err != nil {
					return nil, fmt.Errorf("field %q: the value is not Base64", fields[i].Name)
				}
				row[i] = b
			}
		}
	}
	return row, nil
}

// Emit writes rows with the given fields to a target: ToMain, ToError or the id of a transform
// from a field with format runnel-target. It waits until Runnel takes more records. Rows
// that do not fit into one message (16 MiB) are sent in several; a single record larger
// than that is an error. Rows must not be changed after Emit.
func (s *Stream) Emit(ctx context.Context, to string, fields Fields, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	// fields go with every emit: the protocol allows leaving them out when they did not
	// change, but Runnel before the fix of runnel#228 reads such an emit as one without
	// fields.
	data, err := json.Marshal(emitMessage{JSONRPC: "2.0", Method: "transform.emit", Params: emitParams{Stream: s.id, To: to, Fields: fields, Rows: rows}})
	if err != nil {
		return fmt.Errorf("cannot encode the records for %q: %w", to, err)
	}
	if !fits(data) {
		if len(rows) == 1 {
			return fmt.Errorf("a record for %q has %s, more than the %s a message may have", to, byteSize(len(data)), byteSize(maxMessage))
		}
		half := len(rows) / 2
		if err := s.Emit(ctx, to, fields, rows[:half]); err != nil {
			return err
		}
		return s.Emit(ctx, to, fields, rows[half:])
	}
	if err := s.credit(ctx); err != nil {
		return err
	}
	s.srv.sendRaw(data)
	return nil
}

type emitMessage struct {
	JSONRPC string     `json:"jsonrpc"`
	Method  string     `json:"method"`
	Params  emitParams `json:"params"`
}

type emitParams struct {
	Stream int64   `json:"stream"`
	To     string  `json:"to"`
	Fields Fields  `json:"fields"`
	Rows   [][]any `json:"rows"`
}

// credit waits for a credit of Runnel and takes it.
func (s *Stream) credit(ctx context.Context) error {
	s.mu.Lock()
	for s.credits <= 0 {
		more := s.more
		s.mu.Unlock()
		select {
		case <-more:
		case <-ctx.Done():
			if err := s.brokenErr(); err != nil {
				return err
			}
			return ctx.Err()
		}
		s.mu.Lock()
	}
	s.credits--
	s.mu.Unlock()
	return nil
}

func (s *Stream) ack(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credits += n
	close(s.more)
	s.more = make(chan struct{})
}

func (srv *server) notify(method string, params any) {
	srv.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (srv *server) openStream(ctx context.Context, m message) {
	var params struct {
		Stream  int64           `json:"stream"`
		Type    string          `json:"type"`
		Config  json.RawMessage `json:"config"`
		Context struct {
			RunID       string `json:"runId"`
			TransformID string `json:"transformId"`
			ProjectDir  string `json:"projectDir"`
			Environment string `json:"environment"`
		} `json:"context"`
		EmitWindow *int `json:"emitWindow"`
	}
	if err := json.Unmarshal(m.Params, &params); err != nil {
		srv.fail(m.ID, CodeInvalidParams, "invalid parameters of transform.open")
		return
	}
	entry := srv.transform(params.Type)
	if entry == nil {
		srv.fail(m.ID, CodeInvalidParams, fmt.Sprintf("unknown transform type %q", params.Type))
		return
	}
	credits := 4
	if params.EmitWindow != nil {
		credits = *params.EmitWindow
	}
	st := &Stream{
		RunID: params.Context.RunID, TransformID: params.Context.TransformID, ProjectDir: params.Context.ProjectDir,
		Environment: params.Context.Environment, Log: srv.log,
		id: params.Stream, srv: srv, batches: make(chan json.RawMessage, StreamWindow+1), credits: credits, more: make(chan struct{}),
	}
	sctx, cancel := context.WithCancel(ctx)
	st.cancel = cancel
	srv.mu.Lock()
	if _, dup := srv.streams[st.id]; dup {
		srv.mu.Unlock()
		cancel()
		srv.fail(m.ID, CodeInvalidParams, fmt.Sprintf("stream %d is open already", st.id))
		return
	}
	srv.streams[st.id] = st
	srv.mu.Unlock()
	srv.reply(m.ID, map[string]any{"window": StreamWindow})
	srv.wg.Go(func() {
		defer cancel()
		closed := map[string]any{"stream": st.id}
		_, err := safely(sctx, srv.log, params.Type, func(ctx context.Context) (any, error) {
			return nil, entry.run(ctx, st, params.Config)
		})
		srv.mu.Lock()
		delete(srv.streams, st.id)
		srv.mu.Unlock()
		if broken := st.brokenErr(); broken != nil {
			err = broken // the cause, not the cancellation it led to
		}
		if err != nil {
			closed["error"] = map[string]any{"message": err.Error()}
		}
		srv.notify("transform.closed", closed)
	})
}

// streamMessage handles transform.batch, transform.end and $/ack of a stream. It never
// blocks: Runnel sends at most StreamWindow batches ahead, so the buffer has room; more
// is an error of the host, which ends the stream with an error.
func (srv *server) streamMessage(m message) {
	var p struct {
		Stream  int64 `json:"stream"`
		Batches int   `json:"batches"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	srv.mu.Lock()
	st := srv.streams[p.Stream]
	srv.mu.Unlock()
	if st == nil {
		return
	}
	switch m.Method {
	case "$/ack":
		if p.Batches > 0 {
			st.ack(p.Batches)
		}
	case "transform.batch", "transform.end":
		var item json.RawMessage
		if m.Method == "transform.batch" {
			item = m.Params
		}
		if st.brokenErr() != nil {
			return
		}
		if item != nil && len(st.batches) >= StreamWindow {
			// Dropping the batch would lose records; the stream fails instead.
			st.fail(fmt.Errorf("more than %d batches arrived from Runnel without waiting for $/ack; the records cannot be processed completely", StreamWindow))
			return
		}
		select {
		case st.batches <- item:
		default:
			// Dropping the batch would lose records; the stream fails instead.
			st.fail(fmt.Errorf("more than %d batches arrived from Runnel without waiting for $/ack; the records cannot be processed completely", StreamWindow))
		}
	}
}

func (srv *server) transform(typ string) *transformEntry {
	for _, t := range srv.p.transforms {
		if t.descriptor.Type == typ {
			return t
		}
	}
	return nil
}

func (srv *server) initTransform(ctx context.Context, m message) {
	var params struct {
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
		InitInput
	}
	if err := json.Unmarshal(m.Params, &params); err != nil {
		srv.fail(m.ID, CodeInvalidParams, "invalid parameters of transform.init")
		return
	}
	entry := srv.transform(params.Type)
	if entry == nil || entry.init == nil {
		srv.fail(m.ID, CodeMethodNotFound, fmt.Sprintf("transform %q has no init", params.Type))
		return
	}
	srv.run(ctx, m.ID, params.Type, func(ctx context.Context) (any, error) {
		return entry.init(ctx, params.InitInput, params.Config), nil
	})
}

func (srv *server) detectFields(ctx context.Context, m message) {
	var params struct {
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
		Limit  int             `json:"limit"`
	}
	if err := json.Unmarshal(m.Params, &params); err != nil {
		srv.fail(m.ID, CodeInvalidParams, "invalid parameters of transform.detectFields")
		return
	}
	entry := srv.transform(params.Type)
	if entry == nil || entry.detect == nil {
		srv.fail(m.ID, CodeMethodNotFound, fmt.Sprintf("transform %q cannot detect fields", params.Type))
		return
	}
	if params.Limit <= 0 {
		params.Limit = 1000
	}
	srv.run(ctx, m.ID, params.Type, func(ctx context.Context) (any, error) {
		d, err := entry.detect(ctx, params.Config, params.Limit)
		if err != nil {
			return nil, err
		}
		if d.Source == nil {
			d.Source = []Fact{}
		}
		for i := range d.Fields {
			if d.Fields[i].Values == nil {
				d.Fields[i].Values = []string{}
			}
		}
		if d.Fields == nil {
			d.Fields = []DetectedField{}
		}
		return d, nil
	})
}
