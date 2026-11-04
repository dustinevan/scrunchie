// Package files reads SQL and writes files only at the user's explicit request.
package files

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dustinevan/scrunchie/pkg/result"
)

const maxSQLBytes = 16 << 20

// Open reads a UTF-8 SQL file with a 16 MiB limit.
func Open(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening SQL: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxSQLBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return "", fmt.Errorf("reading SQL: %w", readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("closing SQL: %w", closeErr)
	}
	if len(data) > maxSQLBytes {
		return "", fmt.Errorf("reading SQL: file exceeds 16 MiB")
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("reading SQL: file is not UTF-8")
	}
	if strings.IndexByte(string(data), 0) >= 0 {
		return "", fmt.Errorf("reading SQL: file contains a NUL byte")
	}
	return string(data), nil
}

// Save atomically replaces a SQL file with owner-only permissions. On a write
// error the original stays intact and the error names the recovery file.
func Save(path, sql string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".scrunch-save-*")
	if err != nil {
		return fmt.Errorf("saving SQL: %w", err)
	}
	_, writeErr := io.WriteString(f, sql)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("saving SQL (recovery file %s): %w", f.Name(), writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing SQL (recovery file %s): %w", f.Name(), closeErr)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replacing SQL (recovery file %s): %w", f.Name(), err)
	}
	return nil
}

// Export atomically writes the retained rows in csv or json format. The UI
// confirms overwrites before calling Export. The destination is owner-only.
func Export(path string, set result.Set, format string) error {
	if format != "csv" && format != "json" {
		return fmt.Errorf("exporting results: unknown format %q", format)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".scrunch-export-*")
	if err != nil {
		return fmt.Errorf("exporting results: %w", err)
	}
	writeErr := Write(f, set, format)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("exporting results (partial file %s): %w", f.Name(), writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing export (recovery file %s): %w", f.Name(), closeErr)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replacing export (recovery file %s): %w", f.Name(), err)
	}
	return nil
}

// Write writes a single result. JSON uses column metadata plus arrays of
// strings/nulls so duplicate column names and decimal precision survive. CSV
// quotes every non-NULL value and writes SQL NULL as an unquoted backslash-N.
func Write(w io.Writer, set result.Set, format string) error {
	switch format {
	case "csv":
		for i, column := range set.Columns {
			if i > 0 {
				if _, err := io.WriteString(w, ","); err != nil {
					return fmt.Errorf("writing CSV separator: %w", err)
				}
			}
			if _, err := io.WriteString(w, `"`+strings.ReplaceAll(column.Name, `"`, `""`)+`"`); err != nil {
				return fmt.Errorf("writing CSV header: %w", err)
			}
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return fmt.Errorf("writing CSV newline: %w", err)
		}
		for _, row := range set.Rows {
			for i, value := range row {
				if i > 0 {
					if _, err := io.WriteString(w, ","); err != nil {
						return fmt.Errorf("writing CSV separator: %w", err)
					}
				}
				field := `\N`
				if !value.Null {
					text := value.Text
					if i < len(set.Columns) && set.Columns[i].OID == 17 {
						text = `\x` + hex.EncodeToString(value.Bytes)
					}
					field = `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
				}
				if _, err := io.WriteString(w, field); err != nil {
					return fmt.Errorf("writing CSV value: %w", err)
				}
			}
			if _, err := io.WriteString(w, "\n"); err != nil {
				return fmt.Errorf("writing CSV newline: %w", err)
			}
		}
		return nil
	case "json":
		output := struct {
			Columns   []result.Column     `json:"columns"`
			Rows      [][]json.RawMessage `json:"rows"`
			Command   string              `json:"command"`
			RowCount  int64               `json:"row_count"`
			Truncated bool                `json:"truncated"`
		}{Columns: set.Columns, Rows: make([][]json.RawMessage, 0, len(set.Rows)), Command: set.Command, RowCount: set.RowCount, Truncated: set.Truncated}
		for _, row := range set.Rows {
			values := make([]json.RawMessage, len(row))
			for i, value := range row {
				if value.Null {
					values[i] = json.RawMessage("null")
					continue
				}
				text := value.Text
				if i < len(set.Columns) && set.Columns[i].OID == 17 {
					text = `\x` + hex.EncodeToString(value.Bytes)
				}
				data, err := json.Marshal(text)
				if err != nil {
					return fmt.Errorf("encoding result value: %w", err)
				}
				values[i] = data
			}
			output.Rows = append(output.Rows, values)
		}
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			return fmt.Errorf("writing JSON: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("writing results: unknown format %q", format)
	}
}
