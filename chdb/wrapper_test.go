package chdb

import (
	"fmt"
	"testing"
)

func TestQueryToBuffer(t *testing.T) {
	// Create a temporary directory

	// Define test cases
	testCases := []struct {
		name         string
		queryStr     string
		outputFormat string

		udfPath        string
		expectedErrMsg string
		expectedResult string
	}{
		{
			name:         "Basic Query",
			queryStr:     "SELECT 123",
			outputFormat: "CSV",

			udfPath:        "",
			expectedErrMsg: "",
			expectedResult: "123\n",
		},

		{
			name:         "Error Query",
			queryStr:     "SELECT * FROM nonexist; ",
			outputFormat: "CSV",

			udfPath:        "",
			expectedErrMsg: "Code: 60. DB::Exception: Unknown table expression identifier 'nonexist' in scope SELECT * FROM nonexist. (UNKNOWN_TABLE)",
			expectedResult: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call queryToBuffer

			result, err := Query(tc.queryStr, tc.outputFormat)
			fmt.Println("result: ", result)

			// Verify
			if tc.expectedErrMsg != "" {
				if err == nil {
					t.Errorf("%v queryToBuffer() with queryStr %v, outputFormat %v, udfPath %v, expect error message: %v, got no error",
						tc.name, tc.queryStr, tc.outputFormat, tc.udfPath, tc.expectedErrMsg)
				} else {
					if err.Error() != tc.expectedErrMsg {
						t.Errorf("%v queryToBuffer() with queryStr %v, outputFormat %v, udfPath %v, expect error message: %v, got error message: %v",
							tc.name, tc.queryStr, tc.outputFormat, tc.udfPath, tc.expectedErrMsg, err.Error())
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
