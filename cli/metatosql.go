package cli

import (
	"errors"
	"strings"
)

// ErrInvalidMetaCommand error.
var ErrInvalidMetaCommand = errors.New("incorrect meta-command")

// ErrArgumentNotProvided error.
var ErrArgumentNotProvided = errors.New("argument to meta-command doesnt provided")

// MetaToSQL convert meta-command to SQL expression
func (c *CLI) MetaToSQL(metaCommand string) (string, error) {
	var expression string

	metaCommand = strings.TrimPrefix(metaCommand, "\\")

	metaCommandArr := strings.Split(metaCommand, " ")

	switch metaCommandArr[0] {
	case "dt":
		if len(metaCommandArr) >= 2 {
			expression = "SHOW TABLES FROM " + metaCommandArr[1] + ";"
		} else {
			return "", ErrArgumentNotProvided
		}
	case "l":
		expression = "SHOW DATABASES;"
	default:
		return "", ErrInvalidMetaCommand
	}

	return expression, nil
}
