package runneltest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"
)

// RunnelEnv is the environment variable with the path of the runnel program for tests
// against Runnel itself (NewProject); without it, those tests are skipped.
const RunnelEnv = "RUNNEL_BIN"

// Project is a temporary Runnel project with the plugin under test, built and trusted.
type Project struct {
	t      testing.TB
	runnel string
	env    []string
	// Dir is the project folder.
	Dir string
}

var pluginName = regexp.MustCompile(`(?m)^name:\s*([a-z][a-z0-9-]*)\s*$`)

// NewProject creates a project in a temporary folder for tests against Runnel: it builds
// the Go package pkg (e.g. ".") into plugins/<name>/bin/<name>[.exe], copies pluginYAML
// (the plugin.yaml of the plugin, whose executable must point there) next to it and trusts
// the plugin with "runnel plugins trust --yes". The trust file lies in the temporary folder
// too, so the test does not touch the trusts of the user.
//
// The test is skipped unless RUNNEL_BIN names the runnel program.
func NewProject(t testing.TB, pkg, pluginYAML string) *Project {
	t.Helper()
	bin := os.Getenv(RunnelEnv)
	if bin == "" {
		t.Skipf("%s is not set; set it to the runnel program to test against Runnel", RunnelEnv)
	}
	yaml, err := os.ReadFile(pluginYAML)
	if err != nil {
		t.Fatal(err)
	}
	m := pluginName.FindSubmatch(yaml)
	if m == nil {
		t.Fatalf("%s: no name", pluginYAML)
	}
	name := string(m[1])
	root := t.TempDir()
	p := &Project{t: t, runnel: bin, Dir: filepath.Join(root, "project")}
	config := filepath.Join(root, "config")
	p.env = append(os.Environ(),
		"XDG_CONFIG_HOME="+config, // trusts on Linux
		"APPDATA="+config,         // trusts on Windows
		"HOME="+config,            // trusts on macOS
		"LOCALAPPDATA="+filepath.Join(root, "local"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
	)
	pluginDir := filepath.Join(p.Dir, "plugins", name)
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.MkdirAll(filepath.Join(pluginDir, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(context.Background(), "go", "build", "-o", filepath.Join(pluginDir, "bin", exe), pkg)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
	p.WriteFile(filepath.Join("plugins", name, "plugin.yaml"), string(yaml))
	p.WriteFile("project.yaml", "kind: Project\napiVersion: runnel/v1\nname: plugin-test\n")
	if out, code := p.Runnel("plugins", "trust", p.Dir, "--yes"); code != 0 {
		t.Fatalf("runnel plugins trust: exit %d\n%s", code, out)
	}
	return p
}

// WriteFile writes a file of the project, e.g. "workflows/check.workflow.yaml".
func (p *Project) WriteFile(name, content string) {
	p.t.Helper()
	path := filepath.Join(p.Dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		p.t.Fatal(err)
	}
}

// Runnel runs the runnel program with args and returns its output (stdout and stderr) and
// exit code.
func (p *Project) Runnel(args ...string) (string, int) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.runnel, args...)
	cmd.Env = p.env
	cmd.Dir = p.Dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return out.String(), ee.ExitCode()
	}
	if err != nil {
		p.t.Fatalf("runnel %v: %v", args, err)
	}
	return out.String(), 0
}

// Run runs a workflow of the project (path relative to the project) with "runnel run" and
// parameters KEY=VALUE, without run history, and returns its log and exit code.
func (p *Project) Run(workflow string, params ...string) (string, int) {
	p.t.Helper()
	args := []string{"run", "--history", "none", filepath.Join(p.Dir, filepath.FromSlash(workflow))}
	for _, kv := range params {
		args = append(args, "--param", kv)
	}
	return p.Runnel(args...)
}

// Validate runs "runnel validate" on the project and returns its output and exit code.
func (p *Project) Validate() (string, int) {
	p.t.Helper()
	return p.Runnel("validate", p.Dir)
}
