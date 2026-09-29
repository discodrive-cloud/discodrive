package db

import "strings"

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike escapes s for use inside a LIKE/ILIKE pattern, so that user input
// such as "100%" or "a_b" matches literally. Postgres uses backslash as the
// default LIKE escape character, so no ESCAPE clause is needed.
func EscapeLike(s string) string { return likeEscaper.Replace(s) }
