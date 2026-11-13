//go:build cgo && (darwin || linux)

package chdbarrow

import "testing"

func TestNativeParamsRejectsInvalidAndDuplicateNames(t *testing.T) {
	for _, params := range [][]Parameter{
		{{Name: "", Value: "x"}},
		{{Name: "a-b", Value: "x"}},
		{{Name: "1a", Value: "x"}},
		{{Name: "x", Value: "one"}, {Name: "x", Value: "two"}},
	} {
		if _, err := nativeParams(params); err == nil {
			t.Fatalf("accepted invalid parameters: %#v", params)
		}
	}
	bindings, err := nativeParams([]Parameter{{Name: "_x2", Value: "quoted'\x00value"}})
	if err != nil || len(bindings) != 1 || bindings[0].Value != "quoted'\x00value" {
		t.Fatalf("parameter value changed: %#v, %v", bindings, err)
	}
}
