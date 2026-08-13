package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	pinnedGoVersion = "1.26.5"
	pinnedGoAMD64   = "5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053"
	pinnedGoARM64   = "fe4789e92b1f33358680864bbe8704289e7bb5fc207d80623c308935bd696d49"
)

func TestInstallerPrefersPinnedLocalToolchainOverNewerPATHGo(t *testing.T) {
	testRoot := t.TempDir()
	fakeBin := filepath.Join(testRoot, "bin")
	dataDir := filepath.Join(testRoot, "data")
	localGo := filepath.Join(dataDir, "toolchains", "go"+pinnedGoVersion, "bin", "go")
	writeInstallerExecutable(t, filepath.Join(fakeBin, "go"), goVersionFixture("1.27.0"))
	writeInstallerExecutable(t, localGo, goVersionFixture(pinnedGoVersion))

	output := runInstallerFunctions(t, testRoot, fakeBin, dataDir, `
source "$1"
select_go
printf 'SELECTED=%s\n' "$selected_go_binary"
`)
	if !strings.Contains(output, "SELECTED="+localGo) {
		t.Fatalf("installer did not select the exact pinned local Go:\n%s", output)
	}
	if strings.Contains(output, "SELECTED="+filepath.Join(fakeBin, "go")) {
		t.Fatalf("installer selected a newer PATH Go:\n%s", output)
	}
}

func TestInstallerRejectsNewerPATHGoAndInstallsPin(t *testing.T) {
	testRoot := t.TempDir()
	fakeBin := filepath.Join(testRoot, "bin")
	dataDir := filepath.Join(testRoot, "data")
	localGo := filepath.Join(dataDir, "toolchains", "go"+pinnedGoVersion, "bin", "go")
	writeInstallerExecutable(t, filepath.Join(fakeBin, "go"), goVersionFixture("1.27.0"))
	fixture := installOfflineGoDownloadFixtures(t, testRoot, fakeBin)

	output := runInstallerFunctionsWithEnv(t, testRoot, fakeBin, dataDir, fixture.environment(), `
source "$1"
select_go
printf 'SELECTED=%s\n' "$selected_go_binary"
`)
	if !strings.Contains(output, "does not match required Go "+pinnedGoVersion) {
		t.Fatalf("installer did not reject the newer PATH Go:\n%s", output)
	}
	if !strings.Contains(output, "SELECTED="+localGo) {
		t.Fatalf("installer did not activate the downloaded pinned Go:\n%s", output)
	}
	assertGoFixtureVersion(t, localGo, pinnedGoVersion)
	fixture.assertAMD64Download(t)
}

func TestInstallerReusesExactPinnedPATHGo(t *testing.T) {
	testRoot := t.TempDir()
	fakeBin := filepath.Join(testRoot, "bin")
	dataDir := filepath.Join(testRoot, "data")
	pathGo := filepath.Join(fakeBin, "go")
	writeInstallerExecutable(t, pathGo, goVersionFixture(pinnedGoVersion))

	output := runInstallerFunctions(t, testRoot, fakeBin, dataDir, `
source "$1"
select_go
printf 'SELECTED=%s\n' "$selected_go_binary"
`)
	if !strings.Contains(output, "SELECTED="+pathGo) {
		t.Fatalf("installer did not reuse an exact pinned PATH Go:\n%s", output)
	}
}

func TestInstallerRepairsAndPreservesMismatchedLocalToolchain(t *testing.T) {
	testRoot := t.TempDir()
	fakeBin := filepath.Join(testRoot, "bin")
	dataDir := filepath.Join(testRoot, "data")
	toolchainParent := filepath.Join(dataDir, "toolchains")
	localToolchain := filepath.Join(toolchainParent, "go"+pinnedGoVersion)
	localGo := filepath.Join(localToolchain, "bin", "go")

	writeInstallerExecutable(t, filepath.Join(fakeBin, "go"), goVersionFixture(pinnedGoVersion))
	writeInstallerExecutable(t, localGo, goVersionFixture("1.25.12"))
	fixture := installOfflineGoDownloadFixtures(t, testRoot, fakeBin)

	output := runInstallerFunctionsWithEnv(t, testRoot, fakeBin, dataDir, fixture.environment(), `
source "$1"
select_go
printf 'SELECTED=%s\n' "$selected_go_binary"
`)
	if !strings.Contains(output, "SELECTED="+localGo) {
		t.Fatalf("installer did not activate the repaired local toolchain:\n%s", output)
	}
	assertGoFixtureVersion(t, localGo, pinnedGoVersion)

	backups, err := filepath.Glob(filepath.Join(toolchainParent, ".go"+pinnedGoVersion+".replaced.*", "go", "bin", "go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("mismatched toolchain backups = %v; want exactly one", backups)
	}
	assertGoFixtureVersion(t, backups[0], "1.25.12")
	fixture.assertAMD64Download(t)
}

func TestInstallerContractPinsExactGo(t *testing.T) {
	contents, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	installer := string(contents)
	for _, required := range []string{
		`readonly GO_VERSION="1.26.5"`,
		`readonly GO_AMD64_SHA256="` + pinnedGoAMD64 + `"`,
		`readonly GO_ARM64_SHA256="` + pinnedGoARM64 + `"`,
		`[[ "${version}" == "${GO_VERSION}" ]]`,
		`mv --no-target-directory -- "${temporary_toolchain_staging}/go" "${TOOLCHAIN_DIR}"`,
		`if [[ "${BASH_SOURCE[0]}" == "$0" ]]`,
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("installer is missing exact-Go contract %q", required)
		}
	}
	for _, forbidden := range []string{"MIN_GO_VERSION", "version_at_least"} {
		if strings.Contains(installer, forbidden) {
			t.Fatalf("installer still contains permissive version contract %q", forbidden)
		}
	}
}

