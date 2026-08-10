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
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Masterminds/semver/v3"
	"github.com/columnar-tech/dbc"
	"github.com/columnar-tech/dbc/config"
	"github.com/columnar-tech/dbc/internal/jsonschema"
)

func manifestToPackageInfo(m config.Manifest) dbc.PkgInfo {
	return dbc.PkgInfo{
		Driver: dbc.Driver{
			Title:   m.Name,
			Path:    m.ID,
			License: m.License,
		},
		Version: m.Version,
	}
}

func parseDriverConstraint(driver string) (string, *semver.Constraints, error) {
	driver = strings.TrimSpace(driver)
	splitIdx := strings.IndexAny(driver, " ~^<>=!")
	if splitIdx == -1 {
		return driver, nil, nil
	}

	driverName := driver[:splitIdx]
	constraints, err := semver.NewConstraint(strings.TrimSpace(driver[splitIdx:]))
	if err != nil {
		return "", nil, fmt.Errorf("invalid version constraint: %w", err)
	}

	return driverName, constraints, nil
}

type InstallCmd struct {
	// URI    url.URL `arg:"-u" placeholder:"URL" help:"Base URL for fetching drivers"`
	Drivers            []string           `arg:"positional,required" help:"Drivers to install. Each driver can include an optional version constraint (for example: mysql, mysql=0.1.0, mysql>=1,<2)"`
	Driver             string             `arg:"-"`
	Level              config.ConfigLevel `arg:"-l" help:"Config level to install to (user, system)"`
	Json               bool               `arg:"--json" help:"Print output as JSON instead of plaintext"`
	JsonStreamProgress bool               `arg:"--json-stream-progress" help:"Stream progress events as JSON lines (implies --json)"`
	NoVerify           bool               `arg:"--no-verify" help:"Allow installation of drivers without a signature file"`
	Pre                bool               `arg:"--pre" help:"Allow implicit installation of pre-release versions"`
	InsecureNoChecksum bool               `arg:"--insecure-no-checksum" help:"Skip sha256 checksum recording (not recommended)"`
}

func (InstallCmd) Description() string {
	return "Install one or more drivers.\n\n" +
		"Each `DRIVER` may include a version constraint, for example `dbc install mysql`, `dbc install mysql postgresql`, `dbc install \"mysql=0.1.0\"`, or `dbc install \"mysql>=1,<2\"`.\n" +
		"See https://docs.columnar.tech/dbc/guides/installing/#version-constraints for more on version constraint syntax."
}

func (c InstallCmd) GetModelCustom(baseModel baseModel) tea.Model {
	drivers := append([]string(nil), c.Drivers...)
	// Driver is retained as an ignored parser field for callers that construct
	// InstallCmd directly. CLI input always arrives through Drivers.
	if len(drivers) == 0 && c.Driver != "" {
		drivers = []string{c.Driver}
	}
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	isLocal := len(drivers) == 1 && isLocalDriverPackage(drivers[0])
	localPackagePath := ""
	if isLocal {
		localPackagePath = drivers[0]
	}
	return progressiveInstallModel{
		Drivers:            drivers,
		NoVerify:           c.NoVerify,
		jsonOutput:         c.Json || c.JsonStreamProgress,
		jsonStreamProgress: c.JsonStreamProgress,
		Pre:                c.Pre,
		insecureNoChecksum: c.InsecureNoChecksum,
		spinner:            s,
		cfg:                getConfig(c.Level),
		baseModel:          baseModel,
		isLocal:            isLocal,
		localPackagePath:   localPackagePath,
		p: NewFileProgress(
			progress.WithDefaultBlend(),
			progress.WithWidth(20),
			progress.WithoutPercentage(),
		),
		queueProgress: progress.New(
			progress.WithDefaultBlend(),
			progress.WithWidth(40),
			progress.WithoutPercentage(),
		),
	}
}

