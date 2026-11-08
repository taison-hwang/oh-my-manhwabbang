package index

import (
	"context"
	"regexp"

	"shelf/internal/principal"
)

// progressRef matches the one spelling every reading-progress query uses for the
// single-user table.
var progressRef = regexp.MustCompile(`ud\.progress\b`)

// userProgress replaces it for a request that carries a principal: the user's
// own rows of ud.user_progress, behind a CTE whose single parameter is the
// user's sub (ruling E-74 §2.13). The statement text only ever gets one of two
// fixed table names; the user is a bound parameter.
const (
	userProgress = `(SELECT * FROM ud.user_progress WHERE user_sub = (SELECT sub FROM shelf_me))`
	userPrefix   = `WITH shelf_me(sub) AS (SELECT ?) `
)

// scopeProgress rewrites a query that reads reading progress so a request with a
// principal sees only that user's rows. Without a principal — no-auth and
// password servers, and the scan's own passes — it returns the query unchanged
// and reads the single-user table, as before. The HTTP layer is what refuses a
// per-user request that has no principal.
//
// The user parameter becomes the statement's first argument, which is why every
// caller passes its arguments through here rather than appending to them.
func scopeProgress(ctx context.Context, query string, args ...any) (string, []any) {
	p, ok := principal.From(ctx)
	if !ok {
		return query, args
	}
	return userPrefix + progressRef.ReplaceAllString(query, userProgress),
		append([]any{p.Sub}, args...)
}
