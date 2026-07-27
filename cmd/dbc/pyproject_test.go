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
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverDriverList_PrefersPyproject(t *testing.T) {
	dir := t.TempDir()

	// Create both dbc.toml and pyproject.toml with [tool.dbc]
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dbc.toml"), []byte(`[drivers]
[drivers.mysql]
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`[project]
name = "myproject"

[tool.dbc.drivers]
[tool.dbc.drivers.postgresql]
`), 0644))

	src, err := discoverDriverList(dir)
	require.NoError(t, err)
	assert.True(t, src.IsPyproject)
	assert.Equal(t, filepath.Join(dir, "pyproject.toml"), src.Path)
}

func TestDiscoverDriverList_FallsToDBC(t *testing.T) {
	dir := t.TempDir()

	// Only dbc.toml exists
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dbc.toml"), []byte(`[drivers]
`), 0644))

	src, err := discoverDriverList(dir)
	require.NoError(t, err)
	assert.False(t, src.IsPyproject)
	assert.Equal(t, filepath.Join(dir, "dbc.toml"), src.Path)
}

func TestDiscoverDriverList_PyprojectWithoutToolDBC(t *testing.T) {
	dir := t.TempDir()

	// pyproject.toml exists but without [tool.dbc]
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`[project]
name = "myproject"

[tool.ruff]
line-length = 88
`), 0644))

	src, err := discoverDriverList(dir)
	require.NoError(t, err)
	assert.False(t, src.IsPyproject)
	assert.Equal(t, filepath.Join(dir, "dbc.toml"), src.Path)
}

func TestDiscoverDriverList_NeitherExists(t *testing.T) {
	dir := t.TempDir()

	src, err := discoverDriverList(dir)
	require.NoError(t, err)
	// Falls back to dbc.toml in the starting dir (even though it doesn't exist)
	assert.False(t, src.IsPyproject)
	assert.Equal(t, filepath.Join(dir, "dbc.toml"), src.Path)
}

func TestDiscoverDriverList_MalformedPyproject(t *testing.T) {
	dir := t.TempDir()

	// A pyproject.toml with broken TOML syntax should cause an error,
	// not silently fall through to dbc.toml.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`[tool.dbc
this is not valid toml!!!
`), 0644))

	_, err := discoverDriverList(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "error parsing")
}

func TestDiscoverDriverList_WalksUpTree(t *testing.T) {
	// Create a project structure: root/sub/deep/
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	deep := filepath.Join(sub, "deep")
	require.NoError(t, os.MkdirAll(deep, 0755))

	// Put dbc.toml in root only
	require.NoError(t, os.WriteFile(filepath.Join(root, "dbc.toml"), []byte(`[drivers]
`), 0644))

	// Discover from deep subdirectory should find root's dbc.toml
	src, err := discoverDriverList(deep)
	require.NoError(t, err)
	assert.False(t, src.IsPyproject)
	assert.Equal(t, filepath.Join(root, "dbc.toml"), src.Path)
}

func TestDiscoverDriverList_WalksUpTree_PyprojectPreferred(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.MkdirAll(sub, 0755))

	// Put both in root — pyproject.toml with [tool.dbc] should win
	require.NoError(t, os.WriteFile(filepath.Join(root, "dbc.toml"), []byte(`[drivers]
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pyproject.toml"), []byte(`[tool.dbc.drivers]
`), 0644))

	src, err := discoverDriverList(sub)
	require.NoError(t, err)
	assert.True(t, src.IsPyproject)
	assert.Equal(t, filepath.Join(root, "pyproject.toml"), src.Path)
}

func TestDiscoverDriverList_ClosestWins(t *testing.T) {
	// root has dbc.toml, sub has pyproject.toml with [tool.dbc]
	// Starting from sub, pyproject.toml should win (closer)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.MkdirAll(sub, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(root, "dbc.toml"), []byte(`[drivers]
[drivers.from-root]
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "pyproject.toml"), []byte(`[tool.dbc.drivers]
[tool.dbc.drivers.from-sub]
`), 0644))

	src, err := discoverDriverList(sub)
	require.NoError(t, err)
	assert.True(t, src.IsPyproject)
	assert.Equal(t, filepath.Join(sub, "pyproject.toml"), src.Path)
}

