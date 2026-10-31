package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTableAlignsColumns(t *testing.T) {
	var buf bytes.Buffer

	err := Table(&buf,
		[]string{"MODULE", "SOURCE", "LOCKED"},
		[][]string{
			{"initializer", "hashicorp/azurerm", "4.54.0"},
			{"network/expressroute", "aztfmod/azurecaf", "1.2.31"},
		})
	if err != nil {
		t.Fatalf("Table: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}

	// Every column must begin at the same offset on every line.
	wantOffset := strings.Index(lines[0], "SOURCE")
	if wantOffset < 0 {
		t.Fatalf("header not rendered: %q", lines[0])
	}
	if got := strings.Index(lines[1], "hashicorp/azurerm"); got != wantOffset {
		t.Errorf("row 1 second column at %d, want %d\n%s", got, wantOffset, buf.String())
	}
	if got := strings.Index(lines[2], "aztfmod/azurecaf"); got != wantOffset {
		t.Errorf("row 2 second column at %d, want %d\n%s", got, wantOffset, buf.String())
	}
}

func TestTableSeparatesColumnsWithPadding(t *testing.T) {
	var buf bytes.Buffer

	if err := Table(&buf, []string{"A", "B"}, [][]string{{"x", "y"}}); err != nil {
		t.Fatalf("Table: %v", err)
	}

	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if !strings.Contains(line, "  ") {
			t.Errorf("columns are not separated by padding: %q", line)
		}
	}
}

func TestTableRendersEmptyCellsAsDash(t *testing.T) {
	var buf bytes.Buffer

	// A blank cell must be visibly distinct, not mistaken for a missing column.
	if err := Table(&buf, []string{"A", "B"}, [][]string{{"value", ""}, {"", "  "}}); err != nil {
		t.Fatalf("Table: %v", err)
	}

	out := buf.String()
	if strings.Count(out, "-") != 3 {
		t.Errorf("expected 3 dashes for blank cells, got:\n%s", out)
	}
}

func TestTableHandlesNoRows(t *testing.T) {
	var buf bytes.Buffer

	if err := Table(&buf, []string{"A", "B"}, nil); err != nil {
		t.Fatalf("Table: %v", err)
	}

	if got := strings.TrimSpace(buf.String()); got != "A  B" {
		t.Errorf("output = %q, want header only", got)
	}
}

func TestJSONIsIndentedAndParsable(t *testing.T) {
	var buf bytes.Buffer

	payload := map[string]any{"providers": []string{"azurerm"}, "count": 1}
	if err := JSON(&buf, payload); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	if !strings.Contains(buf.String(), "\n  ") {
		t.Errorf("output is not indented:\n%s", buf.String())
	}

	var round map[string]any
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if round["count"] != float64(1) {
		t.Errorf("count = %v, want 1", round["count"])
	}
}

func TestSectionUnderlinesTitle(t *testing.T) {
	var buf bytes.Buffer
	Section(&buf, "Planned upgrades")

	out := buf.String()
	if !strings.Contains(out, "Planned upgrades\n----------------\n") {
		t.Errorf("unexpected section rendering:\n%q", out)
	}
}
