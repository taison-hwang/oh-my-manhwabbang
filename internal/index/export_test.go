package index

import "time"

// SchemaV1 exposes the first rung of the ladder to the external test package,
// so a migration test can build a genuine v1 file — the DDL as v1 shipped it —
// instead of approximating one by taking columns out of a current file.
const SchemaV1 = schemaV1

// SetClock replaces the clock that dates a batch's opening and judges its age,
// so a test can place the boundary exactly instead of racing the wall clock.
func (w *Writer) SetClock(now func() time.Time) { w.now = now }
