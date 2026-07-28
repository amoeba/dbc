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
	"github.com/columnar-tech/dbc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverDriverList_PrefersDBC(t *testing.T) {
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
	assert.False(t, src.IsPyproject)
	assert.Equal(t, filepath.Join(dir, "dbc.toml"), src.Path)
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

func TestDiscoverDriverList_DBCIgnoresMalformedPyproject(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "dbc.toml"), []byte(`[drivers]
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`[tool.dbc
this is not valid toml!!!
`), 0644))

	src, err := discoverDriverList(dir)
	require.NoError(t, err)
	assert.False(t, src.IsPyproject)
	assert.Equal(t, filepath.Join(dir, "dbc.toml"), src.Path)
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

func TestDiscoverDriverList_WalksUpTree_DBCPreferredInSameDirectory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.MkdirAll(sub, 0755))

	// Put both in root — standalone dbc.toml should win
	require.NoError(t, os.WriteFile(filepath.Join(root, "dbc.toml"), []byte(`[drivers]
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pyproject.toml"), []byte(`[tool.dbc.drivers]
`), 0644))

	src, err := discoverDriverList(sub)
	require.NoError(t, err)
	assert.False(t, src.IsPyproject)
	assert.Equal(t, filepath.Join(root, "dbc.toml"), src.Path)
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

	t.Run("omitted path triggers discovery", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.dbc.drivers]\n"), 0644))

		origDir, err := os.Getwd()
		require.NoError(t, err)
		require.NoError(t, os.Chdir(dir))
		defer os.Chdir(origDir)

		src, err := resolveDriverListSource("")
		require.NoError(t, err)
		assert.True(t, src.IsPyproject, "omitted path should auto-discover pyproject.toml")
	})

	t.Run("omitted path prefers dbc.toml when both files exist", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "dbc.toml"), []byte("[drivers]\n"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.dbc.drivers]\n"), 0644))

		origDir, err := os.Getwd()
		require.NoError(t, err)
		require.NoError(t, os.Chdir(dir))
		defer os.Chdir(origDir)

		src, err := resolveDriverListSource("")
		require.NoError(t, err)
		assert.False(t, src.IsPyproject, "omitted path should prefer dbc.toml over pyproject.toml")
		expected, err := filepath.Abs(filepath.Join(dir, "dbc.toml"))
		require.NoError(t, err)
		expected, err = filepath.EvalSymlinks(expected)
		require.NoError(t, err)
		actual, err := filepath.EvalSymlinks(src.Path)
		require.NoError(t, err)
		assert.Equal(t, expected, actual)
	})
}

func TestAddWithPyprojectFails(t *testing.T) {
	dir := t.TempDir()
	pyprojectPath := filepath.Join(dir, "pyproject.toml")

	initial := `[project]
name = "myproject"

[tool.dbc.drivers]
`
	require.NoError(t, os.WriteFile(pyprojectPath, []byte(initial), 0644))

	m := AddCmd{Path: pyprojectPath, Driver: []string{"test-driver-1"}}.GetModelCustom(
		testBaseModel())

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&out), tea.WithContext(ctx))

	result, err := p.Run()
	require.NoError(t, err)
	assert.Equal(t, 1, result.(HasStatus).Status())
	assert.Contains(t, result.(HasStatus).Err().Error(), "does not modify pyproject.toml")

	data, err := os.ReadFile(pyprojectPath)
	require.NoError(t, err)
	assert.Equal(t, initial, string(data))
}

func TestAddWithOmittedPathPrefersDBCOverPyproject(t *testing.T) {
	dir := t.TempDir()
	pyprojectPath := filepath.Join(dir, "pyproject.toml")
	dbcPath := filepath.Join(dir, "dbc.toml")

	require.NoError(t, os.WriteFile(dbcPath, []byte(`[drivers]
`), 0644))
	require.NoError(t, os.WriteFile(pyprojectPath, []byte(`[project]
name = "myproject"

[tool.dbc.drivers]
`), 0644))

	drivers, err := getTestDriverRegistry()
	require.NoError(t, err)

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(origDir)

	m := AddCmd{Driver: []string{"test-driver-1"}}.GetModelCustom(baseModel{
		getDriverRegistry: func() ([]dbc.Driver, error) {
			return drivers, nil
		},
	})

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&out), tea.WithContext(ctx))

	result, err := p.Run()
	require.NoError(t, err)
	if status, ok := result.(HasStatus); ok && status.Err() != nil {
		require.NoError(t, status.Err())
	}

	list, err := openAndDecodeDriverList(dbcPath)
	require.NoError(t, err)
	assert.Contains(t, list.Drivers, "test-driver-1")

	pyprojectData, err := os.ReadFile(pyprojectPath)
	require.NoError(t, err)
	assert.NotContains(t, string(pyprojectData), "test-driver-1")
}

func TestRemoveWithPyprojectFails(t *testing.T) {
	dir := t.TempDir()
	pyprojectPath := filepath.Join(dir, "pyproject.toml")

	initial := `[project]
name = "myproject"

[tool.dbc.drivers]
[tool.dbc.drivers.test-driver-1]
`
	require.NoError(t, os.WriteFile(pyprojectPath, []byte(initial), 0644))

	m := RemoveCmd{Path: pyprojectPath, Driver: "test-driver-1"}.GetModelCustom(
		testBaseModel())

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&out), tea.WithContext(ctx))

	result, err := p.Run()
	require.NoError(t, err)
	assert.Equal(t, 1, result.(HasStatus).Status())
	assert.Contains(t, result.(HasStatus).Err().Error(), "does not modify pyproject.toml")

	data, err := os.ReadFile(pyprojectPath)
	require.NoError(t, err)
	assert.Equal(t, initial, string(data))
}

func TestInitRejectsPyprojectPath(t *testing.T) {
	dir := t.TempDir()
	pyprojectPath := filepath.Join(dir, "pyproject.toml")

	initial := `[project]
name = "myproject"
version = "1.0.0"
`
	require.NoError(t, os.WriteFile(pyprojectPath, []byte(initial), 0644))
	m := InitCmd{Path: pyprojectPath}.GetModel()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(&out), tea.WithContext(ctx))

	result, err := p.Run()
	require.NoError(t, err)
	assert.Equal(t, 1, result.(HasStatus).Status())
	assert.Contains(t, result.(HasStatus).Err().Error(), "does not modify pyproject.toml")

	data, err := os.ReadFile(pyprojectPath)
	require.NoError(t, err)
	assert.Equal(t, initial, string(data))
}
