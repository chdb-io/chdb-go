package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/c-bata/go-prompt"

	"github.com/chdb-io/chdb-go/cli"
	"github.com/chdb-io/chdb-go/cli/completer"
	"github.com/chdb-io/chdb-go/cli/history"

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

		result := chdb.Query(sql, format)
		if result == nil {
			fmt.Println("No result or an error occurred.")
			return
		}

		// Print the result
		fmt.Println(result)
	}
}

func interactiveMode(session *chdb.Session) {
	fmt.Println("Enter your SQL commands; type 'exit' to quit.")

	h, uh, err := initHistory("")
	if err != nil {
		fmt.Errorf("Failed to init history: %s", err)
		return
	}

	c := cli.New(session, h, true)
	complete := completer.New()

	p := prompt.New(
		c.Executor,
		complete.Complete,
		prompt.OptionTitle("chDB golang cli."),
		prompt.OptionHistory(h.RowsToStrArr(uh)),
		prompt.OptionPrefix(c.GetCurrentDB(context.Background())+" :) "),
		prompt.OptionLivePrefix(c.GetLivePrefixState),
		prompt.OptionPrefixTextColor(prompt.White),
		prompt.OptionAddKeyBind(prompt.KeyBind{
			Key: prompt.F3,
			Fn:  c.MultilineControl,
		}),
	)

	p.Run()
}

func initHistory(path string) (*history.History, []*history.Row, error) {
	var historyPath string
	if path != "" {
		historyPath = path
	} else {
		home, _ := os.UserHomeDir()
		historyPath = home + "/.chdb-go-cli-history"
	}

	h, err := history.New(historyPath)
	if err != nil {
		return nil, nil, err
	}

	uh, err := h.Read()
	if err != nil {
		return nil, nil, err
	}

	return h, uh, nil
}
