// Copyright 2026 Columnar Technologies Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// driverListSource describes where a driver list was loaded from so commands
// can write back to the correct location with the correct format.
type driverListSource struct {
	// Path is the absolute path to the file (dbc.toml or pyproject.toml).
	Path string
	// IsPyproject is true when the driver list lives in [tool.dbc] inside a
	// pyproject.toml.
	IsPyproject bool
}

// lockfilePath returns the path to the lockfile associated with this source.
// The lockfile is always "dbc.lock" in the same directory as the source file,
// regardless of whether the source is dbc.toml or pyproject.toml. This ensures
// users migrating between config formats don't get a different lockfile, and
// the lockfile clearly belongs to the dbc tool.
func (s driverListSource) lockfilePath() string {
	return filepath.Join(filepath.Dir(s.Path), "dbc.lock")
}

// discoverDriverList walks up the directory tree from dir, looking for a
// driver list source. At each directory it checks (in order):
//
//  1. pyproject.toml with a [tool.dbc] section
//  2. dbc.toml
//
// The first match wins. If no match is found all the way up to the filesystem
// root, it returns a source pointing at dbc.toml in the original directory
// (which may or may not exist — callers handle the not-found case).
func discoverDriverList(dir string) (driverListSource, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return driverListSource{}, fmt.Errorf("invalid path: %w", err)
	}

	current := absDir
	for {
		pyprojectPath := filepath.Join(current, "pyproject.toml")
		_, err := checkPyprojectDBCSection(pyprojectPath)
		if err == nil {
			return driverListSource{Path: pyprojectPath, IsPyproject: true}, nil
		}
		// If the file exists but is malformed TOML, report the error rather
		// than silently falling through to dbc.toml.
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errPyprojectNoDBC) {
			return driverListSource{}, err
		}

		dbcPath := filepath.Join(current, "dbc.toml")
		if _, err := os.Stat(dbcPath); err == nil {
			return driverListSource{Path: dbcPath}, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root without finding anything.
			// Fall back to dbc.toml in the original directory.
			break
		}
		current = parent
	}

	return driverListSource{Path: filepath.Join(absDir, "dbc.toml")}, nil
}

// hasPyprojectDBCSection reports whether the given file exists and contains a
// [tool.dbc] table. Returns false if the file doesn't exist. Returns an error
// if the file exists but cannot be parsed as TOML — callers can decide whether
// to treat this as fatal or as a skip.
func hasPyprojectDBCSection(path string) bool {
	_, err := checkPyprojectDBCSection(path)
	return err == nil
}

// checkPyprojectDBCSection checks whether a pyproject.toml at path contains a
// [tool.dbc] section. Returns:
//   - nil if the section is present
//   - errPyprojectNoDBC if the file exists but has no [tool.dbc]
//   - os.ErrNotExist (wrapped) if the file doesn't exist
//   - a parse error if the file is malformed TOML
var errPyprojectNoDBC = errors.New("no [tool.dbc] section")

func checkPyprojectDBCSection(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", path, err)
	}
	tool, ok := doc["tool"].(map[string]any)
	if !ok {
		return nil, errPyprojectNoDBC
	}
	if _, ok = tool["dbc"]; !ok {
		return nil, errPyprojectNoDBC
	}
	return doc, nil
}

// openAndDecodePyprojectDriverList reads the [tool.dbc] section from a
// pyproject.toml and decodes it into a DriversList.
func openAndDecodePyprojectDriverList(path string) (DriversList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DriversList{}, fmt.Errorf("error opening driver list: %s doesn't exist", path)
		}
		return DriversList{}, fmt.Errorf("error opening driver list at %s: %w", path, err)
	}

	list, err := extractDBCSection(data)
	if err != nil {
		return DriversList{}, fmt.Errorf("error decoding [tool.dbc] in %s: %w", path, err)
	}
	return list, nil
}

