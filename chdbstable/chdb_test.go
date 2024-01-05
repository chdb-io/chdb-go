package chdbstable

import (
	"testing"
)

// TestCase defines the structure of a test case
type TestCase struct {
	name         string   // Name of the test case
	argv         []string // Arguments to pass to QueryStable
	expectError  bool     // Whether an error is expected
	expectOutput string   // Expected output
}

func TestQueryStableMultipleCases(t *testing.T) {
	// Define a series of test cases
	testCases := []TestCase{
		{
			name:         "Single Query",
			argv:         []string{"clickhouse", "--multiquery", "--output-format=CSV", "--query=SELECT 123;"},
			expectError:  false,
			expectOutput: "123\n",
		},
		{
			name:         "Multiple Queries",
			argv:         []string{"clickhouse", "--multiquery", "--output-format=CSV", "--query=SELECT 'abc';"},
			expectError:  false,
			expectOutput: "abc",
		},
	}

	// Iterate over the test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := QueryStable(len(tc.argv), tc.argv)

			// Assert based on the expected outcome of the test case
			if (result == nil) != tc.expectError {
				t.Errorf("QueryStable() with args %v, expect error: %v, got result: %v", tc.argv, tc.expectError, result)
			}

			if (result != nil) && (string(result) != tc.expectOutput) {
				t.Errorf("QueryStable() with args %v, expect output: %v, got output: %v", tc.argv, tc.expectOutput, string(result.Buf()))
			}
		})
	}
}
