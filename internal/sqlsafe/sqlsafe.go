// Package sqlsafe provides a conservative validator for the single read-only
// SELECT statement that MySQL health checks are allowed to execute.
//
// It is intentionally strict ("reject ambiguous") and is a parser aid only:
// the real security boundary is the dedicated read-only database account
// (grants), as mandated by the vshm spec. The validator never returns SQL
// text in errors.
//
// Accepted grammar (chosen answer to the WP-07 open question on grammar):
//   - exactly one statement; leading keyword must be SELECT (case-insensitive)
//   - at most one optional trailing semicolon (whitespace allowed before it)
//   - no semicolon anywhere else, including inside string/identifier quotes
//   - no comments of any kind ("--", "#", "/* ... */" including MySQL
//     executable comments "/*! ... */")
//   - no WITH/CTE or other leading statements (they do not start with SELECT)
//   - forbidden keywords anywhere outside string literals: write, lock and
//     session keywords (INTO, FOR, LOCK, INSERT, UPDATE, DELETE, ...)
//   - maximum length MaxQueryLen
package sqlsafe

import (
	"errors"
	"fmt"
	"strings"
)

// MaxQueryLen is the maximum accepted query length in bytes.
const MaxQueryLen = 4096

// blockedKeywords are rejected as bare word tokens outside string literals.
// They cover file-writing SELECT variants (INTO OUTFILE/DUMPFILE), locking
// reads (FOR UPDATE, LOCK IN SHARE MODE) and, defensively, every other
// statement-leading keyword so a smuggled second operation fails closed.
var blockedKeywords = map[string]bool{
	"INTO": true, "FOR": true, "LOCK": true,
	"INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true,
	"DROP": true, "ALTER": true, "CREATE": true, "TRUNCATE": true,
	"RENAME": true, "GRANT": true, "REVOKE": true,
	"SET": true, "USE": true, "CALL": true, "EXEC": true, "EXECUTE": true,
	"PREPARE": true, "DEALLOCATE": true, "LOAD": true, "HANDLER": true,
	"DO": true, "MERGE": true, "SHOW": true, "DESCRIBE": true, "EXPLAIN": true,
	"KILL": true, "SHUTDOWN": true, "INSTALL": true, "UNINSTALL": true,
	"BEGIN": true, "START": true, "COMMIT": true, "ROLLBACK": true,
	"SAVEPOINT": true, "XA": true, "FLUSH": true, "PURGE": true,
	"ANALYZE": true, "OPTIMIZE": true, "CHECK": true, "REPAIR": true,
}

// Validate checks that q is exactly one read-only SELECT statement and
// returns the normalized statement (trimmed, single trailing semicolon
// stripped) safe to send to the server. Errors never contain SQL text.
func Validate(q string) (string, error) {
	if len(q) > MaxQueryLen {
		return "", errors.New("query exceeds maximum length")
	}
	s := strings.TrimSpace(q)
	if s == "" {
		return "", errors.New("query is empty")
	}
	// Strip at most one trailing semicolon (whitespace allowed before it).
	if strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(s[:len(s)-1])
		if s == "" {
			return "", errors.New("query is empty")
		}
	}
	// Conservative: reject any remaining semicolon, including quoted ones.
	if strings.ContainsRune(s, ';') {
		return "", errors.New("multiple statements or semicolon inside statement rejected")
	}
	toks, err := tokenize(s)
	if err != nil {
		return "", err
	}
	if len(toks) < 2 || !strings.EqualFold(toks[0], "select") {
		return "", errors.New("statement must be a single SELECT")
	}
	for _, t := range toks[1:] {
		if blockedKeywords[strings.ToUpper(t)] {
			return "", errors.New("forbidden keyword in statement rejected")
		}
	}
	return s, nil
}

// tokenize splits s into bare word tokens, skipping over quoted string and
// identifier literals. Any comment marker outside a literal is an error.
func tokenize(s string) ([]string, error) {
	var toks []string
	var word strings.Builder
	depth := 0
	flush := func() {
		if word.Len() > 0 {
			toks = append(toks, word.String())
			word.Reset()
		}
	}
	i := 0
	for i < len(s) {
		ch := s[i]
		switch {
		case ch == '\'' || ch == '"' || ch == '`':
			flush()
			quote := ch
			i++
			closed := false
			for i < len(s) {
				if s[i] == '\\' && quote != '`' {
					i += 2 // backslash escape inside string literals
					continue
				}
				if s[i] == quote {
					if i+1 < len(s) && s[i+1] == quote {
						i += 2 // doubled quote escape
						continue
					}
					closed = true
					i++
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated literal rejected")
			}
			toks = append(toks, "<literal>") // literal counts as content, never inspected
		case ch == '-' && i+1 < len(s) && s[i+1] == '-':
			return nil, errors.New("comments rejected")
		case ch == '#':
			return nil, errors.New("comments rejected")
		case ch == '/' && i+1 < len(s) && s[i+1] == '*':
			return nil, errors.New("comments rejected")
		case ch == '(':
			flush()
			depth++
			i++
		case ch == ')':
			flush()
			depth--
			if depth < 0 {
				return nil, errors.New("unbalanced parentheses rejected")
			}
			i++
		case isWordChar(ch):
			word.WriteByte(ch)
			i++
		default:
			flush()
			i++
		}
	}
	flush()
	if depth != 0 {
		return nil, errors.New("unbalanced parentheses rejected")
	}
	return toks, nil
}

func isWordChar(ch byte) bool {
	return ch == '_' || ch == '$' ||
		(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
}

// String is a short constant description for logs; it never contains SQL.
func String() string { return fmt.Sprintf("single read-only SELECT, max %d bytes", MaxQueryLen) }