func TestExtractDBCSection(t *testing.T) {
	content := `[project]
name = "myproject"

[tool.dbc.drivers]
[tool.dbc.drivers.mysql]
version = '>=1.0.0'

[tool.dbc.drivers.snowflake]
prerelease = 'allow'
`
	list, err := extractDBCSection([]byte(content))
	require.NoError(t, err)
	assert.Len(t, list.Drivers, 2)
	assert.Contains(t, list.Drivers, "mysql")
	assert.Contains(t, list.Drivers, "snowflake")
	assert.Equal(t, "allow", list.Drivers["snowflake"].Prerelease)
}

func TestExtractDBCSection_WithRegistries(t *testing.T) {
	content := `[project]
name = "myproject"

[[tool.dbc.registries]]
url = "https://custom.example.com"
name = "Custom"

[tool.dbc.drivers]
[tool.dbc.drivers.mysql]
`
	list, err := extractDBCSection([]byte(content))
	require.NoError(t, err)
	assert.Len(t, list.Drivers, 1)
	require.Len(t, list.Registries, 1)
	assert.Equal(t, "https://custom.example.com", list.Registries[0].URL)
	assert.Equal(t, "Custom", list.Registries[0].Name)
}

func TestExtractDBCSection_NoToolSection(t *testing.T) {
	content := `[project]
name = "myproject"
`
	_, err := extractDBCSection([]byte(content))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "[tool] section not found")
}

func TestExtractDBCSection_NoDBCSection(t *testing.T) {
	content := `[tool.ruff]
line-length = 88
`
	_, err := extractDBCSection([]byte(content))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "[tool.dbc] section not found")
}

func TestWritePyprojectDriverList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pyproject.toml")

	// Create an existing pyproject.toml with other content
	initial := `[project]
name = "myproject"
version = "1.0.0"

[tool.dbc.drivers]
`
	require.NoError(t, os.WriteFile(path, []byte(initial), 0644))

	list := DriversList{
		Drivers: map[string]driverSpec{
			"mysql": {},
		},
	}

	err := writePyprojectDriverList(path, list)
	require.NoError(t, err)

	// Verify the file was written and can be read back
	readBack, err := openAndDecodePyprojectDriverList(path)
	require.NoError(t, err)
	assert.Contains(t, readBack.Drivers, "mysql")

	// Verify the [project] section is preserved
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "myproject")
}

func TestWritePyprojectDriverList_PreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pyproject.toml")

	// A realistic pyproject.toml with comments, various sections, and formatting
	initial := `# My awesome project
[project]
name = "myproject"
version = "1.0.0"
description = "A test project"

# Python dependencies
dependencies = [
    "requests>=2.28",
    "pandas",
]

# Ruff linter configuration
[tool.ruff]
line-length = 88
target-version = "py311"

# DBC driver configuration
[tool.dbc.drivers]
[tool.dbc.drivers.duckdb]
version = '=1.4.0'

# Pytest settings
[tool.pytest.ini_options]
testpaths = ["tests"]
`
	require.NoError(t, os.WriteFile(path, []byte(initial), 0644))

	list := DriversList{
		Drivers: map[string]driverSpec{
			"mysql":      {},
			"postgresql": {},
		},
	}

	err := writePyprojectDriverList(path, list)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)

	// Verify comments are preserved
	assert.Contains(t, content, "# My awesome project")
	assert.Contains(t, content, "# Python dependencies")
	assert.Contains(t, content, "# Ruff linter configuration")
	assert.Contains(t, content, "# Pytest settings")

	// Verify other sections are preserved
	assert.Contains(t, content, `name = "myproject"`)
	assert.Contains(t, content, "line-length = 88")
	assert.Contains(t, content, `testpaths = ["tests"]`)

	// Verify the drivers were updated
	readBack, err := openAndDecodePyprojectDriverList(path)
	require.NoError(t, err)
	assert.Contains(t, readBack.Drivers, "mysql")
	assert.Contains(t, readBack.Drivers, "postgresql")
	// Old driver should be gone (we replaced the whole section)
	assert.NotContains(t, readBack.Drivers, "duckdb")
}

func TestWritePyprojectDriverList_SectionAtEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pyproject.toml")

	// [tool.dbc] at the very end of file (no trailing section)
	initial := `[project]
name = "myproject"

[tool.dbc.drivers]
[tool.dbc.drivers.old-driver]
`
	require.NoError(t, os.WriteFile(path, []byte(initial), 0644))

	list := DriversList{
		Drivers: map[string]driverSpec{
			"new-driver": {},
		},
	}

	err := writePyprojectDriverList(path, list)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, `name = "myproject"`)

	readBack, err := openAndDecodePyprojectDriverList(path)
	require.NoError(t, err)
	assert.Contains(t, readBack.Drivers, "new-driver")
	assert.NotContains(t, readBack.Drivers, "old-driver")
}

