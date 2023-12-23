package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chdb-io/chdb-go/cli/history"
)

var exitCodes = [...]string{
	"exit",
	"quit",
	"logout",
	"учше",
	"йгше",
	"дщпщге",
	"exit;",
	"quit;",
	"logout;",
	"учшеж",
	"йгшеж",
	"дщпщгеж",
	"q",
	"й",
	"Q",
	":q",
	"Й",
	"Жй",
}

// Executor - exec query and write it to history + checking for one of quit commands.
func (c *CLI) Executor(s string) {
	if !c.isMultilineInputStarted {
		for _, code := range exitCodes {
			if s == code {
				fmt.Println("Bye.")
				os.Exit(0)
				return
			}
		}

		if strings.Contains(s, "\\") {
			mToSQL, err := c.MetaToSQL(s)
			if err != nil {
				fmt.Println(err)
				return
			}

			s = mToSQL
		}
	}

	if c.Multiline {
		if strings.TrimSpace(s) != "" {
			if strings.HasSuffix(s, ";") {
				c.query += s

				c.isMultilineInputStarted = false
			} else {
				c.query += s + " "

				c.isMultilineInputStarted = true
			}
		}
	} else {
		c.query = s
	}

	if !c.isMultilineInputStarted {
		if err := c.history.Write(&history.Row{
			CreatedAt: time.Now(),
			Query:     c.query,
		}); err != nil {
			fmt.Println(err)
		}

		trimedQuery := strings.TrimSpace(c.query)
		if len(trimedQuery) == 0 {
			fmt.Println("")
		} else {
			data := c.Session.Query(trimedQuery, "Pretty")
			fmt.Println(data)
		}

		c.query = ""
	}
}