func isLocalDriverPackage(driver string) bool {
	return strings.HasSuffix(driver, ".tar.gz") || strings.HasSuffix(driver, ".tgz")
}

func (c InstallCmd) GetModel() tea.Model {
	return c.GetModelCustom(defaultBaseModel())
}

func verifySignature(m config.Manifest, noVerify bool) error {
	if m.Files.Driver == "" || noVerify {
		return nil
	}

	path := filepath.Dir(m.Driver.Shared.Get(config.PlatformTuple()))

	lib, err := os.Open(filepath.Join(path, m.Files.Driver))
	if err != nil {
		return fmt.Errorf("could not open driver file: %w", err)
	}
	defer lib.Close()

	sigFile := m.Files.Signature
	if sigFile == "" {
		sigFile = m.Files.Driver + ".sig"
	}

	sig, err := os.Open(filepath.Join(path, sigFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("signature file '%s' for driver is missing", sigFile)
		}
		return fmt.Errorf("failed to open signature file: %w", err)
	}
	defer sig.Close()

	if err := dbc.SignedByColumnar(lib, sig); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	return nil
}

type writeDriverManifestMsg struct {
	DriverInfo config.DriverInfo
}

type driverManifestCreatedMsg struct{}

type localInstallMsg struct{}

// alreadyInstalledChecksumMsg carries the checksum computed for an already-installed driver.
type alreadyInstalledChecksumMsg string

type installState int

const (
	stSearching installState = iota
	stDownloading
	stInstalling
	stVerifying
	stDone
)

func (s installState) String() string {
	switch s {
	case stSearching:
		return "searching"
	case stDownloading:
		return "downloading"
	case stVerifying:
		return "verifying signature"
	case stInstalling:
		return "installing"
	default:
		return "done"
	}
}

func (progressiveInstallModel) NeedsRenderer() {}

func (m progressiveInstallModel) IsJSONMode() bool { return m.jsonOutput }

func (m progressiveInstallModel) WithJSONWriter(w io.Writer) tea.Model {
	m.jsonOut = w
	return m
}

func (m progressiveInstallModel) emitJSON(kind string, payload any) {
	out := m.jsonOut
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintln(out, marshalEnvelope(kind, payload))
}

func (m progressiveInstallModel) addEvent(event string, extra ...func(*jsonschema.InstallProgressEvent)) progressiveInstallModel {
	if !m.jsonStreamProgress {
		return m
	}
	evt := jsonschema.InstallProgressEvent{
		Event:   event,
		Driver:  m.Driver,
		Drivers: m.Drivers,
	}
	for _, fn := range extra {
		fn(&evt)
	}
	m.emitJSON("install.progress", evt)
	return m
}

type progressiveInstallModel struct {
	baseModel

	Drivers            []string
	Driver             string
	VersionInput       *semver.Version
	NoVerify           bool
	jsonOutput         bool
	jsonStreamProgress bool
	Pre                bool
	cfg                config.Config

	insecureNoChecksum  bool
	installedDriverInfo config.DriverInfo

	DriverPackage      dbc.PkgInfo
	conflictingInfo    config.DriverInfo
	postInstallMessage string

	state   installState
	spinner spinner.Model
	p       FileProgressModel
	// queueProgress tracks completed drivers for the sync-style multi-driver view.
	queueProgress progress.Model

	width, height    int
	isLocal          bool
	localPackagePath string

	registryErrors           error
	alreadyInstalledChecksum string
	jsonOut                  io.Writer
	jsonErrorOutput          string // JSON error envelope to emit via FinalOutput
	installItems             []installItem
	index                    int
	results                  []jsonschema.InstallStatus
}

type driversWithRegistryError struct {
	drivers []dbc.Driver
	err     error
}

