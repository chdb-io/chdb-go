package chdbdriver

import (
	"fmt"
	"testing"
)

func TestPlaceholderOffsets(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []int
	}{
		{"", nil},
		{"?", []int{0}},
		{"SELECT ?, ?", []int{7, 10}},
		{"SELECT '?'", nil},
		{"SELECT 'it''s ?', ?", []int{18}},
		{`SELECT 'a\'?', ?`, []int{15}},
		{`SELECT "?", ` + "`?`, ?", []int{17}},
		{"SELECT 1 -- ?\n, ?", []int{16}},
		{"SELECT 1 - ?", []int{11}},
		{"SELECT ? #?", []int{7, 10}},
		{"SELECT ? # ?\n, ?", []int{7, 15}},
		{"SELECT /* /* ? */ ? */ ?", []int{23}},
		{"SELECT 1 / ?", []int{11}},
		{"SELECT 'é?', ?", []int{14}},
		{"SELECT '?", nil},
		{"SELECT /* ?", nil},
		{"SELECT -- ?", nil},
	} {
		if got := placeholders(tc.query); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("placeholders(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}
