package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const sampleTypes = `
export const KINDS = ['zip', 'dir'] as const
export type Kind = (typeof KINDS)[number]

export const CODES = [
  'a', // 409  ?v= does not match the book's current cv
  'b', // 500
] as const
export type Code = (typeof CODES)[number]

export interface Inner {
  n: number
  label: string | null
}

export interface Base {
  id: string
  kind: Kind
}

export interface Thing extends Base {
  inner: Inner
  list: Inner[]
  detail?: Record<string, unknown>
}
`

func mustParse(t *testing.T) *tsTypes {
	t.Helper()
	ts, err := parseTypes(sampleTypes)
	if err != nil {
		t.Fatalf("parseTypes: %v", err)
	}
	return ts
}

// The apostrophe in "book's" inside a trailing comment used to open a quote and
// swallow the next member. It is the reason stripLineComments exists.
func TestParseTypes_commentApostropheDoesNotEatMembers(t *testing.T) {
	t.Parallel()
	ts := mustParse(t)
	got := ts.enums["Code"]
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("CODES parsed as %q, want [a b]", got)
	}
}

func TestParseTypes_extendsIsFlattened(t *testing.T) {
	t.Parallel()
	ts := mustParse(t)
	names := map[string]bool{}
	for _, f := range ts.interfaces["Thing"] {
		names[f.name] = true
	}
	for _, want := range []string{"id", "kind", "inner", "list", "detail"} {
		if !names[want] {
			t.Errorf("Thing is missing field %q after flattening extends: %v", want, names)
		}
	}
}

func check1(t *testing.T, ts *tsTypes, body string) []string {
	t.Helper()
	var doc any
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&doc); err != nil {
		t.Fatalf("bad test JSON: %v", err)
	}
	c := &checker{ts: ts, file: "sample.json"}
	c.value(doc, typeRef{name: "Thing"}, "")
	return c.findings
}

func TestChecker_agreementProducesNoFindings(t *testing.T) {
	t.Parallel()
	got := check1(t, mustParse(t), `{
	  "id": "x", "kind": "zip",
	  "inner": {"n": 1, "label": null},
	  "list": [{"n": 2, "label": "hi"}]
	}`)
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %v", got)
	}
}

func TestChecker_findsEveryKindOfDisagreement(t *testing.T) {
	t.Parallel()
	ts := mustParse(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "field the client does not declare",
			body: `{"id":"x","kind":"zip","inner":{"n":1,"label":null},"list":[],"surprise":1}`,
			want: "does not declare it",
		},
		{
			name: "field the client requires and the server omits",
			body: `{"kind":"zip","inner":{"n":1,"label":null},"list":[]}`,
			want: "requires this field",
		},
		{
			name: "enum value outside the union",
			body: `{"id":"x","kind":"folder","inner":{"n":1,"label":null},"list":[]}`,
			want: `"folder" is not a member of Kind`,
		},
		{
			name: "null where the type does not admit it",
			body: `{"id":"x","kind":"zip","inner":{"n":1,"label":null},"list":null}`,
			want: "does not admit null",
		},
		{
			name: "nested field the client does not declare",
			body: `{"id":"x","kind":"zip","inner":{"n":1,"label":null,"extra":2},"list":[]}`,
			want: "inner.extra",
		},
		{
			name: "array element checked as its own object",
			body: `{"id":"x","kind":"zip","inner":{"n":1,"label":null},"list":[{"n":1}]}`,
			want: "list[0].label",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := check1(t, ts, tc.body)
			joined := strings.Join(got, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("findings did not mention %q:\n%s", tc.want, joined)
			}
		})
	}
}
