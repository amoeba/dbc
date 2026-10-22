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

package dbc_test

// These tests exercise the examples in testdata/registry_spec, which are
// included verbatim in docs/reference/driver_registry.md.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/columnar-tech/dbc"
	"github.com/columnar-tech/dbc/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readSpecFile reads an example from testdata/registry_spec, rewriting the
// linux_amd64 platform used in the docs to the platform running the test.
func readSpecFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "registry_spec", name))
	require.NoError(t, err)
	return []byte(strings.ReplaceAll(string(data), "linux_amd64", config.PlatformTuple()))
}

// makeTarball builds a driver archive containing files in the given order.
func makeTarball(t *testing.T, files map[string][]byte, order []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range order {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(files[name])),
			Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write(files[name])
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// newSpecServer serves index at /index.yaml and tarball at tarballPath only,
// so a test fails if dbc resolves a different package URL.
func newSpecServer(t *testing.T, index []byte, tarballPath string, tarball []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			w.Header().Set("Content-Type", "application/yaml")
			w.Write(index)
		case tarballPath:
			w.Header().Set("Content-Type", "application/gzip")
			w.Write(tarball)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func minimalPackage(t *testing.T) []byte {
	return makeTarball(t, map[string][]byte{
		"MANIFEST": readSpecFile(t, "MANIFEST_minimal.toml"),
	}, []string{"MANIFEST"})
}

func installFromSpecServer(t *testing.T, srv *httptest.Server) (config.Config, *config.Manifest) {
	t.Helper()
	cfg := config.Config{Level: config.ConfigEnv, Location: t.TempDir()}
	manifest, err := newTestClientForServer(t, srv.URL).Install(t.Context(), cfg, "my_driver")
	require.NoError(t, err)
	return cfg, manifest
}

func TestRegistrySpecMinimalIndex(t *testing.T) {
	platform := config.PlatformTuple()
	// With no url set, dbc derives the package location from path, version, and platform.
	defaultPath := "/my_driver/0.1.0/my_driver_" + platform + "-0.1.0.tar.gz"
	srv := newSpecServer(t, readSpecFile(t, "index_minimal.yaml"), defaultPath, minimalPackage(t))

	drivers, err := newTestClientForServer(t, srv.URL).Search(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, drivers, 1)
	assert.Equal(t, "my_driver", drivers[0].Path)

	_, manifest := installFromSpecServer(t, srv)
	assert.Equal(t, "my_driver", manifest.DriverInfo.ID)
	assert.Equal(t, "0.1.0", manifest.DriverInfo.Version.String())
}

func TestRegistrySpecRecommendedIndex(t *testing.T) {
	platform := config.PlatformTuple()
	pkgPath := "/my_driver/0.1.0/my_driver_" + platform + "-0.1.0.tar.gz"
	srv := newSpecServer(t, readSpecFile(t, "index_recommended.yaml"), pkgPath, minimalPackage(t))

	c, err := dbc.NewClient(dbc.WithHTTPClient(&http.Client{}), dbc.WithBaseURL(srv.URL))
	require.NoError(t, err)
	drivers, err := c.Search(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, drivers, 1)

	d := drivers[0]
	assert.Equal(t, "My Registry", d.Registry.Name)
	assert.Equal(t, "My Driver", d.Title)
	assert.Equal(t, "A description of the driver", d.Desc)
	assert.Equal(t, "MIT", d.License)
	assert.Equal(t, "my_driver", d.Path)
	assert.Equal(t, []string{"https://example.com"}, d.URLs)
	assert.Equal(t, "https://example.com/docs", d.DocsURL)

	versions := d.AllVersions()
	require.Len(t, versions, 1)
	assert.Equal(t, "0.1.0", versions[0].Version.String())
	var platforms []string
	for _, p := range versions[0].Packages {
		platforms = append(platforms, p.Platform)
	}
	assert.Contains(t, platforms, platform)

	_, manifest := installFromSpecServer(t, srv)
	assert.Equal(t, "my_driver", manifest.DriverInfo.ID)
}

func TestRegistrySpecMinimalPackage(t *testing.T) {
	defaultPath := "/my_driver/0.1.0/my_driver_" + config.PlatformTuple() + "-0.1.0.tar.gz"
	srv := newSpecServer(t, readSpecFile(t, "index_minimal.yaml"), defaultPath, minimalPackage(t))

	cfg, manifest := installFromSpecServer(t, srv)
	assert.Equal(t, "My Driver", manifest.DriverInfo.Name)
	assert.Equal(t, "0.1.0", manifest.DriverInfo.Version.String())

	installed, err := config.GetDriver(cfg, "my_driver")
	require.NoError(t, err)
	assert.Equal(t, "My Driver", installed.Name)
}

func TestRegistrySpecRecommendedPackage(t *testing.T) {
	defaultPath := "/my_driver/0.1.0/my_driver_" + config.PlatformTuple() + "-0.1.0.tar.gz"
	tarball := makeTarball(t, map[string][]byte{
		"MANIFEST":            readSpecFile(t, "MANIFEST_recommended.toml"),
		"libmy_driver.so":     []byte("not a real driver"),
		"libmy_driver.so.sig": []byte("not a real signature"),
	}, []string{"MANIFEST", "libmy_driver.so", "libmy_driver.so.sig"})
	srv := newSpecServer(t, readSpecFile(t, "index_minimal.yaml"), defaultPath, tarball)

	cfg, manifest := installFromSpecServer(t, srv)
	assert.Equal(t, "My Driver", manifest.DriverInfo.Name)
	assert.Equal(t, "My Company", manifest.DriverInfo.Publisher)
	assert.Equal(t, "MIT", manifest.DriverInfo.License)
	assert.Equal(t, "0.1.0", manifest.DriverInfo.Version.String())
	assert.Equal(t, "1.1.0", manifest.DriverInfo.AdbcInfo.Version.String())
	assert.Equal(t, "libmy_driver.so", manifest.Files.Driver)
	assert.Equal(t, "libmy_driver.so.sig", manifest.Files.Signature)

	shared := manifest.DriverInfo.Driver.Shared.Get(config.PlatformTuple())
	assert.Equal(t, "libmy_driver.so", filepath.Base(shared))
	assert.FileExists(t, shared)
	assert.FileExists(t, filepath.Join(filepath.Dir(shared), "libmy_driver.so.sig"))

	installed, err := config.GetDriver(cfg, "my_driver")
	require.NoError(t, err)
	assert.Equal(t, "My Company", installed.Publisher)
}
