package chdb

import (
	"fmt"
	"strings"
	"testing"
)

func TestQueryToBuffer(t *testing.T) {
	// Create a temporary directory

	// Define test cases
	testCases := []struct {
		name         string
		queryStr     string
		outputFormat string

		udfPath          string
		expectedErrParts []string
		expectedResult   string
	}{
		{
			name:         "Basic Query",
			queryStr:     "SELECT 123",
			outputFormat: "CSV",

			udfPath:        "",
			expectedResult: "123\n",
		},

		{
			name:         "Error Query",
			queryStr:     "SELECT * FROM nonexist; ",
			outputFormat: "CSV",

			udfPath:          "",
			expectedErrParts: []string{"Code: 60", "'nonexist'", "UNKNOWN_TABLE"},
			expectedResult:   "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call queryToBuffer

			result, err := Query(tc.queryStr, tc.outputFormat)
			fmt.Println("result: ", result)

			// Verify
			if len(tc.expectedErrParts) != 0 {
				if err == nil {
					t.Errorf("%v queryToBuffer() with queryStr %v, outputFormat %v, udfPath %v, expected error containing %v, got no error",
						tc.name, tc.queryStr, tc.outputFormat, tc.udfPath, tc.expectedErrParts)
				} else {
					for _, part := range tc.expectedErrParts {
						if !strings.Contains(err.Error(), part) {
							t.Errorf("%v queryToBuffer() with queryStr %v, outputFormat %v, udfPath %v, expected error containing %q, got error message: %v",
								tc.name, tc.queryStr, tc.outputFormat, tc.udfPath, part, err)
						}
					}
				}
			} else {
				if string(result.Buf()) != tc.expectedResult {
					t.Errorf("%v queryToBuffer() with queryStr %v, outputFormat %v,  udfPath %v, expect result: %v, got result: %v",
						tc.name, tc.queryStr, tc.outputFormat, tc.udfPath, tc.expectedResult, string(result.Buf()))
				}
			}
		})
	}
}
