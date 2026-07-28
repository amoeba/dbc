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
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
//  1. dbc.toml
//  2. pyproject.toml with a [tool.dbc] section
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
		dbcPath := filepath.Join(current, "dbc.toml")
		if _, err := os.Stat(dbcPath); err == nil {
			return driverListSource{Path: dbcPath}, nil
		}

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

// defaultDriverListPath is the conventional standalone driver-list path.
const defaultDriverListPath = "./dbc.toml"

// resolveDriverListSource determines the driver list source from the user's -p
// flag. An empty flagPath means the user omitted -p, so auto-discovery walks
// up the tree and prefers dbc.toml over pyproject.toml with [tool.dbc]. If the
// user explicitly provides a path to an existing file, that file is used
// directly — even if it happens to be "./dbc.toml".
func resolveDriverListSource(flagPath string) (driverListSource, error) {
	if flagPath == "" {
		return discoverDriverList(".")
	}

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

// rejectPyprojectMutation prevents dbc from editing pyproject.toml. Users may
// manually embed [tool.dbc] there, and read-only/sync flows can consume it, but
// dbc only writes standalone driver-list files.
func rejectPyprojectMutation(src driverListSource) error {
	if !src.IsPyproject {
		return nil
	}
	return fmt.Errorf("dbc does not modify pyproject.toml; edit [tool.dbc] manually or create dbc.toml")
}

// writeDriverListToSource writes a DriversList back to the given source.
func writeDriverListToSource(src driverListSource, list DriversList) error {
	if src.IsPyproject {
		return rejectPyprojectMutation(src)
	}
	f, err := os.Create(src.Path)
	if err != nil {
		return fmt.Errorf("error creating file %s: %w", src.Path, err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(list)
}