func TestTUIModuleRequiresPinnedGo(t *testing.T) {
	contents, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "\ngo "+pinnedGoVersion+"\n") {
		t.Fatalf("go.mod does not require exact Go %s", pinnedGoVersion)
	}
}

func runInstallerFunctions(t *testing.T, testRoot, fakeBin, dataDir, program string) string {
	t.Helper()
	return runInstallerFunctionsWithEnv(t, testRoot, fakeBin, dataDir, nil, program)
}

func runInstallerFunctionsWithEnv(
	t *testing.T,
	testRoot string,
	fakeBin string,
	dataDir string,
	extraEnvironment []string,
	program string,
) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	installerPath := filepath.Join(workingDirectory, "install.sh")
	temporaryDirectory := filepath.Join(testRoot, "tmp")
	if err := os.MkdirAll(temporaryDirectory, 0o700); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("/bin/bash", "-c", program, "installer-test", installerPath)
	command.Env = append([]string{
		"HOME=" + testRoot,
		"LC_ALL=C",
		"PATH=" + fakeBin + ":/usr/bin:/bin",
		"TMPDIR=" + temporaryDirectory,
		"LINUXCNCSETUP_DATA_DIR=" + dataDir,
		"LINUXCNCSETUP_CACHE_DIR=" + filepath.Join(testRoot, "cache"),
		"LINUXCNCSETUP_BIN_DIR=" + filepath.Join(testRoot, "user-bin"),
	}, extraEnvironment...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("installer function test failed: %v\n%s", err, output)
	}
	return string(output)
}

func writeInstallerExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

type offlineGoDownloadFixture struct {
	downloadLog  string
	checksumLog  string
	pinnedGoPath string
}

func installOfflineGoDownloadFixtures(t *testing.T, testRoot, fakeBin string) offlineGoDownloadFixture {
	t.Helper()
	fixture := offlineGoDownloadFixture{
		downloadLog:  filepath.Join(testRoot, "download.log"),
		checksumLog:  filepath.Join(testRoot, "checksum.log"),
		pinnedGoPath: filepath.Join(testRoot, "pinned-go"),
	}
	writeInstallerExecutable(t, fixture.pinnedGoPath, goVersionFixture(pinnedGoVersion))
	writeInstallerExecutable(t, filepath.Join(fakeBin, "uname"), `#!/bin/sh
case "${1:-}" in
-s) printf '%s\n' Linux ;;
-m) printf '%s\n' x86_64 ;;
*) exit 64 ;;
esac
`)
	writeInstallerExecutable(t, filepath.Join(fakeBin, "curl"), `#!/bin/sh
destination=
url=
while [ "$#" -gt 0 ]; do
	case "$1" in
	--output)
		shift
		destination=$1
		;;
	https://*)
		url=$1
		;;
	esac
	shift
done
printf '%s\n' "$url" > "$TEST_DOWNLOAD_LOG"
: > "$destination"
`)
	writeInstallerExecutable(t, filepath.Join(fakeBin, "sha256sum"), `#!/bin/sh
cat > "$TEST_CHECKSUM_LOG"
`)
	writeInstallerExecutable(t, filepath.Join(fakeBin, "tar"), `#!/bin/sh
destination=
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-C" ]; then
		shift
		destination=$1
	fi
	shift
done
mkdir -p "$destination/go/bin"
cp "$TEST_PINNED_GO_FIXTURE" "$destination/go/bin/go"
chmod 0755 "$destination/go/bin/go"
`)
	return fixture
}

func (fixture offlineGoDownloadFixture) environment() []string {
	return []string{
		"TEST_DOWNLOAD_LOG=" + fixture.downloadLog,
		"TEST_CHECKSUM_LOG=" + fixture.checksumLog,
		"TEST_PINNED_GO_FIXTURE=" + fixture.pinnedGoPath,
	}
}

func (fixture offlineGoDownloadFixture) assertAMD64Download(t *testing.T) {
	t.Helper()
	downloadedURL, err := os.ReadFile(fixture.downloadLog)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "https://go.dev/dl/go" + pinnedGoVersion + ".linux-amd64.tar.gz"
	if strings.TrimSpace(string(downloadedURL)) != wantURL {
		t.Fatalf("download URL = %q; want %q", strings.TrimSpace(string(downloadedURL)), wantURL)
	}
	checksumInput, err := os.ReadFile(fixture.checksumLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(checksumInput), pinnedGoAMD64+"  ") {
		t.Fatalf("checksum verification input does not use the pinned digest: %q", checksumInput)
	}
}

func goVersionFixture(version string) string {
	return "#!/bin/sh\n" +
		"if [ \"${1:-}\" = version ]; then\n" +
		"\tprintf 'go version go" + version + " linux/amd64\\n'\n" +
		"\texit 0\n" +
		"fi\n" +
		"exit 64\n"
}

func assertGoFixtureVersion(t *testing.T, path, version string) {
	t.Helper()
	output, err := exec.Command(path, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("run %s version: %v\n%s", path, err, output)
	}
	want := "go version go" + version + " linux/amd64"
	if strings.TrimSpace(string(output)) != want {
		t.Fatalf("%s version = %q; want %q", path, strings.TrimSpace(string(output)), want)
	}
}