func TestMarshalDBCSection(t *testing.T) {
	list := DriversList{
		Drivers: map[string]driverSpec{
			"mysql": {},
		},
	}

	result, err := marshalDBCSection(list)
	require.NoError(t, err)

	content := string(result)
	// Should have tool.dbc prefix on table headers
	assert.Contains(t, content, "[tool.dbc.drivers]")
	assert.Contains(t, content, "[tool.dbc.drivers.mysql]")
	// Should NOT have bare [drivers] headers
	assert.NotContains(t, content, "\n[drivers]")
}

func TestLockfilePath(t *testing.T) {
	t.Run("dbc.toml", func(t *testing.T) {
		src := driverListSource{Path: "/project/dbc.toml", IsPyproject: false}
		assert.Equal(t, "/project/dbc.lock", src.lockfilePath())
	})

	t.Run("pyproject.toml uses dbc.lock too", func(t *testing.T) {
		src := driverListSource{Path: "/project/pyproject.toml", IsPyproject: true}
		assert.Equal(t, "/project/dbc.lock", src.lockfilePath(),
			"lockfile should always be dbc.lock regardless of config source")
	})

	t.Run("custom filename still uses dbc.lock", func(t *testing.T) {
		src := driverListSource{Path: "/project/drivers-dev.toml", IsPyproject: false}
		assert.Equal(t, "/project/dbc.lock", src.lockfilePath())
	})
}

func TestResolveDriverListSource(t *testing.T) {
	t.Run("explicit pyproject.toml path", func(t *testing.T) {
		dir := t.TempDir()
		pypath := filepath.Join(dir, "pyproject.toml")
		require.NoError(t, os.WriteFile(pypath, []byte("[tool.dbc.drivers]\n"), 0644))

		src, err := resolveDriverListSource(pypath)
		require.NoError(t, err)
		assert.True(t, src.IsPyproject)
		assert.Equal(t, pypath, src.Path)
	})

	t.Run("explicit dbc.toml path", func(t *testing.T) {
		dir := t.TempDir()
		dbcpath := filepath.Join(dir, "dbc.toml")
		require.NoError(t, os.WriteFile(dbcpath, []byte("[drivers]\n"), 0644))

		src, err := resolveDriverListSource(dbcpath)
		require.NoError(t, err)
		assert.False(t, src.IsPyproject)
		assert.Equal(t, dbcpath, src.Path)
	})

	t.Run("explicit dbc.toml is respected even when pyproject.toml exists", func(t *testing.T) {
		// When user explicitly passes -p ./dbc.toml and that file exists,
		// it should be used even if pyproject.toml with [tool.dbc] also exists.
		dir := t.TempDir()
		dbcpath := filepath.Join(dir, "dbc.toml")
		pypath := filepath.Join(dir, "pyproject.toml")
		require.NoError(t, os.WriteFile(dbcpath, []byte("[drivers]\n"), 0644))
		require.NoError(t, os.WriteFile(pypath, []byte("[tool.dbc.drivers]\n"), 0644))

		src, err := resolveDriverListSource(dbcpath)
		require.NoError(t, err)
		assert.False(t, src.IsPyproject, "explicit path to dbc.toml must not auto-discover pyproject.toml")
		assert.Equal(t, dbcpath, src.Path)
	})

	t.Run("non-existent file with default path triggers discovery", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.dbc.drivers]\n"), 0644))

		origDir, err := os.Getwd()
		require.NoError(t, err)
		require.NoError(t, os.Chdir(dir))
		defer os.Chdir(origDir)

		src, err := resolveDriverListSource(defaultDriverListPath)
		require.NoError(t, err)
		assert.True(t, src.IsPyproject, "default path with no dbc.toml should auto-discover pyproject.toml")
	})
}

