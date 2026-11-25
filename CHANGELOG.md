# Änderungen

## Unveröffentlicht (v0.1.0)

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
