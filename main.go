package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/c-bata/go-prompt"

	// "github.com/chdb-io/chdb-go/cli"
	// "github.com/chdb-io/chdb-go/cli/completer"
	// "github.com/chdb-io/chdb-go/cli/history"

	"github.com/chdb-io/chdb-go/chdb"
)

func main() {
	// Define command line flags
	pathFlag := flag.String("path", "",
		`Specify a custom path for the session, default is a temporary directory and 
data will lost after exit. If you want to keep the data, specify a path to a directory.`)

	helpFlag := flag.Bool("help", false,
		`Show this help message and exit.
	Usage: chdb-go [options] [sql [output format]]
	Example:
		./chdb-go 'SELECT 123' 		 # default output CSV
		./chdb-go 'SELECT 123' JSON
		./chdb-go  					 # enter interactive mode, data will lost after exit
		./chdb-go --path sess_path	 # enter persistent interactive mode
`)

	flag.Parse()

	if *helpFlag {
		flag.Usage()
		return
	}

	// If path is specified or no additional arguments, enter interactive mode
	if len(flag.Args()) == 0 {
		t := "/tmp"
		pathFlag = &t
		var err error
		var session *chdb.Session
		if *pathFlag != "" {
			session, err = chdb.NewSession(*pathFlag)
		} else {
			session, err = chdb.NewSession()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create session: %s\n", err)
			os.Exit(1)
		}
		defer session.Close()

		interactiveMode(session)
	} else {
		// Execute a single query from command line arguments
		args := flag.Args()
		sql := args[0]
		format := "CSV" // Default format
		if len(args) > 1 {
			format = args[1]
		}

		result, err := chdb.Query(sql, format)
		if err != nil {
			fmt.Println(err)
			return
		}

		// Print the result
		fmt.Println(result)
	}
}

func interactiveMode(session *chdb.Session) {
	fmt.Println("Enter your SQL commands; type 'exit' to quit.")

	p := prompt.New(
		func(query string) {
			if query == "exit" {
				os.Exit(0)
			}

			result, err := session.Query(query, "CSV")
			if err != nil {
				fmt.Println(err)
				return
			}

			fmt.Println(result)
		},
		func(d prompt.Document) []prompt.Suggest {
			return []prompt.Suggest{
				{Text: "SELECT", Description: "SELECT"},
				{Text: "INSERT", Description: "INSERT"},
				{Text: "UPDATE", Description: "UPDATE"},
				{Text: "DELETE", Description: "DELETE"},
				{Text: "CREATE", Description: "CREATE"},
				{Text: "ALTER", Description: "ALTER"},
				{Text: "DROP", Description: "DROP"},
				{Text: "DESCRIBE", Description: "DESCRIBE"},
				{Text: "SHOW", Description: "SHOW"},
				{Text: "OPTIMIZE", Description: "OPTIMIZE"},
			}
		},
		prompt.OptionPrefix(":) "),
		prompt.OptionTitle("chdb-go"),
	)
	p.Run()
}