func TestAddWithPyproject(t *testing.T) {
	dir := t.TempDir()
	pyprojectPath := filepath.Join(dir, "pyproject.toml")

	// Create pyproject.toml with [tool.dbc] section
	require.NoError(t, os.WriteFile(pyprojectPath, []byte(`[project]
name = "myproject"

[tool.dbc.drivers]
`), 0644))

	// Use explicit path to pyproject.toml
	m := AddCmd{Path: pyprojectPath, Driver: []string{"test-driver-1"}}.GetModelCustom(
		testBaseModel())

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&out), tea.WithContext(ctx))

	result, err := p.Run()
	require.NoError(t, err)
	assert.Equal(t, 0, result.(HasStatus).Status())

	// Verify the driver was added to pyproject.toml
	data, err := os.ReadFile(pyprojectPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "myproject")

	// Verify we can read it back
	list, err := openAndDecodePyprojectDriverList(pyprojectPath)
	require.NoError(t, err)
	assert.Contains(t, list.Drivers, "test-driver-1")
}

func TestRemoveWithPyproject(t *testing.T) {
	dir := t.TempDir()
	pyprojectPath := filepath.Join(dir, "pyproject.toml")

	// Create pyproject.toml with a driver already present
	require.NoError(t, os.WriteFile(pyprojectPath, []byte(`[project]
name = "myproject"

[tool.dbc.drivers]
[tool.dbc.drivers.test-driver-1]
`), 0644))

	m := RemoveCmd{Path: pyprojectPath, Driver: "test-driver-1"}.GetModelCustom(
		testBaseModel())

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&out), tea.WithContext(ctx))

	result, err := p.Run()
	require.NoError(t, err)
	assert.Equal(t, 0, result.(HasStatus).Status())

	// Verify the driver was removed
	list, err := openAndDecodePyprojectDriverList(pyprojectPath)
	require.NoError(t, err)
	assert.NotContains(t, list.Drivers, "test-driver-1")

	// Verify [project] section is preserved
	data, err := os.ReadFile(pyprojectPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "myproject")
}

func TestInitPyproject(t *testing.T) {
	dir := t.TempDir()

	// Change to temp dir for the test (init --pyproject uses cwd)
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(origDir)

	m := InitCmd{Path: defaultDriverListPath, Pyproject: true}.GetModel()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&out), tea.WithContext(ctx))

	result, err := p.Run()
	require.NoError(t, err)
	assert.Equal(t, 0, result.(HasStatus).Status())

	// Verify pyproject.toml was created
	data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "[tool.dbc.drivers]")
}

func TestInitPyproject_ExistingFile(t *testing.T) {
	dir := t.TempDir()

	// Create an existing pyproject.toml without [tool.dbc]
	pyprojectPath := filepath.Join(dir, "pyproject.toml")
	require.NoError(t, os.WriteFile(pyprojectPath, []byte(`[project]
name = "myproject"
version = "1.0.0"
`), 0644))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(origDir)

	m := InitCmd{Path: defaultDriverListPath, Pyproject: true}.GetModel()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&out), tea.WithContext(ctx))

	result, err := p.Run()
	require.NoError(t, err)
	assert.Equal(t, 0, result.(HasStatus).Status())

	// Verify [tool.dbc] was appended
	data, err := os.ReadFile(pyprojectPath)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "[project]")
	assert.Contains(t, content, "myproject")
	assert.Contains(t, content, "[tool.dbc.drivers]")
}

func TestInitPyproject_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subproject")
	require.NoError(t, os.MkdirAll(subdir, 0755))

	pypath := filepath.Join(subdir, "pyproject.toml")

	// dbc init path/to/pyproject.toml --pyproject should write to that path
	m := InitCmd{Path: pypath, Pyproject: true}.GetModel()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&out), tea.WithContext(ctx))

	result, err := p.Run()
	require.NoError(t, err)
	assert.Equal(t, 0, result.(HasStatus).Status())

	// Verify the file was created at the explicit path
	data, err := os.ReadFile(pypath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "[tool.dbc.drivers]")
}

func TestInitPyproject_AlreadyHasDBCSection(t *testing.T) {
	dir := t.TempDir()

	pyprojectPath := filepath.Join(dir, "pyproject.toml")
	require.NoError(t, os.WriteFile(pyprojectPath, []byte(`[project]
name = "myproject"

[tool.dbc.drivers]
[tool.dbc.drivers.mysql]
`), 0644))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(origDir)

	m := InitCmd{Path: defaultDriverListPath, Pyproject: true}.GetModel()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&out), tea.WithContext(ctx))

	result, err := p.Run()
	require.NoError(t, err)
	// Should fail because [tool.dbc] already exists
	assert.Equal(t, 1, result.(HasStatus).Status())
}
