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
			name:         "Single Queries",
			argv:         []string{"clickhouse", "--multiquery", "--output-format=CSV", "--query=SELECT 'abc';"},
			expectError:  false,
			expectOutput: "\"abc\"\n",
		},
		{
			name:         "Error Query",
			argv:         []string{"clickhouse", "--multiquery", "--output-format=CSV", "--query=XXX;"},
			expectError:  true,
			expectOutput: "",
		},
	}

	// Iterate over the test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := QueryStable(len(tc.argv), tc.argv)

			// Assert based on the expected outcome of the test case
			if tc.expectError {
				if err == nil {
					t.Errorf("Expected error, but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, but got %v", err)
				} else {
					if result == nil {
						t.Errorf("Expected non-nil result, but got nil")
					} else {
						if result.cResult == nil {
							t.Errorf("Expected non-nil cResult, but got nil")
						} else {
							if result.cResult.error_message != nil {
								t.Errorf("Expected nil error_message, but got %v", result.cResult.error_message)
							} else {
								if result.cResult.buf == nil {
									t.Errorf("Expected non-nil output, but got nil")
								} else {
									if tc.expectOutput != string(result.String()) {
										t.Errorf("Expected output %v, but got %v", tc.expectOutput, string(result.String()))
									}
								}
							}
						}
					}
				}
			}
		})
	}
}