func (m progressiveInstallModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		installDir := "."
		if locs := filepath.SplitList(m.cfg.Location); len(locs) > 0 && locs[0] != "" {
			installDir = locs[0]
		}
		lockDir := installDir
		for {
			if _, err := os.Stat(lockDir); err == nil {
				break
			}
			parent := filepath.Dir(lockDir)
			if parent == lockDir {
				lockDir = os.TempDir()
				break
			}
			lockDir = parent
		}
		lockPath := filepath.Join(lockDir, ".dbc.install.lock")
		lock, err := acquireLock(lockPath, 10*time.Second)
		if err != nil {
			return err
		}
		defer lock.Release()

		var needsRegistry bool
		for _, driver := range m.Drivers {
			if !isLocalDriverPackage(driver) {
				needsRegistry = true
				break
			}
		}
		if !needsRegistry {
			return m.resolveInstallItems(nil)
		}

		drivers, err := m.getDriverRegistry()
		return driversWithRegistryError{
			drivers: drivers,
			err:     err,
		}
	})
}

func (m progressiveInstallModel) Preamble() string {
	if m.isLocal {
		return "Installing from local package: " + m.localPackagePath + "\n\n"
	}
	return ""
}

func (m progressiveInstallModel) hasConflict() bool {
	return m.conflictingInfo.ID != "" && m.conflictingInfo.Version != nil
}

func (m progressiveInstallModel) isAlreadyInstalled() bool {
	return m.conflictingInfo.ID != "" && m.conflictingInfo.Version != nil &&
		m.conflictingInfo.Version.Equal(m.DriverPackage.Version)
}

func (m progressiveInstallModel) FinalOutput() string {
	if m.status != 0 {
		return m.jsonErrorOutput // empty string for non-JSON errors; structured envelope for JSON mode
	}

	var b strings.Builder
	for _, installStatus := range m.results {
		if m.jsonOutput {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(marshalEnvelope("install.status", installStatus))
			continue
		}
		if installStatus.Status == "already installed" {
			fmt.Fprintf(&b, "\nDriver %s %s already installed at %s",
				installStatus.Driver, installStatus.Version, installStatus.Location)
			continue
		}

		if installStatus.Conflict != "" {
			fmt.Fprintf(&b, "\nRemoved conflicting driver: %s", installStatus.Conflict)
		}

		fmt.Fprintf(&b, "\nInstalled %s %s to %s",
			installStatus.Driver, installStatus.Version, installStatus.Location)

		if installStatus.Message != "" {
			b.WriteString("\n\n" + postMsgStyle.Render(installStatus.Message))
		}
	}
	return b.String()
}

type installRequest struct {
	driverName      string
	localPath       string
	constraints     []*semver.Constraints
	constraintTexts []string
}

func groupInstallRequests(inputs []string) ([]installRequest, error) {
	requests := make([]installRequest, 0, len(inputs))
	requestIndexes := make(map[string]int, len(inputs))

	for _, input := range inputs {
		if isLocalDriverPackage(input) {
			key := "local\x00" + input
			if _, ok := requestIndexes[key]; ok {
				continue
			}
			requestIndexes[key] = len(requests)
			requests = append(requests, installRequest{localPath: input})
			continue
		}

		driverName, constraint, err := parseDriverConstraint(input)
		if err != nil {
			return nil, err
		}
		key := "registry\x00" + driverName
		idx, ok := requestIndexes[key]
		if !ok {
			idx = len(requests)
			requestIndexes[key] = idx
			requests = append(requests, installRequest{driverName: driverName})
		}
		if constraint != nil {
			requests[idx].constraints = append(requests[idx].constraints, constraint)
			requests[idx].constraintTexts = append(requests[idx].constraintTexts, constraint.String())
		}
	}

	return requests, nil
}

