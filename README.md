# sdk-go – Go-SDK für Runnel-Plugins

Mit diesem SDK schreibt man [Runnel](https://github.com/runnelhq/runnel)-Plugins in Go. Ein Plugin ist ein eigenes
Programm, das Runnel als Kindprozess startet und über JSON-RPC auf stdin/stdout anspricht (ADR 0019 von Runnel). Das
SDK übernimmt das Protokoll; ein Plugin beschreibt nur seine Actions und Connection-Typen als Go-Structs.

Verbindlich ist das Protokoll in [`docs/dev/PLUGINS.md`](https://github.com/runnelhq/runnel/blob/main/docs/dev/PLUGINS.md)
von Runnel, nicht dieses SDK.

| | |
|---|---|
| Modul | `github.com/runnelhq/sdk-go`, Paket `runnel` |
| Protokoll | Version 1: `initialize`, `describe`, `action.execute`, `connection.test`, `$/cancelRequest`, `shutdown` |
| Runnel | ab S49c (Actions und Connection-Typen aus Plugins) |
| Noch nicht | Transforms in Pipelines (`transform.open/batch/end`, Kredite) – folgen, sobald Runnel sie anbietet (S50) |
| Lizenz | MIT |

## Ein Plugin in fünf Minuten

```go
package main

import (
	"context"
	"fmt"

	runnel "github.com/runnelhq/sdk-go"
)

type greetConfig struct {
	Name  string `json:"name" doc:"Whom to greet" schema:"required,example=World"`
	Shout bool   `json:"shout" doc:"Capital letters"`
}

func main() {
	p := runnel.New("hello", "1.0.0") // name und version wie in plugin.yaml
	runnel.AddAction(p, runnel.Action[greetConfig]{
		Type:        "hello.greet",
		Label:       "Greet",
		Description: "Returns a greeting for a name",
		Run: func(ctx context.Context, call *runnel.Call, c greetConfig) (any, error) {
			call.Log.Info("greeting %s", c.Name)
			return map[string]any{"message": fmt.Sprintf("Hello, %s!", c.Name)}, nil
		},
	})
	p.Main()
}
```

Das vollständige Beispiel mit Tests liegt in [`examples/hello`](examples/hello). Bauen und im Projekt ablegen:

```bash
go build -o D:/flows/plugins/hello/bin/hello.exe ./examples/hello   # GOOS=windows für Windows
cp examples/hello/plugin.yaml D:/flows/plugins/hello/
runnel plugins trust D:/flows
runnel plugins D:/flows
```

Im Workflow steht das Ergebnis unter `results.<id>.output`:

```yaml
actions:
  - id: greet
    type: hello.greet
    with:
      name: ${TARGET}
```

## Konfiguration als Struct

Das JSON Schema der Konfiguration entsteht aus den Tags – genau wie bei den eingebauten Actions von Runnel (ADR 0014).
Runnel prüft die `with:`-Werte gegen das Schema (Pflichtfelder, unbekannte Felder), löst Variablen, Ausdrücke und
Connections auf und ruft erst dann das Plugin.

| Tag | Bedeutung |
|---|---|
| `json:"name"` | Name des Feldes in der Datei; ohne Namen, mit `json:"-"` oder nicht exportiert: kein Feld |
| `doc:"…"` | Beschreibung für Editor und Werkzeuge (englisch, ein Satz) |
| `schema:"required"` | Pflichtfeld |
| `schema:"default=…"`, `example=…`, `enum=a\|b` | Vorgabe, Beispiel, erlaubte Werte |
| `schema:"format=runnel-secret"` | Passwort oder Token (nur solche Felder dürfen `${secret:…}` nutzen) |
| `schema:"format=runnel-duration"`, `runnel-text` | Zeitspanne (`30s`), mehrzeiliger Text |
| `schema:"group=Advanced"`, `only=mode:b` | Abschnitt im Formular; Feld gilt nur bei einem Wert eines anderen Feldes |
| `schema:"connection=ldap"` | Name einer Connection dieser Typen; das Feld darf ein Struct sein und bekommt die **aufgelöste** Connection |
| `schema:"-"` | wird gelesen, steht aber nicht im Schema (z. B. `name` und `type` in `ConnectionInfo`) |

Texte in Feldern vom Typ Zahl oder `bool` wandelt das SDK um, denn ein Wert aus einer Variablen (`times: ${N}`) kommt
als Text an. Passt ein Wert nicht, scheitert die Action mit einer Meldung wie
`field "times": expected a whole number, got string`.

## Connection-Typen

```go
type ldapConnection struct {
	runnel.ConnectionInfo // name und type der Connection
	URL      string `json:"url" doc:"Address, e.g. ldaps://dc1.example.com" schema:"required"`
	Password string `json:"password" doc:"Password" schema:"format=runnel-secret"`
}

runnel.AddConnectionType(p, runnel.ConnectionType[ldapConnection]{
	Type: "ldap", Label: "LDAP", Description: "LDAP or Active Directory server",
	Test: func(ctx context.Context, c ldapConnection) (runnel.TestResult, error) { … },
})

type searchConfig struct {
	Server ldapConnection `json:"connection" doc:"LDAP connection" schema:"required,connection=ldap"`
}
```

- Der Name ist der Namensraum oder beginnt mit `<namensraum>-`.
- `Test` ist optional; damit bietet der Editor „Test connection“ an. Ein Fehler zählt als gescheiterter Test.
- Secrets kommen im Klartext an. Runnel maskiert sie in allem, was vom Plugin zurückkommt; trotzdem nicht loggen.

## Was das SDK übernimmt

- **Start:** Ohne `RUNNEL_PLUGIN=1` gibt `Main` einen Hinweis aus und endet mit Exit-Code 2.
- **Handshake:** `initialize` mit `protocolVersion: 1`, `encoding: json`, Name, Version und `concurrency`
  (Standard 4, `p.Concurrency`); `describe` mit Schemas aus den Structs.
- **Parallelität:** höchstens `Concurrency` Anfragen gleichzeitig, jede in einer eigenen Goroutine.
- **Abbruch:** `$/cancelRequest` beendet den `ctx` der Action; das SDK antwortet mit `-32800`.
- **Fehler:** Ein `error` aus `Run` wird zur Meldung der Action (`-32000`); ein `panic` ebenso, das Plugin läuft weiter.
  Unbekannte Methoden: `-32601`; Nachrichten über 16 MiB beenden das Plugin.
- **Log:** `call.Log.Debug/Info/Warn/Error` schreibt Zeilen mit Stufe auf stderr; Runnel zeigt sie im Log des Laufs.
  stdout gehört allein dem Protokoll – nie `fmt.Println` in einem Plugin.

## Testen

Das Paket `runneltest` spricht das Protokoll wie Runnel, ohne Prozess:

```go
func TestGreet(t *testing.T) {
	c := runneltest.Start(t, newPlugin())
	out, err := c.Execute("hello.greet", map[string]any{"name": "World"})
	…
}
```

Gegen ein echtes Runnel testet `runneltest.NewProject`: Es baut das Plugin in ein temporäres Projekt, gibt es mit
`runnel plugins trust --yes` frei (Freigaben ebenfalls im temporären Ordner) und startet Workflows mit `runnel run`.
Solche Tests laufen nur, wenn `RUNNEL_BIN` auf das Programm zeigt:

```bash
make runnel-test RUNNEL_BIN=../runnel/bin/runnel
```

## Entwickeln

| Zweck | Befehl |
|---|---|
| Tests (mit Race-Detector) | `make test` |
| Lint (golangci-lint v2.15.0, Linux- und Windows-Code) | `make lint` (ohne passendes golangci-lint: `make bin/tools/golangci-lint lint GOLANGCI_LINT=bin/tools/golangci-lint`) |
| Fuzzing der Protokoll-Schleife | `make fuzz FUZZTIME=1m` |
| Tests gegen Runnel | `make runnel-test RUNNEL_BIN=…` |

Versionen nach SemVer mit Tags (`v0.1.0`); bis `v1.0.0` kann sich die Go-API noch ändern, das Protokoll nicht.
Änderungen stehen in [CHANGELOG.md](CHANGELOG.md).

## Plugins mit diesem SDK

- [`runnelhq/plugin-iban`](https://github.com/runnelhq/plugin-iban) – IBAN und BIC prüfen, IBAN bilden
- [`runnelhq/plugin-ldap`](https://github.com/runnelhq/plugin-ldap) – LDAP und Active Directory: suchen, anmelden, ändern
