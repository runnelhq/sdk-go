package runnel

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ProtocolVersion is the version of the plugin protocol this SDK speaks (ADR 0019).
const ProtocolVersion = 1

// DefaultConcurrency is how many requests a plugin of this SDK handles at the same time,
// unless Plugin.Concurrency says otherwise.
const DefaultConcurrency = 4

// Categories of actions in the palette of the editor.
const (
	CategoryGeneral   = "general"
	CategoryExecution = "execution"
	CategoryFiles     = "files"
	CategoryNetwork   = "network"
	CategoryDatabase  = "database"
)

var categories = []string{CategoryGeneral, CategoryExecution, CategoryFiles, CategoryNetwork, CategoryDatabase}

var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	typePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$`)
)

// Plugin is a Runnel plugin: its name and version as in plugin.yaml, and the actions and
// connection types it offers. Create it with New, add actions with AddAction and connection
// types with AddConnectionType, then call Main.
type Plugin struct {
	// Name and Version must match name and version in plugin.yaml.
	Name    string
	Version string
	// Namespace is the namespace in plugin.yaml; all types of the plugin start with it.
	// New sets it to Name.
	Namespace string
	// Concurrency is how many requests the plugin handles at the same time (at least 1);
	// New sets DefaultConcurrency. Set 1 if the actions must not run in parallel.
	Concurrency int

	actions     []*actionEntry
	transforms  []*transformEntry
	connections []*connectionEntry
}

// New creates a plugin with the name and version from plugin.yaml; the namespace is the
// name. It panics on an invalid name, which is a programming error.
func New(name, version string) *Plugin {
	if !namePattern.MatchString(name) {
		panic(fmt.Sprintf("runnel: invalid plugin name %q (lowercase letters, digits and -)", name))
	}
	if version == "" {
		panic("runnel: the plugin needs a version")
	}
	return &Plugin{Name: name, Version: version, Namespace: name, Concurrency: DefaultConcurrency}
}

// Call describes one execution of an action.
type Call struct {
	RunID       string // ID of the run
	ActionID    string // id of the action in the workflow
	ProjectDir  string // project folder; also the working directory of the plugin
	Environment string // name of the environment, empty without one
	// Log writes to the log of the run (stderr of the plugin).
	Log *Logger
}

// Action describes an action of the plugin with its configuration type C, a struct whose
// tags describe the fields (see ConfigSchemaFor).
type Action[C any] struct {
	// Type is "<namespace>.<name>", e.g. "iban.check".
	Type string
	// Category is optional: one of the Category constants puts the action into that group
	// of the palette; empty puts it into a group of its own named after the plugin
	// ("Plugin: <name>", Runnel after runnel#236; older versions show it under general).
	Category string
	// Label is the short name in the palette, e.g. "Check IBAN".
	Label string
	// Description is one sentence for editor and tools.
	Description string
	// Example is optional YAML of the action for the help in the editor.
	Example string
	// Run executes the action with the configuration that Runnel checked and resolved
	// (variables, expressions, connections). Its result is result.output in the workflow
	// (text, number, list, map or struct). An error fails the action with its message.
	// Run must stop when ctx ends (cancelled run or timeout).
	Run func(ctx context.Context, call *Call, config C) (any, error)
}

// ConnectionInfo is the name and type of a resolved connection. Embed it in the struct of a
// connection type to receive them; they are not part of the schema.
type ConnectionInfo struct {
	Name string `json:"name" schema:"-"`
	Type string `json:"type" schema:"-"`
}

// TestResult is the result of testing a connection.
type TestResult struct {
	// OK reports whether the connection works; false with Message is a failed test.
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	// Version of the server, if known, e.g. "OpenLDAP 2.6".
	Version string `json:"version,omitempty"`
}

// ConnectionType describes a connection type of the plugin with the struct C of its
// fields. Fields holding passwords or tokens need format=runnel-secret.
type ConnectionType[C any] struct {
	// Type is the namespace or "<namespace>-…", e.g. "ldap" or "ldap-ad".
	Type        string
	Label       string
	Description string
	// Test is optional: it checks the connection for "Test connection" in the editor. It
	// should sign in, but neither write nor send anything else. An error counts as a
	// failed test with its message.
	Test func(ctx context.Context, conn C) (TestResult, error)
}

type actionEntry struct {
	descriptor actionDescriptor
	run        func(ctx context.Context, call *Call, config json.RawMessage) (any, error)
}

type connectionEntry struct {
	descriptor connectionDescriptor
	test       func(ctx context.Context, conn json.RawMessage) (TestResult, error)
}

type actionDescriptor struct {
	Type        string        `json:"type"`
	Kind        string        `json:"kind"`
	Category    string        `json:"category,omitempty"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	Schema      *ConfigSchema `json:"schema"`
	Example     string        `json:"example,omitempty"`
}