func packageMatchingAllConstraints(d dbc.Driver, constraints []*semver.Constraints, constraintTexts []string, platform string, includePrerelease bool) (dbc.PkgInfo, error) {
	if len(constraints) == 0 {
		return d.GetPackage(nil, platform, includePrerelease)
	}
	if len(constraints) == 1 {
		constraints[0].IncludePrerelease = includePrerelease
		return d.GetWithConstraint(constraints[0], platform)
	}

	for _, constraint := range constraints {
		constraint.IncludePrerelease = includePrerelease
	}

	var selected *semver.Version
	for _, version := range d.Versions(platform) {
		matches := true
		for _, constraint := range constraints {
			if !constraint.Check(version) {
				matches = false
				break
			}
		}
		if matches && (selected == nil || version.GreaterThan(selected)) {
			selected = version
		}
	}
	if selected == nil {
		return dbc.PkgInfo{}, fmt.Errorf(
			"conflicting constraints for driver `%s`: %s; no available version satisfies all constraints",
			d.Path, strings.Join(constraintTexts, " and "),
		)
	}

	// The exact version has already passed prerelease-aware constraint checks.
	return d.GetPackage(selected, platform, true)
}

func (m progressiveInstallModel) resolveInstallItems(list []dbc.Driver) tea.Msg {
	requests, err := groupInstallRequests(m.Drivers)
	if err != nil {
		return err
	}

	items := make([]installItem, 0, len(requests))
	for _, request := range requests {
		if request.localPath != "" {
			items = append(items, installItem{
				Driver:    dbc.Driver{Path: request.localPath},
				LocalPath: request.localPath,
			})
			continue
		}

		d, err := findDriver(request.driverName, list)
		if err != nil {
			return wrapWithRegistryContext(fmt.Errorf("could not find driver: %w", err), m.registryErrors)
		}

		pkg, err := packageMatchingAllConstraints(
			d, request.constraints, request.constraintTexts, config.PlatformTuple(), m.Pre,
		)
		if err != nil {
			if !m.Pre && !d.HasNonPrerelease() {
				for _, cfg := range config.Get() {
					if di, ok := cfg.Drivers[request.driverName]; ok && di.Version != nil && di.Version.Prerelease() != "" {
						return fmt.Errorf("driver `%s` is already installed (version %s); only pre-release versions are available for this driver; to update, use: dbc install --pre %s", request.driverName, di.Version, request.driverName)
					}
				}
			}
			return err
		}
		items = append(items, installItem{Driver: d, Package: pkg})
	}
	return items
}

func (m progressiveInstallModel) startCurrentItem() (tea.Model, tea.Cmd) {
	item := m.installItems[m.index]
	m.Driver = item.Driver.Path
	m.DriverPackage = item.Package
	m.conflictingInfo = config.DriverInfo{}
	m.installedDriverInfo = config.DriverInfo{}
	m.postInstallMessage = ""
	m.alreadyInstalledChecksum = ""
	m.state = stSearching
	m.isLocal = item.LocalPath != ""
	m.localPackagePath = item.LocalPath

	if m.isLocal {
		return m, func() tea.Msg { return localInstallMsg{} }
	}
	// Pre-populate conflictingInfo for conflict detection (e.g. a different version
	// already installed). inspectInstallTarget returns a non-empty DriverInfo even
	// when alreadyInstalled is false, so we check di.ID rather than the bool.
	if di, _ := inspectInstallTarget(m.cfg, item); di.ID != "" {
		m.conflictingInfo = di
	}
	return m.startDownloading()
}

func (m progressiveInstallModel) completeCurrent(status jsonschema.InstallStatus) (tea.Model, tea.Cmd) {
	m.results = append(m.results, status)
	if m.jsonStreamProgress {
		m.emitJSON("install.progress", jsonschema.InstallProgressEvent{
			Event:   "install.complete",
			Driver:  status.Driver,
			Drivers: m.Drivers,
		})
	}
	if m.index == len(m.installItems)-1 {
		return m, tea.Quit
	}
	m.index++
	return m.startCurrentItem()
}

