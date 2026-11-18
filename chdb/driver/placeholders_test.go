package chdbdriver

import (
	"strings"
	"testing"
)

// markTokens shows what scanQuery found: each comment wrapped in «», each
// placeholder replaced by <?>.
func markTokens(query string) string {
	pos, comments := scanQuery(query)
	var b strings.Builder
	last := 0
	for len(pos) > 0 || len(comments) > 0 {
		if len(comments) == 0 || len(pos) > 0 && pos[0] < comments[0][0] {
			b.WriteString(query[last:pos[0]] + "<?>")
			last, pos = pos[0]+1, pos[1:]
		} else {
			c := comments[0]
			b.WriteString(query[last:c[0]] + "«" + query[c[0]:c[1]] + "»")
			last, comments = c[1], comments[1:]
		}
	}
	return b.String() + query[last:]
}

// The expectations follow ClickHouse's lexer (src/Parsers/Lexer.cpp); the
// cases that are valid queries were also checked against chDB.
func TestScanQuery(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{"", ""},
		{"?", "<?>"},
		{"SELECT ?, ?", "SELECT <?>, <?>"},

		// '...' strings: backslash and doubled-quote escapes.
		{"SELECT '?', ?", "SELECT '?', <?>"},
		{"SELECT 'it''s ?', ?", "SELECT 'it''s ?', <?>"},
		{`SELECT 'a\'?', ?`, `SELECT 'a\'?', <?>`},
		{`SELECT 'a\\', ?`, `SELECT 'a\\', <?>`},
		{"SELECT '-- /* // # ', ?", "SELECT '-- /* // # ', <?>"},
		{"SELECT 'é?', ?", "SELECT 'é?', <?>"},
		{"SELECT '? -- unclosed", "SELECT '? -- unclosed"},

		// "..." and `...` identifiers, with the same escapes.
		{`SELECT "a""?", "b\"?", ?`, `SELECT "a""?", "b\"?", <?>`},
		{"SELECT `a``?`, `b\\`?`, ?", "SELECT `a``?`, `b\\`?`, <?>"},
		{"SELECT `? -- unclosed", "SELECT `? -- unclosed"},

		// ‘...’ strings and “...” identifiers end at the first closing quote.
		{"SELECT ‘?--’, “?”, ?", "SELECT ‘?--’, “?”, <?>"},
		{"SELECT ‘a\\’?’, ?", "SELECT ‘a\\’<?>’, <?>"},

		// x'..' and b'..' strings; any other character leaves them unclosed.
		{"SELECT x'3F', B'0011', ?", "SELECT x'3F', B'0011', <?>"},
		{"SELECT ax'?', ?", "SELECT ax'?', <?>"},
		{"SELECT x'?', ?", "SELECT x'?', ?"},

		// Heredocs: $tag$...$tag$ with a tag of word characters, or $$...$$.
		{"SELECT $h$a/*keep*/b$h$, ?", "SELECT $h$a/*keep*/b$h$, <?>"},
		{"SELECT $$-- ? 'x$$, ?", "SELECT $$-- ? 'x$$, <?>"},
		{"SELECT $h$?$H$?$h$, ?", "SELECT $h$?$H$?$h$, <?>"},
		{"SELECT $1$?$1$, $_$?$_$, ?", "SELECT $1$?$1$, $_$?$_$, <?>"},
		{"SELECT 1$h$?$h$", "SELECT 1$h$?$h$"},
		{"SELECT $h-$?$h-$", "SELECT $h-$<?>$h-$"},
		{"SELECT a$h$?$h$", "SELECT a$h$<?>$h$"},
		{"SELECT $h$? -- unclosed", "SELECT $h$<?> «-- unclosed»"},
		{"SELECT $ ?", "SELECT $ <?>"},

		// -- comments run to the newline, which is not part of them.
		{"SELECT 1 -- ?\n, ?", "SELECT 1 «-- ?»\n, <?>"},
		{"SELECT 1 --\r?\n?", "SELECT 1 «--\r?»\n<?>"},
		{"SELECT ? -- ?", "SELECT <?> «-- ?»"},
		{"SELECT 1 - -?", "SELECT 1 - -<?>"},

		// // comments, like --.
		{"SELECT 6 // ?\n, ?", "SELECT 6 «// ?»\n, <?>"},
		{"SELECT 6 / ?", "SELECT 6 / <?>"},
		{"SELECT 6 /\n/ ?", "SELECT 6 /\n/ <?>"},

		// # comments only when followed by a space or !.
		{"SELECT ? # ?\n, ?", "SELECT <?> «# ?»\n, <?>"},
		{"SELECT ? #! ?\n, ?", "SELECT <?> «#! ?»\n, <?>"},
		{"SELECT ? #?", "SELECT <?> #<?>"},
		{"SELECT ? #\t?", "SELECT <?> #\t<?>"},

		// /* */ comments nest.
		{"SELECT /* /* ? */ ? */ ?", "SELECT «/* /* ? */ ? */» <?>"},
		{"SELECT /*/ ? */ ?", "SELECT «/*/ ? */» <?>"},
		{"SELECT /* '*/ ?, ' */", "SELECT «/* '*/» <?>, ' */"},
		{"SELECT /* ?", "SELECT «/* ?»"},
		{"SELECT /* /* */ ?", "SELECT «/* /* */ ?»"},

		// A number takes the sign of its exponent, so 1e--? is 1e- - ?.
		{"SELECT 1e-1--?\n, ?", "SELECT 1e-1«--?»\n, <?>"},
		{"SELECT 1e--?", "SELECT 1e--<?>"},
		{"SELECT (.5e--?", "SELECT (.5e--<?>"},
		{"SELECT (.5e-1--?\n, ?", "SELECT (.5e-1«--?»\n, <?>"},
		{"SELECT 1_000e-1--?\n, ?", "SELECT 1_000e-1«--?»\n, <?>"},
		{"SELECT 0x1e--?\n, ?", "SELECT 0x1e«--?»\n, <?>"},
		{"SELECT 0x1p--?", "SELECT 0x1p--<?>"},
		// After a word, a closing bracket, a quoted identifier or a number a
		// dot is tuple access, and only the digits after it are a number:
		// t.1e is t . 1e, and SELECT .5e is SELECT . 5e.
		{"SELECT t.1e--?\n, ?", "SELECT t.1e«--?»\n, <?>"},
		{"SELECT .5e--?\n, ?", "SELECT .5e«--?»\n, <?>"},
		{"SELECT (t).1e--?\n, ?", "SELECT (t).1e«--?»\n, <?>"},
	} {
		if got := markTokens(tc.query); got != tc.want {
			t.Errorf("scanQuery(%q):\n got %q\nwant %q", tc.query, got, tc.want)
		}
	}
}

func TestStripComments(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{"SELECT 1", "SELECT 1"},
		{"SELECT 1 -- c", "SELECT 1  "},
		{"SELECT 1 -- c\n, 2", "SELECT 1  \n, 2"},
		{"SELECT 1; -- c\n", "SELECT 1;  \n"},
		{"INSERT/* c */INTO t", "INSERT INTO t"},
		{"SELECT /* /* nested */ still */ 1", "SELECT   1"},
		{"SELECT '-- not', \"/* not */\", `# not` # c", "SELECT '-- not', \"/* not */\", `# not`  "},
		{"SELECT 1 - -1", "SELECT 1 - -1"},
		{"SELECT 1 /* open", "SELECT 1  "},
		{"INSERT INTO t VALUES ($h$a/*keep*/b$h$)", "INSERT INTO t VALUES ($h$a/*keep*/b$h$)"},
		{"INSERT INTO t VALUES (?) // c", "INSERT INTO t VALUES (?)  "},
	} {
		if got := stripComments(tc.query); got != tc.want {
			t.Errorf("stripComments(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}