// extractDBCSection pulls out the [tool.dbc] table from pyproject.toml bytes
// and decodes it into a DriversList.
func extractDBCSection(data []byte) (DriversList, error) {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return DriversList{}, err
	}

	tool, ok := doc["tool"].(map[string]any)
	if !ok {
		return DriversList{}, fmt.Errorf("[tool] section not found")
	}

	dbcSection, ok := tool["dbc"]
	if !ok {
		return DriversList{}, fmt.Errorf("[tool.dbc] section not found")
	}

	// Re-marshal the dbc sub-table and unmarshal into DriversList
	dbcBytes, err := toml.Marshal(dbcSection)
	if err != nil {
		return DriversList{}, fmt.Errorf("error re-encoding [tool.dbc] section: %w", err)
	}

	var list DriversList
	if err := toml.Unmarshal(dbcBytes, &list); err != nil {
		return DriversList{}, err
	}
	return list, nil
}

// writePyprojectDriverList writes a DriversList back into the [tool.dbc]
// section of the given pyproject.toml file, preserving all other content
// (comments, formatting, ordering) outside the [tool.dbc] section.
//
// It works by finding the byte boundaries of the existing [tool.dbc] section
// and splicing in freshly-encoded content for just that section, leaving the
// rest of the file untouched.
func writePyprojectDriverList(path string, list DriversList) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("error reading %s: %w", path, err)
	}

	// Generate the new [tool.dbc] section content. We encode the DriversList
	// as a standalone TOML document, then prefix all table headers with
	// "tool.dbc." to produce valid pyproject.toml syntax.
	newSection, err := marshalDBCSection(list)
	if err != nil {
		return fmt.Errorf("error encoding driver list: %w", err)
	}

	// Find the byte range of the existing [tool.dbc] section and splice in
	// the new content.
	result, err := spliceDBCSection(data, newSection)
	if err != nil {
		return fmt.Errorf("error splicing [tool.dbc] section: %w", err)
	}

	return os.WriteFile(path, result, 0666)
}

// marshalDBCSection encodes a DriversList into TOML text suitable for
// embedding under [tool.dbc] in a pyproject.toml. Table headers are prefixed
// with "tool.dbc." (e.g. "[drivers]" becomes "[tool.dbc.drivers]") and array
// table headers similarly (e.g. "[[registries]]" becomes
// "[[tool.dbc.registries]]").
func marshalDBCSection(list DriversList) ([]byte, error) {
	raw, err := toml.Marshal(list)
	if err != nil {
		return nil, err
	}

	// Prefix table/array-table headers with tool.dbc
	var buf bytes.Buffer
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]") {
			// Array table: [[foo]] -> [[tool.dbc.foo]]
			inner := trimmed[2 : len(trimmed)-2]
			buf.WriteString("[[tool.dbc." + inner + "]]\n")
		} else if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			// Table: [foo] -> [tool.dbc.foo]
			inner := trimmed[1 : len(trimmed)-1]
			buf.WriteString("[tool.dbc." + inner + "]\n")
		} else {
			buf.WriteString(line + "\n")
		}
	}

	// Trim any trailing extra newline from the split
	result := buf.Bytes()
	if len(result) > 0 && result[len(result)-1] == '\n' {
		// Keep exactly one trailing newline
		result = bytes.TrimRight(result, "\n")
		result = append(result, '\n')
	}
	return result, nil
}

// spliceDBCSection finds the [tool.dbc] section in a pyproject.toml byte
// slice and replaces it with newContent. It preserves all content before and
// after the section, including comments.
//
// Section boundaries are determined by finding:
//  1. The start: a line matching [tool.dbc...] (the first such header)
//  2. The end: the next top-level table header that is NOT a child of [tool.dbc]
//     (i.e., not [tool.dbc.*] or [[tool.dbc.*]])
//
// Comments and blank lines immediately preceding the next non-dbc table header
// are attributed to that header (not consumed as part of [tool.dbc]).
func spliceDBCSection(data []byte, newContent []byte) ([]byte, error) {
	lines := strings.Split(string(data), "\n")

	startLine := -1 // first line of [tool.dbc] section (the header itself)
	endLine := -1   // first line AFTER the section (next non-dbc header or its preamble)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isDBCSectionHeader(trimmed) {
			if startLine == -1 {
				startLine = i
			}
		} else if startLine != -1 && endLine == -1 && isTableHeader(trimmed) && !isDBCSectionHeader(trimmed) {
			// Walk backwards from this header to find comments/blanks that
			// belong to it (its "preamble"). These should NOT be consumed as
			// part of the dbc section.
			preambleStart := i
			for preambleStart > startLine {
				prev := strings.TrimSpace(lines[preambleStart-1])
				if prev == "" || strings.HasPrefix(prev, "#") {
					preambleStart--
				} else {
					break
				}
			}
			endLine = preambleStart
		}
	}

	if startLine == -1 {
		return nil, fmt.Errorf("[tool.dbc] section not found")
	}

	// If no subsequent header was found, the section extends to EOF.
	if endLine == -1 {
		endLine = len(lines)
		// But trim any trailing empty lines from the end so we don't accumulate them
		for endLine > startLine && strings.TrimSpace(lines[endLine-1]) == "" {
			endLine--
		}
	}

	// Build the result: before + new content + after
	var buf bytes.Buffer
	// Lines before the section
	for i := 0; i < startLine; i++ {
		buf.WriteString(lines[i] + "\n")
	}

	// New section content (already has trailing newline)
	buf.Write(newContent)

	// Lines after the section
	if endLine < len(lines) {
		for i := endLine; i < len(lines); i++ {
			if i < len(lines)-1 {
				buf.WriteString(lines[i] + "\n")
			} else {
				// Last line: only add newline if original had one
				buf.WriteString(lines[i])
				if len(data) > 0 && data[len(data)-1] == '\n' {
					buf.WriteByte('\n')
				}
			}
		}
	}

	return buf.Bytes(), nil
}

