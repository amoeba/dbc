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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/columnar-tech/dbc"
	"github.com/columnar-tech/dbc/config"
)

type installItem struct {
	Driver    dbc.Driver
	Package   dbc.PkgInfo
	Checksum  string
	LocalPath string
}

func renderInstallProgress(spinner, progress, driverName string, width, index, total int) string {
	countWidth := lipgloss.Width(fmt.Sprintf("%d", total))
	driverCount := fmt.Sprintf(" %*d/%*d", countWidth, index, countWidth, total)
	spin := spinner + " "
	cellsAvail := max(0, width-lipgloss.Width(spin+progress+driverCount))
	info := lipgloss.NewStyle().MaxWidth(cellsAvail).Render("Installing " + driverName)
	cellsRemaining := max(0, width-lipgloss.Width(spin+info+progress+driverCount))
	gap := strings.Repeat(" ", cellsRemaining)
	return spin + info + gap + progress + driverCount
}

func inspectInstallTarget(cfg config.Config, item installItem) (config.DriverInfo, bool) {
	drv, err := config.GetDriver(cfg, item.Driver.Path)
	if err != nil {
		return config.DriverInfo{}, false
	}
	if drv.Version == nil || item.Package.Version == nil || !item.Package.Version.Equal(drv.Version) {
		return drv, false
	}
	return drv, true
}

func inspectInstalledItem(cfg config.Config, item installItem) (config.DriverInfo, string, bool, error) {
	drv, alreadyInstalled := inspectInstallTarget(cfg, item)
	if !alreadyInstalled {
		return drv, "", false, nil
	}

	chksum, err := checksum(drv.Driver.Shared.Get(config.PlatformTuple()))
	if err != nil {
		return config.DriverInfo{}, "", false, fmt.Errorf("failed to compute checksum: %w", err)
	}
	if item.Checksum != "" && chksum != item.Checksum {
		return config.DriverInfo{}, "", false, fmt.Errorf("checksum mismatch for driver %s: %s != %s",
			item.Driver.Path, chksum, item.Checksum)
	}

	return drv, chksum, true, nil
}

func extractInstallItem(cfg config.Config, item installItem, downloaded *os.File, conflict *config.DriverInfo) (config.Manifest, error) {
	if conflict != nil {
		if err := config.UninstallDriver(cfg, *conflict); err != nil {
			return config.Manifest{}, fmt.Errorf("failed when deleting driver %s-%s: %w",
				conflict.ID, conflict.Version, err)
		}
	}

	manifest, err := config.InstallDriver(cfg, item.Driver.Path, downloaded)
	if err != nil {
		return config.Manifest{}, err
	}
	return manifest, nil
}

func verifyInstalledDriver(manifest config.Manifest, noVerify bool) error {
	if err := verifySignature(manifest, noVerify); err != nil {
		sharedPath := manifest.Driver.Shared.Get(config.PlatformTuple())
		if sharedPath != "" {
			_ = os.RemoveAll(filepath.Dir(sharedPath))
		}
		return err
	}
	return nil
}

func createDriverManifest(cfg config.Config, driver config.DriverInfo) error {
	if err := config.CreateManifest(cfg, driver); err != nil {
		return fmt.Errorf("failed to create driver manifest: %w", err)
	}
	return nil
}
