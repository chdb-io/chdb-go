package chdb

import (
	chdbpurego "github.com/chdb-io/chdb-go/chdb-purego"
)

// Query calls queryToBuffer with a default output format of "CSV" if not provided.
func Query(queryStr string, outputFormats ...string) (result chdbpurego.ChdbResult, err error) {
	outputFormat := "CSV" // Default value
	if len(outputFormats) > 0 {
		outputFormat = outputFormats[0]
	}
	return queryToBuffer(queryStr, outputFormat, "", "")
}

// queryToBuffer constructs the arguments for QueryStable and calls it.
func queryToBuffer(queryStr, outputFormat, path, udfPath string) (result chdbpurego.ChdbResult, err error) {
	argv := []string{"clickhouse", "--multiquery"}

	// Handle output format
	if outputFormat == "Debug" || outputFormat == "debug" {
		argv = append(argv, "--verbose", "--log-level=trace", "--output-format=CSV")
	} else {
		argv = append(argv, "--output-format="+outputFormat)
	}

	// Handle path
	if path != "" {
		argv = append(argv, "--path="+path)
	}

	// Add query string
	argv = append(argv, "--query="+queryStr)

	// Handle user-defined functions path
	if udfPath != "" {
		argv = append(argv, "--", "--user_scripts_path="+udfPath, "--user_defined_executable_functions_config="+udfPath+"/*.xml")
	}

	// Call QueryStable with the constructed arguments
	return chdbpurego.RawQuery(len(argv), argv)
}

func initConnection(connStr string) (result chdbpurego.ChdbConn, err error) {
	argv := []string{connStr}
	// Call NewConnection with the constructed arguments
	return chdbpurego.NewConnection(len(argv), argv)
}

func connQueryToBuffer(conn chdbpurego.ChdbConn, queryStr, outputFormat string) (result chdbpurego.ChdbResult, err error) {
	if outputFormat == "" {
		outputFormat = "CSV"
	}
	return conn.Query(queryStr, outputFormat)
}
