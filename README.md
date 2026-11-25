# sdk-go – Go-SDK für Runnel-Plugins

Mit diesem SDK schreibt man [Runnel](https://github.com/runnelhq/runnel)-Plugins in Go. Ein Plugin ist ein eigenes
Programm, das Runnel als Kindprozess startet und über JSON-RPC auf stdin/stdout anspricht (ADR 0019 von Runnel). Das
SDK übernimmt das Protokoll; ein Plugin beschreibt nur seine Actions und Connection-Typen als Go-Structs.

Verbindlich ist das Protokoll in [`docs/dev/PLUGINS.md`](https://github.com/runnelhq/runnel/blob/main/docs/dev/PLUGINS.md)
von Runnel, nicht dieses SDK.

| | |
|---|---|
| Modul | `github.com/runnelhq/sdk-go`, Paket `runnel` |
| Protokoll | Version 1: `initialize`, `describe`, `action.execute`, `connection.test`, Transforms als Strom (`transform.open/batch/end/emit/closed`, `$/ack`), `transform.init`, `transform.detectFields`, `$/cancelRequest`, `shutdown` |
| Runnel | Actions und Connection-Typen ab S49c, Transforms ab S50a, `init`/`detectFields` ab S50b |
| Lizenz | MIT; ohne Abhängigkeiten außer der Go-Standardbibliothek, Plugins erben also keine fremden Lizenzen (die CI prüft das mit `make deps`) |

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

Das vollständige Beispiel mit Tests – Action und Transform `hello.greet` – liegt in [`examples/hello`](examples/hello). Bauen und im Projekt ablegen:

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

## Kategorie und Gruppe in der Palette

`Category` ist optional. Ohne Kategorie zeigt der Editor die Bausteine eines Plugins in einer eigenen Gruppe
„Plugin: \<name\>“; so sieht man, woher sie kommen. Mit einer Kategorie der eingebauten stehen sie in deren Gruppe:

| | Kategorien |
|---|---|
| Actions | `CategoryGeneral`, `CategoryExecution`, `CategoryFiles`, `CategoryNetwork`, `CategoryDatabase` |
| Transforms | `CategoryInput`, `CategoryTransform`, `CategoryFlow`, `CategoryExecution`, `CategoryCombine`, `CategoryOutput` |

Eine Eingabe (ohne eingehende Links) braucht `CategoryInput`. Runnel vor runnel#236 zeigt Bausteine ohne Kategorie
unter „General“ bzw. „Transform“.

## Konfiguration als Struct

Das JSON Schema der Konfiguration (`ConfigSchema`, `ConfigSchemaFor`) entsteht aus den Tags – genau wie bei den eingebauten Actions von Runnel (ADR 0014).
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

## Transforms

Ein Transform liest Batches mit `Next` und schreibt Records mit `Emit`; das SDK hält die Kredite in beide Richtungen
ein. Derselbe Typname darf zugleich Action sein.

```go
runnel.AddTransform(p, runnel.Transform[greetConfig]{
	Type: "hello.greet", Label: "Greet", Description: "Adds a greeting to every record",
	RecordErrors: true, // darf einen Fehler-Link haben
	Parallel:     true, // erlaubt copies
	Run: func(ctx context.Context, s *runnel.Stream, c greetConfig) error {
		for {
			b, ok, err := s.Next(ctx) // ok == false: keine Batches mehr
			if err != nil || !ok {
				return err
			}
			at := b.Fields.Index(c.Field)
			out := append(slices.Clone(b.Fields), runnel.Field{Name: "greeting", Type: runnel.TypeString})
			var good, bad [][]any
			for _, row := range b.Rows {
				if name, _ := row[at].(string); name != "" {
					good = append(good, append(row, "Hello, "+name))
				} else {
					bad = append(bad, runnel.ErrorRow(row, "name is empty", c.Field))
				}
			}
			if err := s.Emit(ctx, runnel.ToMain, out, good); err != nil {
				return err
			}
			if err := s.Emit(ctx, runnel.ToError, runnel.ErrorFields(b.Fields), bad); err != nil {
				return err
			}
		}
	},
})
```

- **Ziele von `Emit`:** `runnel.ToMain` (alle Daten-Links ohne eigenes Ziel), `runnel.ToError` (Fehler-Link; Records mit
  `ErrorFields`/`ErrorRow`, also `_error` und `_errorField`) oder die ID eines Transforms aus einem Feld mit
  `format=runnel-target`.
- **Eingaben** (`Category: runnel.CategoryInput`) rufen `Next` nicht auf und schreiben nur mit `Emit`. Die Kategorie
  `input` brauchen sie, damit der Editor keinen Link hinein anbietet.
- **Kredite:** Das SDK nimmt höchstens `StreamWindow` (4) Batches an, die `Next` noch nicht geliefert hat; `Emit`
  wartet, bis Runnel frühere Records weitergegeben hat. Eingehende Nachrichten liest das SDK in einer eigenen
  Goroutine, deshalb hängt ein wartendes `Emit` nie an einem nicht gelesenen `$/ack`. Schickt Runnel mehr Batches als
  vereinbart, endet der Strom mit Fehler, statt Records zu verwerfen.
- **Größe:** Records, die nicht in eine Nachricht (16 MiB) passen, schickt `Emit` in mehreren `emit`; ein einzelner
  Record über 16 MiB ist ein Fehler des Transforms.
- **Werte** in `Rows`, beim Lesen mit `Next` wie beim Schreiben mit `Emit`: `int` als `int64`, `float` als `float64`,
  `decimal` als Text (`"12.50"`, exakt), `date` als Text (`"2026-11-24"`), `datetime` als `time.Time` (beim Schreiben
  auch RFC-3339-Text), `binary` als `[]byte`, `json` als JSON-Wert, `null` als `nil`.
- **Ein Fehler** aus `Run` (oder ein `panic`) beendet den Strom mit `transform.closed` und Meldung; die Pipeline
  scheitert. Mehrere Ströme (auch `copies`) laufen in einem Prozess nebeneinander.
- **`Init`** (optional) prüft die Konfiguration und nennt die Felder des Ausgangs (`InitResult` mit `Output` und
  `Problems` am Pfad in `with`). Runnel ruft es beim Prüfen ohne Secrets und mit Connections nur als Name auf; Werte,
  die noch `${…}` sind und nicht passen, bleiben leer.
- **`DetectFields`** (optional) liest für „Detect fields“ im Editor die ersten Records einer Eingabe und schlägt Felder
  mit Typ und Beispielwerten vor; Connections kommen dabei aufgelöst.
- **Feldkopf:** Das SDK schickt `fields` mit jedem `emit`. Das Protokoll erlaubt, ihn bei gleichen Feldern
  wegzulassen; Runnel vor dem Fix in runnel#228 liest ein solches `emit` aber als eines ohne Felder.

Im Test: `c.RunTransform(typ, config, batches...)` (Kredite wie bei Runnel, Ergebnis mit `Records(runnel.ToMain)`),
`c.Init(…)`, `c.DetectFields(…)`. Durchsatz mit Runnel (Beispiel-Plugin, 500.000 Records mit 5 Feldern, 4 Kerne):
etwa 360.000 Records/s; `make bench` misst den Strom ohne Runnel.

## Was das SDK übernimmt

- **Start:** Ohne `RUNNEL_PLUGIN=1` gibt `Main` einen Hinweis aus und endet mit Exit-Code 2.
- **Handshake:** `initialize` mit `protocolVersion: 1`, `encoding: json`, Name, Version und `concurrency`
  (Standard 4, `p.Concurrency`); `describe` mit Schemas aus den Structs.
- **Parallelität:** höchstens `Concurrency` Anfragen gleichzeitig, jede in einer eigenen Goroutine.
- **Abbruch:** `$/cancelRequest` beendet den `ctx` der Action; das SDK antwortet mit `-32800`.
- **Fehler:** Ein `error` aus `Run` wird zur Meldung der Action (`-32000`); ein `panic` ebenso, das Plugin läuft weiter.
  Unbekannte Methoden: `-32601`. Eine Antwort über 16 MiB (z. B. ein sehr großes `output`) wird zur Fehlermeldung der
  Action, statt das Plugin zu beenden; eingehende Nachrichten über 16 MiB beenden es.
- **Log:** `call.Log.Debug/Info/Warn/Error` schreibt Zeilen mit Stufe auf stderr; Runnel zeigt sie im Log des Laufs.
- **stdout:** gehört allein dem Protokoll. `Main` leitet `os.Stdout` auf stderr um, sodass `fmt.Println` oder das Log
  einer Bibliothek nur im Log landen, statt das Protokoll zu zerstören. Code, der direkt auf den Dateideskriptor 1
  schreibt (z. B. C-Bibliotheken), erfasst das nicht.

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
| Tests (mit Race-Detector) | `make test` – dazu Transkripte des Protokolls in `testdata/protocol/` (Golden-Dateien) und Stresstests mit Prüfung auf übrig gebliebene Goroutinen |
| Lint (golangci-lint v2.15.0, Linux- und Windows-Code) | `make lint` (ohne passendes golangci-lint: `make bin/tools/golangci-lint lint GOLANGCI_LINT=bin/tools/golangci-lint`) |
| Fuzzing (Protokoll-Schleife, Zeilen der Batches, Konfiguration) | `make fuzz FUZZTIME=1m` (je Ziel) |
| Transkripte neu schreiben, wenn sich das Protokoll bewusst ändert | `make golden`, danach den Diff in `testdata/protocol/` prüfen |
| API mit dem letzten Tag vergleichen | `make api` (apidiff; Inkompatibles nur mit Eintrag „Inkompatibel“ unter „Unveröffentlicht“ im CHANGELOG) |
| Keine Abhängigkeiten | `make deps` |
| Schwachstellen (auch wöchentlich in der CI) | `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` |
| Benchmark (Strom eines Transforms) | `make bench` |
| Tests gegen Runnel (in der CI unter Linux und Windows) | `make runnel-test RUNNEL_BIN=…` |

Versionen nach SemVer mit Tags (`v0.1.0`, Go-Module kommen direkt aus dem Tag, ein Release-Workflow ist nicht nötig); bis `v1.0.0` kann sich die Go-API noch ändern, das Protokoll nicht.
Änderungen stehen in [CHANGELOG.md](CHANGELOG.md).

## Plugins mit diesem SDK

- [`runnelhq/plugin-iban`](https://github.com/runnelhq/plugin-iban) – IBAN und BIC prüfen, IBAN bilden; Transform mit Fehler-Link
- [`runnelhq/plugin-ldap`](https://github.com/runnelhq/plugin-ldap) – LDAP und Active Directory: suchen, anmelden, ändern; Eingabe mit „Detect fields“