type connectionDescriptor struct {
	Type        string        `json:"type"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	Schema      *ConfigSchema `json:"schema"`
	Test        bool          `json:"test,omitempty"`
}

// AddAction adds an action to the plugin. It panics on mistakes in the definition (type
// outside the namespace, used twice, invalid schema tags, no Run), which are programming
// errors that the first test reveals.
func AddAction[C any](p *Plugin, a Action[C]) {
	if !typePattern.MatchString(a.Type) {
		panic(fmt.Sprintf("runnel: invalid action type %q (<namespace>.<name>)", a.Type))
	}
	if ns, _, _ := strings.Cut(a.Type, "."); ns != p.Namespace {
		panic(fmt.Sprintf("runnel: action type %q is outside the namespace %q", a.Type, p.Namespace))
	}
	if slices.ContainsFunc(p.actions, func(e *actionEntry) bool { return e.descriptor.Type == a.Type }) {
		panic(fmt.Sprintf("runnel: action type %q added twice", a.Type))
	}
	if a.Category != "" && !slices.Contains(categories, a.Category) {
		panic(fmt.Sprintf("runnel: action %q: unknown category %q", a.Type, a.Category))
	}
	if a.Label == "" || a.Description == "" {
		panic(fmt.Sprintf("runnel: action %q needs a label and a description", a.Type))
	}
	if a.Run == nil {
		panic(fmt.Sprintf("runnel: action %q has no Run", a.Type))
	}
	var zero C
	s, err := ConfigSchemaFor(zero)
	if err != nil {
		panic(fmt.Sprintf("runnel: action %q: %v", a.Type, err))
	}
	p.actions = append(p.actions, &actionEntry{
		descriptor: actionDescriptor{Type: a.Type, Kind: "action", Category: a.Category, Label: a.Label, Description: a.Description, Schema: s, Example: a.Example},
		run: func(ctx context.Context, call *Call, raw json.RawMessage) (any, error) {
			var config C
			if err := decode(raw, s, &config); err != nil {
				return nil, err
			}
			return a.Run(ctx, call, config)
		},
	})
}

// AddConnectionType adds a connection type to the plugin. Like AddAction, it panics on
// mistakes in the definition.
func AddConnectionType[C any](p *Plugin, c ConnectionType[C]) {
	if c.Type != p.Namespace && (!strings.HasPrefix(c.Type, p.Namespace+"-") || !namePattern.MatchString(c.Type)) {
		panic(fmt.Sprintf("runnel: connection type %q must be %q or start with %q", c.Type, p.Namespace, p.Namespace+"-"))
	}
	if slices.ContainsFunc(p.connections, func(e *connectionEntry) bool { return e.descriptor.Type == c.Type }) {
		panic(fmt.Sprintf("runnel: connection type %q added twice", c.Type))
	}
	if c.Label == "" || c.Description == "" {
		panic(fmt.Sprintf("runnel: connection type %q needs a label and a description", c.Type))
	}
	var zero C
	s, err := ConfigSchemaFor(zero)
	if err != nil {
		panic(fmt.Sprintf("runnel: connection type %q: %v", c.Type, err))
	}
	e := &connectionEntry{descriptor: connectionDescriptor{Type: c.Type, Label: c.Label, Description: c.Description, Schema: s, Test: c.Test != nil}}
	if c.Test != nil {
		e.test = func(ctx context.Context, raw json.RawMessage) (TestResult, error) {
			var conn C
			if err := decode(raw, s, &conn); err != nil {
				return TestResult{}, err
			}
			return c.Test(ctx, conn)
		}
	}
	p.connections = append(p.connections, e)
}
