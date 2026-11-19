package sqlsafe

import (
	"strings"
	"testing"
)

func TestValidateAcceptsSingleSelect(t *testing.T) {
	valid := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "SELECT 1", "SELECT 1"},
		{"lowercase", "select 1", "select 1"},
		{"mixed case", "Select * From t", "Select * From t"},
		{"trailing semicolon", "SELECT 1;", "SELECT 1"},
		{"semicolon after space", "SELECT 1 ;", "SELECT 1"},
		{"semicolon before trailing space", "SELECT 1;  ", "SELECT 1"},
		{"surrounding whitespace", "  \n SELECT 1 \t ", "SELECT 1"},
		{"columns and where", "SELECT a, b FROM t WHERE x = 1 AND y = 'O''Brien'", "SELECT a, b FROM t WHERE x = 1 AND y = 'O''Brien'"},
		{"quoted string with special chars", `SELECT * FROM t WHERE note = "it's <fine> & dandy"`, `SELECT * FROM t WHERE note = "it's <fine> & dandy"`},
		{"backtick identifier", "SELECT `col` FROM `my table`", "SELECT `col` FROM `my table`"},
		{"escaped quote in string", `SELECT 'a\'b'`, `SELECT 'a\'b'`},
		{"doubled quote in string", "SELECT 'a''b'", "SELECT 'a''b'"},
		{"union is one statement", "SELECT 1 UNION SELECT 2", "SELECT 1 UNION SELECT 2"},
		{"subquery", "SELECT * FROM t WHERE id IN (SELECT id FROM u)", "SELECT * FROM t WHERE id IN (SELECT id FROM u)"},
		{"parenthesized", "(SELECT 1)", "(SELECT 1)"},
		{"join", "SELECT a.x FROM a JOIN b ON a.id = b.id", "SELECT a.x FROM a JOIN b ON a.id = b.id"},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			got, err := Validate(tc.input)
			if err != nil {
				t.Fatalf("Validate(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("Validate(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestValidateRejectsNonSelect(t *testing.T) {
	invalid := []string{
		"",
		"   ",
		";",
		";;",
		"SELECT",
		"SELECT 1; SELECT 2",
		"SELECT 1;;",
		"SELECT 1; DROP TABLE t",
		"SELECT 'a;b'",        // quoted semicolon: conservative reject
		"SELECT `a;b` FROM t", // quoted semicolon in identifier: reject
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET x = 1",
		"DELETE FROM t",
		"REPLACE INTO t VALUES (1)",
		"DROP TABLE t",
		"ALTER TABLE t ADD c INT",
		"CREATE TABLE t (id INT)",
		"TRUNCATE TABLE t",
		"GRANT SELECT ON *.* TO u",
		"SET @x = 1",
		"USE db",
		"SHOW TABLES",
		"WITH x AS (SELECT 1) SELECT * FROM x", // CTEs: conservative reject
		"SELECT 1 -- trailing comment",
		"SELECT 1 # trailing comment",
		"SELECT /*+ hint */ 1",
		"SELECT /*!40101 SET @x=1 */ 1", // MySQL executable comment
		"SELECT 1 /* comment */",
		"SELECT * FROM t INTO OUTFILE '/tmp/x'",
		"SELECT * FROM t INTO DUMPFILE '/tmp/x'",
		"SELECT * FROM t FOR UPDATE",
		"SELECT * FROM t LOCK IN SHARE MODE",
		"SELECT 'unterminated",
		"SELECT `unterminated",
		"(SELECT 1",
		"CALL proc()",
		"DO 1",
	}
	for _, input := range invalid {
		t.Run("invalid/"+input, func(t *testing.T) {
			got, err := Validate(input)
			if err == nil {
				t.Fatalf("Validate(%q) = %q, want error", input, got)
			}
			if strings.Contains(err.Error(), input) && len(input) > 8 {
				t.Errorf("error echoes SQL text: %v", err)
			}
		})
	}
}

func TestValidateRejectsTooLong(t *testing.T) {
	long := "SELECT " + strings.Repeat("a", MaxQueryLen)
	if _, err := Validate(long); err == nil {
		t.Fatal("expected error for over-long query")
	}
}

func TestValidateKeywordInsideStringIsAllowed(t *testing.T) {
	// Keywords inside string literals are data, not operations.
	if _, err := Validate("SELECT * FROM t WHERE note = 'for update'"); err != nil {
		t.Fatalf("keyword inside string literal should be allowed: %v", err)
	}
}
