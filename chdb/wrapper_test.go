package chdb

import (
	"io/ioutil"
	"os"
	"testing"
)

func TestQueryToBuffer(t *testing.T) {
	// Create a temporary directory
	tempDir, err := ioutil.TempDir("", "example")
	if err != nil {
		t.Fatalf("Failed to create temporary directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Define test cases
	testCases := []struct {
		name           string
		queryStr       string
		outputFormat   string
		path           string
		udfPath        string
		expectedResult string
	}{
		{
			name:           "Basic Query",
			queryStr:       "SELECT 123",
			outputFormat:   "CSV",
			path:           "",
			udfPath:        "",
			expectedResult: "123\n",
		},
		// Session
		{
			name:         "Session Query 1",
			queryStr:     "CREATE DATABASE IF NOT EXISTS testdb; "+
					"CREATE TABLE IF NOT EXISTS testdb.testtable (id UInt32) ENGINE = MergeTree() ORDER BY id;",
			outputFormat: "CSV",
			path:         tempDir,
			udfPath:      "",
			expectedResult: "",
		},
		{
			name:         "Session Query 2",
			queryStr:     "USE testdb; INSERT INTO testtable VALUES (1), (2), (3);",
			outputFormat: "CSV",
			path:         tempDir,
			udfPath:      "",
			expectedResult: "",
		},
		{
			name:         "Session Query 3",
			queryStr:     "SELECT * FROM testtable;",
			outputFormat: "CSV",
			path:         tempDir,
			udfPath:      "",
			expectedResult: "1\n2\n3\n",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call queryToBuffer
			result := queryToBuffer(tc.queryStr, tc.outputFormat, tc.path, tc.udfPath)

			// Verify
			if string(result.Buf()) != tc.expectedResult {
				t.Errorf("%v queryToBuffer() with queryStr %v, outputFormat %v, path %v, udfPath %v, expect result: %v, got result: %v", 
					tc.name, tc.queryStr, tc.outputFormat, tc.path, tc.udfPath, tc.expectedResult, string(result.Buf()))
			}
		})
	}
}
