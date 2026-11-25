// Command hello is the example plugin of the Go SDK: the action hello.greet returns a
// greeting, the transform hello.greet adds one to every record. Build it into plugins/hello/bin/ of a project (see README.md of the SDK).
package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	runnel "github.com/runnelhq/sdk-go"
)

// version must match version in plugin.yaml.
const version = "1.0.0"

type greetConfig struct {
	Name     string `json:"name" doc:"Whom to greet, e.g. World" schema:"required,example=World"`
	Greeting string `json:"greeting" doc:"Word to greet with" schema:"enum=Hello|Hi|Moin,default=Hello"`
	Shout    bool   `json:"shout" doc:"Write the greeting in capital letters"`
}

type greetOutput struct {
	Message string `json:"message"`
	Length  int    `json:"length"`
}

func newPlugin() *runnel.Plugin {
	p := runnel.New("hello", version)
	runnel.AddAction(p, runnel.Action[greetConfig]{
		Type:        "hello.greet",
		Label:       "Greet",
		Description: "Returns a greeting for a name",
		Example:     "type: hello.greet\nwith:\n  name: World\n",
		Run: func(_ context.Context, call *runnel.Call, c greetConfig) (any, error) {
			if strings.TrimSpace(c.Name) == "" {
				return nil, fmt.Errorf("name is empty")
			}
			greeting := c.Greeting
			if greeting == "" {
				greeting = "Hello"
			}
			msg := fmt.Sprintf("%s, %s!", greeting, c.Name)
			if c.Shout {
				msg = strings.ToUpper(msg)
			}
			call.Log.Info("greeting %s in run %s", c.Name, call.RunID)
			return greetOutput{Message: msg, Length: len(msg)}, nil
		},
	})
	runnel.AddTransform(p, runnel.Transform[greetFieldConfig]{
		Type:         "hello.greet",
		Label:        "Greet",
		Description:  "Adds a greeting for the name in a field of every record",
		Example:      "type: hello.greet\nwith:\n  field: name\n  target: greeting\n",
		RecordErrors: true,
		Parallel:     true,
		Run:          greetRecords,
		Init:         initGreet,
	})
	return p
}

type greetFieldConfig struct {
	Field    string `json:"field" doc:"Field with the name, e.g. name" schema:"required,format=runnel-field,example=name"`
	Target   string `json:"target" doc:"New field for the greeting" schema:"default=greeting"`
	Greeting string `json:"greeting" doc:"Word to greet with" schema:"enum=Hello|Hi|Moin,default=Hello"`
}

func (c *greetFieldConfig) defaults() {
	if c.Target == "" {
		c.Target = "greeting"
	}
	if c.Greeting == "" {
		c.Greeting = "Hello"
	}
}

// greetRecords adds the greeting to every record; records without a name go to the error
// link with _error and _errorField.
func greetRecords(ctx context.Context, s *runnel.Stream, c greetFieldConfig) error {
	c.defaults()
	for {
		b, ok, err := s.Next(ctx)
		if err != nil || !ok {
			return err
		}
		at := b.Fields.Index(c.Field)
		if at < 0 {
			return fmt.Errorf("field %q is not in the records", c.Field)
		}
		out := append(slices.Clone(b.Fields), runnel.Field{Name: c.Target, Type: runnel.TypeString})
		var good, bad [][]any
		for _, row := range b.Rows {
			name, _ := row[at].(string)
			if strings.TrimSpace(name) == "" {
				bad = append(bad, runnel.ErrorRow(row, "name is empty", c.Field))
				continue
			}
			good = append(good, append(row, fmt.Sprintf("%s, %s!", c.Greeting, name)))
		}
		if err := s.Emit(ctx, runnel.ToMain, out, good); err != nil {
			return err
		}
		if err := s.Emit(ctx, runnel.ToError, runnel.ErrorFields(b.Fields), bad); err != nil {
			return err
		}
	}
}

// initGreet tells Runnel the fields of the output: the input and the greeting.
func initGreet(_ context.Context, in runnel.InitInput, c greetFieldConfig) runnel.InitResult {
	c.defaults()
	res := runnel.InitResult{}
	if !in.Input.Open && in.Input.Fields.Index(c.Field) < 0 {
		res.Problems = append(res.Problems, runnel.Problem{Path: "field", Message: fmt.Sprintf("field %q is not in the input", c.Field)})
	}
	if in.Input.Fields.Index(c.Target) >= 0 {
		res.Problems = append(res.Problems, runnel.Problem{Path: "target", Message: fmt.Sprintf("field %q exists already", c.Target)})
	}
	res.Output = &runnel.RecordSchema{Fields: append(slices.Clone(in.Input.Fields), runnel.Field{Name: c.Target, Type: runnel.TypeString}), Open: in.Input.Open}
	return res
}

func main() { newPlugin().Main() }
