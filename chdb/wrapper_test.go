package chdb

import (
	"testing"
)

func TestQueryToBuffer(t *testing.T) {
	// Create a temporary directory
	sess, err := NewSession()
	if err != nil {
		t.Fatalf("could not create session: %s", err)
	}
	defer sess.Close()

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
		// Session
		{
			name: "Session Query 1",
			queryStr: "CREATE DATABASE IF NOT EXISTS testdb; " +
				"CREATE TABLE IF NOT EXISTS testdb.testtable (id UInt32) ENGINE = MergeTree() ORDER BY id;",
			outputFormat: "CSV",

			udfPath:        "",
			expectedErrMsg: "",
			expectedResult: "",
		},
		{
			name:         "Session Query 2",
			queryStr:     "USE testdb; INSERT INTO testtable VALUES (1), (2), (3);",
			outputFormat: "CSV",

			udfPath:        "",
			expectedErrMsg: "",
			expectedResult: "",
		},
		{
			name:         "Session Query 3",
			queryStr:     "SELECT * FROM testtable;",
			outputFormat: "CSV",

			udfPath:        "",
			expectedErrMsg: "",
			expectedResult: "1\n2\n3\n",
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

			result, err := sess.Query(tc.queryStr, tc.outputFormat)

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
