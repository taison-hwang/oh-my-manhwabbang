package userdata

import (
	"context"

	"shelf/internal/principal"
)

// scope says which set of tables a call reads and writes (ruling E-74 §2.13).
//
// The scope is whatever principal.From finds in the context. With a principal
// the call sees only that user's rows in the user_* tables; without one it uses
// the pre-E-74 tables, which is what the no-auth and password modes, the scan's
// own bookkeeping and the offline tools have always done. The HTTP layer is what
// guarantees a per-user server never reaches here without a principal.
//
// The table names are two fixed spellings and the user is only ever a bound
// parameter, never text. By convention the user parameter is the first argument
// of a statement and its predicate the first in the WHERE, so a query is
// composed as `where(...)` plus `args(...)`.
type scope struct{ sub string }

func scopeOf(ctx context.Context) scope {
	if p, ok := principal.From(ctx); ok {
		return scope{sub: p.Sub}
	}
	return scope{}
}

func (s scope) perUser() bool { return s.sub != "" }

func (s scope) progress() string {
	if s.perUser() {
		return "user_progress"
	}
	return "progress"
}

func (s scope) prefs() string {
	if s.perUser() {
		return "user_book_prefs"
	}
	return "book_prefs"
}

func (s scope) settings() string {
	if s.perUser() {
		return "user_settings"
	}
	return "settings"
}

func (s scope) viewState() string {
	if s.perUser() {
		return "user_view_state"
	}
	return "view_state"
}

// insCols and insVals extend an INSERT's column and value lists with user_sub;
// conflict extends the ON CONFLICT target. All three are empty for the old tables.
func (s scope) insCols() string {
	if s.perUser() {
		return "user_sub, "
	}
	return ""
}

func (s scope) insVals() string {
	if s.perUser() {
		return "?, "
	}
	return ""
}

func (s scope) conflict() string { return s.insCols() }

// where renders a WHERE clause: the user predicate (when scoped) followed by
// cond. cond may be empty. The caller passes args(...) in the same order.
func (s scope) where(cond string) string {
	switch {
	case s.perUser() && cond != "":
		return " WHERE user_sub = ? AND (" + cond + ")"
	case s.perUser():
		return " WHERE user_sub = ?"
	case cond != "":
		return " WHERE " + cond
	}
	return ""
}

// args puts the user parameter in front of the statement's other arguments,
// where where and insVals expect it. Unscoped it is the identity.
func (s scope) args(rest ...any) []any {
	if !s.perUser() {
		return rest
	}
	return append([]any{s.sub}, rest...)
}