func (m progressiveInstallModel) completeAlreadyInstalled() (tea.Model, tea.Cmd) {
	return m.completeCurrent(jsonschema.InstallStatus{
		Status:   "already installed",
		Driver:   m.conflictingInfo.ID,
		Version:  m.conflictingInfo.Version.String(),
		Location: filepath.SplitList(m.cfg.Location)[0],
		Checksum: m.alreadyInstalledChecksum,
	})
}

func (m progressiveInstallModel) startDownloading() (tea.Model, tea.Cmd) {
	m.state = stDownloading
	if m.isAlreadyInstalled() {
		m.state = stDone
		if m.jsonOutput && !m.insecureNoChecksum && m.conflictingInfo.Driver.Shared.Get(config.PlatformTuple()) != "" {
			driverPath := m.conflictingInfo.Driver.Shared.Get(config.PlatformTuple())
			return m, func() tea.Msg {
				chksum, err := checksum(driverPath)
				if err != nil {
					return fmt.Errorf("checksum_failed: %w", err)
				}
				return alreadyInstalledChecksumMsg(chksum)
			}
		}
		return m.completeAlreadyInstalled()
	}

	m = m.addEvent("download.start")
	return m, func() tea.Msg {
		output, err := m.downloadPkg(m.DriverPackage)
		if err != nil {
			return err
		}
		return output
	}
}

func (m progressiveInstallModel) startInstalling(downloaded *os.File) (tea.Model, tea.Cmd) {
	m.state = stInstalling
	if m.isLocal {
		driverName := strings.TrimSuffix(
			strings.TrimSuffix(filepath.Base(m.Driver), ".tar.gz"), ".tgz")
		parts := strings.Split(driverName, "_"+config.PlatformTuple()+"_")
		if len(parts) < 2 {
			m.Driver = driverName
		} else {
			m.Driver = parts[0] // drivername_platform_arch_version grab drivername
		}
	}

	return m, func() tea.Msg {
		item := m.installItems[m.index]
		item.Driver.Path = m.Driver
		var conflict *config.DriverInfo
		if m.conflictingInfo.ID != "" {
			conflict = &m.conflictingInfo
		}
		manifest, err := extractInstallItem(m.cfg, item, downloaded, conflict)
		if err != nil {
			return err
		}
		return manifest
	}
}

