package mdtable

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReplaceAll_NoTable(t *testing.T) {
	input := "plain text\nno table here"
	assert.Equal(t, input, ReplaceAll(input))
}

func TestReplaceAll_TableReplaced(t *testing.T) {
	input := "before\n| A | B |\n|---|---|\n| 1 | 2 |\nafter"
	out := ReplaceAll(input)
	assert.Equal(t, "before\n1 — 2\nafter", out)
}

func TestReplaceAll_TableAtStart(t *testing.T) {
	input := "| A | B |\n|---|---|\n| 1 | 2 |\nafter"
	out := ReplaceAll(input)
	assert.Equal(t, "1 — 2\nafter", out)
}

func TestReplaceAll_TableAtEnd(t *testing.T) {
	input := "before\n| A | B |\n|---|---|\n| 1 | 2 |"
	out := ReplaceAll(input)
	assert.Equal(t, "before\n1 — 2", out)
}

func TestReplaceAll_MultipleTables(t *testing.T) {
	input := "| A | B |\n|---|---|\n| 1 | 2 |\n\n| X | Y |\n|---|---|\n| a | b |"
	out := ReplaceAll(input)
	assert.Equal(t, "1 — 2\n\na — b", out)
}

func TestFormat_TwoColumn(t *testing.T) {
	lines := []string{
		"| Command | Description |",
		"|---------|-------------|",
		"| /new    | New session |",
		"| /list   | List all    |",
	}
	assert.Equal(t, "/new — New session\n/list — List all", Format(lines))
}

func TestFormat_ThreeColumn(t *testing.T) {
	lines := []string{
		"| Name | IP | Port |",
		"|------|----|------|",
		"| host | 1.2.3.4 | 22 |",
	}
	result := Format(lines)
	assert.Contains(t, result, "**Name**: host")
	assert.Contains(t, result, "**IP**: 1.2.3.4")
	assert.Contains(t, result, "**Port**: 22")
}

func TestFormat_ThreeColumnSeparator(t *testing.T) {
	lines := []string{
		"| Name | IP | Port |",
		"|------|----|------|",
		"| host | 1.2.3.4 | 22 |",
		"| srv  | 5.6.7.8 | 80 |",
	}
	result := Format(lines)
	assert.Contains(t, result, rowSep)
}

func TestFormat_HeaderOnlyTable(t *testing.T) {
	lines := []string{
		"| A | B |",
		"|---|---|",
	}
	result := Format(lines)
	assert.Contains(t, result, "A")
	assert.Contains(t, result, "B")
	assert.NotContains(t, result, "|---|")
}
