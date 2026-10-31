// Package report renders command output as either an aligned table or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Table writes an aligned table. Empty cells are rendered as "-" so a blank
// column is never mistaken for a missing one.
func Table(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			if strings.TrimSpace(c) == "" {
				c = "-"
			}
			cells[i] = c
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// JSON writes v as indented JSON, the form intended for CI consumption.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Section prints a titled heading, used to separate per-module output.
func Section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s\n%s\n", title, strings.Repeat("-", len(title)))
}