func (m progressiveInstallModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case alreadyInstalledChecksumMsg:
		m.alreadyInstalledChecksum = string(msg)
		return m.completeAlreadyInstalled()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case progressMsg:
		if m.jsonOutput {
			m = m.addEvent("download.progress", func(e *jsonschema.InstallProgressEvent) {
				e.Bytes = msg.written
				e.Total = msg.total
			})
		}
		progressCmd := m.p.SetPercent(msg.written, msg.total)
		return m, progressCmd
	case progress.FrameMsg:
		var cmd tea.Cmd
		m.p, cmd = m.p.Update(msg)
		return m, cmd
	case driversWithRegistryError:
		m.registryErrors = msg.err
		return m, func() tea.Msg { return m.resolveInstallItems(msg.drivers) }
	case []dbc.Driver:
		// For backwards compatibility, still handle plain driver list
		return m, func() tea.Msg { return m.resolveInstallItems(msg) }
	case []installItem:
		m.installItems = msg
		if len(m.installItems) == 0 {
			return m, errCmd("no drivers specified")
		}
		return m.startCurrentItem()
	case localInstallMsg:
		m.isLocal = true
		if m.localPackagePath == "" {
			m.localPackagePath = m.Driver
		}
		return m, func() tea.Msg {
			localDrv, err := os.Open(m.Driver)
			if err != nil {
				return err
			}
			return localDrv
		}
	case *os.File:
		if !m.isLocal {
			m = m.addEvent("download.complete")
		}
		m = m.addEvent("extract.start")
		return m.startInstalling(msg)
	case config.Manifest:
		if m.DriverPackage.Version == nil {
			m.DriverPackage = manifestToPackageInfo(msg)
		}

		m.state = stVerifying
		m.postInstallMessage = strings.Join(msg.PostInstall.Messages, "\n")
		m = m.addEvent("extract.complete")
		m = m.addEvent("verify.start")
		return m, func() tea.Msg {
			if err := verifyInstalledDriver(msg, m.NoVerify); err != nil {
				return err
			}
			return writeDriverManifestMsg{DriverInfo: msg.DriverInfo}
		}
	case writeDriverManifestMsg:
		m.state = stDone
		m.installedDriverInfo = msg.DriverInfo
		m = m.addEvent("verify.complete")
		m = m.addEvent("manifest.create")
		return m, func() tea.Msg {
			if err := createDriverManifest(m.cfg, msg.DriverInfo); err != nil {
				return err
			}
			return driverManifestCreatedMsg{}
		}
	case driverManifestCreatedMsg:
		status := jsonschema.InstallStatus{
			Status:   "installed",
			Driver:   m.Driver,
			Version:  m.DriverPackage.Version.String(),
			Location: filepath.SplitList(m.cfg.Location)[0],
			Message:  m.postInstallMessage,
		}
		if m.hasConflict() {
			status.Conflict = fmt.Sprintf("%s (version: %s)", m.conflictingInfo.ID, m.conflictingInfo.Version)
		}
		if !m.insecureNoChecksum {
			driverPath := m.installedDriverInfo.Driver.Shared.Get(config.PlatformTuple())
			if driverPath != "" {
				chksum, err := checksum(driverPath)
				if err != nil {
					if m.jsonOutput {
						return m, errCmd("checksum_failed: %w", err)
					}
					// In plaintext mode, checksum errors are non-fatal; the install
					// succeeds without embedding a checksum in the result.
				}
				if err == nil {
					status.Checksum = chksum
					m = m.addEvent("verify.checksum.ok", func(e *jsonschema.InstallProgressEvent) {
						e.Checksum = chksum
					})
				}
			}
		}
		return m.completeCurrent(status)
	case error:
		m.status = 1
		m.err = msg
		if m.jsonOutput {
			m.jsonErrorOutput = marshalEnvelope("error", jsonschema.ErrorResponse{
				Code:    "install_failed",
				Message: msg.Error(),
			})
			return m, tea.Quit
		}
	}

	base, cmd := m.baseModel.Update(msg)
	m.baseModel = base.(baseModel)
	return m, cmd
}

func checkbox(label string, checked bool) string {
	if checked {
		return fmt.Sprintf("[%s] %s", checkMark, label)
	}
	return fmt.Sprintf("[ ] %s", label)
}

var postMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))

func (m progressiveInstallModel) View() tea.View {
	if m.status != 0 || m.jsonOutput {
		return tea.NewView("")
	}
	if len(m.installItems) == 0 {
		return tea.NewView("Determining drivers to install...")
	}

	if m.isAlreadyInstalled() {
		return tea.NewView("")
	}
	if len(m.installItems) > 1 {
		driverName := m.installItems[m.index].Driver.Path
		progressView := m.queueProgress.ViewAs(float64(m.index) / float64(len(m.installItems)))
		return tea.NewView(renderInstallProgress(
			m.spinner.View(), progressView, driverName, m.width, m.index, len(m.installItems),
		))
	}

	var b strings.Builder
	for s := range stDone {
		if m.isLocal && (s == stSearching || s == stDownloading) {
			continue
		}

		if s == m.state {
			fmt.Fprintf(&b, "[%s] %s...", m.spinner.View(), s.String())
			if s == stDownloading {
				b.WriteString(" " + m.p.View())
			}
		} else {
			if s == stVerifying && s < m.state && m.NoVerify {
				fmt.Fprintf(&b, "[%s] %s", skipMark, s.String())
			} else {
				b.WriteString(checkbox(s.String(), s < m.state))
			}
		}
		b.WriteByte('\n')
	}

	return tea.NewView(b.String())
}
