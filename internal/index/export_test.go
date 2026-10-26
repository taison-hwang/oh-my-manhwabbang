package index

// SchemaV1 exposes the first rung of the ladder to the external test package,
// so a migration test can build a genuine v1 file — the DDL as v1 shipped it —
// instead of approximating one by taking columns out of a current file.
const SchemaV1 = schemaV1
