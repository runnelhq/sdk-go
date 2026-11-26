# Änderungen

## v0.3.0 (2026-11-26)

- **Neu:** `Reference` in `Action` und `Transform` (`Outputs`, `Records`, `Errors`, `Limits`) beschreibt Ergebnis,
  Fehler und Grenzen wie bei den eingebauten Bausteinen (ADR 0014 von Runnel, E15b); `describe` liefert sie als
  `outputs`, `records`, `errors` und `limits`. `AddAction`/`AddTransform` lehnen unbekannte Typen und `Records` an einer
  Action mit `panic` ab; `runnel.OutputTypes()` nennt die Typen der Ausgaben von Actions.
- **Neu:** `runneltest.CheckReference` prüft die Referenzen eines Plugins mit den Regeln der Vertragstests von Runnel
  (freiwillig, z. B. in einem eigenen Test).
- **Geändert:** Nachrichten an Runnel schreiben `<`, `>` und `&` unverändert statt als `\u003c` usw. (gleiches JSON,
  wie im Rust-SDK); so stehen Namen wie `<field>` lesbar im Transkript.
- Beispiel `hello` mit Referenz und `TestReference`.

## v0.2.1 (2026-11-26)

- **Behoben:** IDs nach ADR 0019 (Nachtrag 2026-11-26): Eine Anfrage, deren ID weder eine ganze Zahl im Bereich von
  int64 noch ein Text ist (z. B. `1e999`, `1.5`, `true`, ein Objekt), beantwortet das SDK mit `-32600` und
  `"id": null`, statt die ID zurückzuschicken. Text-IDs von `$/cancelRequest` finden ihre Anfrage unabhängig von der
  Schreibweise (`"a\u0062"` = `"ab"`).
- **Behoben:** Leere Listen gehen als `[]` statt `null` an Runnel: `output.fields` der Antwort auf `transform.init`
  und `fields` von `Emit` mit `nil`. Dass das SDK `null` von Runnel als leere Liste liest, prüft jetzt ein Transkript.

## v0.2.0 (2026-11-25)

- **Geändert:** Die Kategorie von Actions und Transforms ist optional. Ohne `Category` gibt `describe` keine
  Kategorie mehr an (bisher `general` bzw. `transform`); Runnel zeigt den Baustein dann in einer eigenen Gruppe
  „Plugin: \<name\>“ der Palette (ab runnel#236, ältere Versionen wie bisher unter General bzw. Transform). Wer die
  bisherige Gruppe behalten will, setzt `Category: runnel.CategoryGeneral` bzw. `runnel.CategoryTransform`. Eine
  Eingabe braucht weiter `runnel.CategoryInput`, damit der Editor keinen Link hinein anbietet.

## v0.1.0 (2026-11-25)

- Erste Version für Runnel ab S49c: Handshake, `describe` mit Schemas aus Struct-Tags, `action.execute`,
  Connection-Typen mit `connection.test`, `concurrency`, `$/cancelRequest`, Log über stderr.
- Transforms (`AddTransform`): Strom mit Krediten in beide Richtungen, Ziele `main`, `error` und `runnel-target`,
  optional `Init` (`transform.init`) und `DetectFields` (`transform.detectFields`); für Runnel ab S50b.
- `runneltest`: Client für Tests ohne Runnel (`RunTransform`, `Init`, `DetectFields`), `NewProject` für Tests gegen
  Runnel (`RUNNEL_BIN`).
- Beispiel-Plugin `examples/hello` mit Action und Transform.
- Robustheit vor dem ersten Release:
  - Mehr Batches als das Fenster beenden den Strom mit Fehler, statt Records zu verwerfen.
  - `Main` leitet `os.Stdout` auf stderr um; Ausgaben mit `fmt.Println` zerstören das Protokoll nicht mehr.
  - `Emit` teilt Records, die nicht in eine Nachricht passen (16 MiB); ein zu großer Record und eine zu große Antwort
    einer Action sind Fehlermeldungen, statt dass Runnel das Plugin beendet.
- Namen vor dem ersten Release festgelegt: `ConfigSchema` und `ConfigSchemaFor` (JSON Schema der Konfiguration, vorher
  `Schema` und `SchemaFor`), `RecordSchema` (Felder der Datensätze), Ziele `ToMain` und `ToError` (vorher `Main` und
  `Error`). `Next` liefert `datetime` als `time.Time` und `binary` als `[]byte`, wie `Emit` sie nimmt.
- Qualitätssicherung: Transkripte des Protokolls als Golden-Dateien, API-Vergleich mit dem letzten Tag (`make api`),
  Fuzzing auch der Batch-Zeilen und der Konfiguration, Stresstests mit vielen Strömen, Abbrüchen und Ende von stdin,
  Tests gegen Runnel auch unter Windows.
