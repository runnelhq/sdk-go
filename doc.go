// Package runnel is the Go SDK for Runnel plugins (ADR 0019 of Runnel, protocol in
// docs/dev/PLUGINS.md of the Runnel repository). A plugin is a program that Runnel starts
// with RUNNEL_PLUGIN=1 and talks to over JSON-RPC on stdin and stdout; this package speaks
// the protocol (handshake, describe, action.execute, connection.test, transforms as
// streams, cancellation, concurrency) so that a plugin only defines its actions and connection types:
//
//	type checkConfig struct {
//		IBAN string `json:"iban" doc:"The IBAN to check" schema:"required"`
//	}
//
//	func main() {
//		p := runnel.New("iban", "1.0.0") // name and version as in plugin.yaml
//		runnel.AddAction(p, runnel.Action[checkConfig]{
//			Type: "iban.check", Label: "Check IBAN", Description: "Checks an IBAN",
//			Run: func(ctx context.Context, call *runnel.Call, c checkConfig) (any, error) {
//				return map[string]any{"valid": check(c.IBAN)}, nil
//			},
//		})
//		p.Main()
//	}
//
// The schema of the configuration comes from the struct tags, as for the built-in actions
// of Runnel (see ConfigSchemaFor). Runnel checks the configuration against it and resolves
// variables, expressions and connections before it calls the plugin.
//
// Transforms in pipelines (AddTransform) read batches with Stream.Next and write records
// with Stream.Emit; the SDK keeps to the credits of the protocol in both directions and
// offers transform.init and transform.detectFields when the transform defines Init and
// DetectFields.
//
// A Reference in Action and Transform optionally documents the output, the errors and the
// limits for the catalog of a project (runnel serve) and the help in the editor; runneltest.CheckReference checks
// it with the rules Runnel applies to its own types.
package runnel
