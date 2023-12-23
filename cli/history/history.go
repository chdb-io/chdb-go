package history

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ClickHouseDateFormat - representation of date in Clickhouse,
// like RFC3339 but without timezone and with milliseconds
const ClickHouseDateFormat = "2006-01-02 15:04:05.000"

// History object
type History struct {
	file *os.File
}

// Row of history
type Row struct {
	CreatedAt time.Time
	Query     string
}

// New history object from file
func New(path string) (*History, error) {
	var file *os.File
	var err error

	file, err = os.OpenFile(path, os.O_RDWR, os.ModePerm)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			file, err = os.Create(path)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	return &History{file: file}, err
}

// Close history file
func (h *History) Close() error {
	return h.file.Close()
}

// Read history from file
func (h *History) Read() ([]*Row, error) {
	var rows []*Row

	scanner := bufio.NewScanner(h.file)

	for scanner.Scan() {
		text := scanner.Text()

		if strings.Contains(text, "###") {
			var row Row

			dateStr := strings.TrimPrefix(text, "### ")
			date, err := time.Parse("2006-01-02 15:04:05", dateStr)
			if err != nil {
				return nil, err
			}

			row.CreatedAt = date

			scanner.Scan()
			row.Query = scanner.Text()

			rows = append(rows, &row)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return rows, nil
}

// Write row creation date
func (h *History) Write(row *Row) error {
	_, err := h.file.WriteString(fmt.Sprintf("### %s\n%s\n", row.CreatedAt.Format(ClickHouseDateFormat), row.Query))

	return err
}

// RowsToStrArr - convert rows to slice of strings
func (h *History) RowsToStrArr(rows []*Row) []string {
	historyArr := make([]string, 0, len(rows))

	for _, row := range rows {
		historyArr = append(historyArr, row.Query)
	}

	return historyArr
}
