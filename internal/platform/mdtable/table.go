// Package mdtable detects GFM-style markdown tables and reformats them as
// vertical plain text suitable for narrow displays.
package mdtable

import "strings"

const rowSep = "───────────"

// ReplaceAll scans text for GFM-style tables and replaces each one in-place
// with the formatted vertical representation produced by Format.
func ReplaceAll(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	i := 0
	for i < len(lines) {
		if i+1 < len(lines) && hasTableCells(lines[i]) && isSeparator(lines[i+1]) {
			end := i + 2
			for end < len(lines) && hasTableCells(lines[end]) {
				end++
			}
			out = append(out, strings.Split(Format(lines[i:end]), "\n")...)
			i = end
		} else {
			out = append(out, lines[i])
			i++
		}
	}
	return strings.Join(out, "\n")
}

// Format converts GFM table lines (header row, separator, data rows) to a
// vertical plain-text representation:
//   - 2-column: "val1 — val2" per row
//   - 3+ columns: "**Header**: value" blocks separated by ───────────
func Format(lines []string) string {
	if len(lines) < 2 {
		return strings.Join(lines, "\n")
	}
	headers := parseRow(lines[0])
	ncols := len(headers)
	if ncols == 0 {
		return strings.Join(lines, "\n")
	}

	dataRows := make([][]string, 0, len(lines)-2)
	for _, line := range lines[2:] {
		row := parseRow(line)
		for len(row) < ncols {
			row = append(row, "")
		}
		dataRows = append(dataRows, row[:ncols])
	}

	if len(dataRows) == 0 {
		return strings.Join(headers, " — ")
	}

	if ncols <= 2 {
		rows := make([]string, len(dataRows))
		for i, row := range dataRows {
			if ncols == 1 {
				rows[i] = row[0]
			} else {
				rows[i] = row[0] + " — " + row[1]
			}
		}
		return strings.Join(rows, "\n")
	}

	blocks := make([]string, len(dataRows))
	for i, row := range dataRows {
		fieldLines := make([]string, ncols)
		for j, h := range headers {
			val := ""
			if j < len(row) {
				val = row[j]
			}
			fieldLines[j] = "**" + h + "**: " + val
		}
		blocks[i] = strings.Join(fieldLines, "\n")
	}
	return strings.Join(blocks, "\n"+rowSep+"\n")
}

func hasTableCells(line string) bool {
	return strings.Contains(line, "|")
}

func isSeparator(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.Contains(t, "-") {
		return false
	}
	for _, r := range t {
		if r != '|' && r != '-' && r != ':' && r != ' ' {
			return false
		}
	}
	return true
}

func parseRow(line string) []string {
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	for len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	for len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}
