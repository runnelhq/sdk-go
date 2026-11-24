// Command hello is the example plugin of the Go SDK: the action hello.greet returns a
// greeting. Build it into plugins/hello/bin/ of a project (see README.md of the SDK).
package main

import (
	"context"
	"fmt"
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
	return p
}

func main() { newPlugin().Main() }
