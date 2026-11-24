# Änderungen

## Unveröffentlicht (v0.1.0)

- Erste Version für Runnel ab S49c: Handshake, `describe` mit Schemas aus Struct-Tags, `action.execute`,
  Connection-Typen mit `connection.test`, `concurrency`, `$/cancelRequest`, Log über stderr.
- Transforms (`AddTransform`): Strom mit Krediten in beide Richtungen, Ziele `main`, `error` und `runnel-target`,
  optional `Init` (`transform.init`) und `DetectFields` (`transform.detectFields`); für Runnel ab S50b.
- `runneltest`: Client für Tests ohne Runnel (`RunTransform`, `Init`, `DetectFields`), `NewProject` für Tests gegen
  Runnel (`RUNNEL_BIN`).
- Beispiel-Plugin `examples/hello` mit Action und Transform.