// isDBCSectionHeader reports whether a trimmed line is a [tool.dbc] or
// [[tool.dbc]] family header (including sub-tables like [tool.dbc.drivers]).
func isDBCSectionHeader(trimmed string) bool {
	if strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]") {
		inner := strings.TrimSpace(trimmed[2 : len(trimmed)-2])
		return inner == "tool.dbc" || strings.HasPrefix(inner, "tool.dbc.")
	}
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
		return inner == "tool.dbc" || strings.HasPrefix(inner, "tool.dbc.")
	}
	return false
}

// isTableHeader reports whether a trimmed line is a TOML table header
// (either [table] or [[array-table]]).
func isTableHeader(trimmed string) bool {
	return (strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))
}

// defaultDriverListPath is the default value for the -p flag across commands.
const defaultDriverListPath = "./dbc.toml"

// resolveDriverListSource determines the driver list source from the user's -p
// flag. Auto-discovery (walking up the tree, preferring pyproject.toml) only
// activates when the default path is used AND the default file does not exist.
// If the user explicitly provides a path to an existing file, that file is used
// directly — even if it happens to be "./dbc.toml".
func resolveDriverListSource(flagPath string) (driverListSource, error) {
	abs, err := filepath.Abs(flagPath)
	if err != nil {
		return driverListSource{}, fmt.Errorf("invalid path: %w", err)
	}

	// If a directory was given (no extension), discover in that directory.
	if filepath.Ext(abs) == "" {
		return discoverDriverList(flagPath)
	}

	// If the path points to an existing file, use it directly (explicit path).
	if _, err := os.Stat(abs); err == nil {
		isPyproject := filepath.Base(abs) == "pyproject.toml"
		return driverListSource{Path: abs, IsPyproject: isPyproject}, nil
	}

	// The file doesn't exist. If this is the default path, attempt
	// auto-discovery (walk up looking for pyproject.toml or dbc.toml).
	if flagPath == defaultDriverListPath {
		return discoverDriverList(".")
	}

	// Explicit path to a non-existent file — return it as-is so the caller
	// produces the appropriate "file not found" error.
	isPyproject := filepath.Base(abs) == "pyproject.toml"
	return driverListSource{Path: abs, IsPyproject: isPyproject}, nil
}

// openAndDecodeFromSource loads a DriversList from the given source,
// dispatching to the appropriate reader based on whether it's a pyproject.toml
// or a standalone dbc.toml.
func openAndDecodeFromSource(src driverListSource) (DriversList, error) {
	if src.IsPyproject {
		return openAndDecodePyprojectDriverList(src.Path)
	}
	return openAndDecodeDriverList(src.Path)
}

// writeDriverListToSource writes a DriversList back to the given source.
func writeDriverListToSource(src driverListSource, list DriversList) error {
	if src.IsPyproject {
		return writePyprojectDriverList(src.Path, list)
	}
	f, err := os.Create(src.Path)
	if err != nil {
		return fmt.Errorf("error creating file %s: %w", src.Path, err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(list)
}
