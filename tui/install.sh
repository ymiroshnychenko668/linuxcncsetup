#!/usr/bin/env bash

set -Eeuo pipefail

readonly APP_NAME="linuxcncsetup"
readonly MIN_GO_VERSION="1.25.0"
readonly GO_VERSION="1.26.5"

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DATA_DIR="${LINUXCNCSETUP_DATA_DIR:-${XDG_DATA_HOME:-${HOME}/.local/share}/${APP_NAME}}"
CACHE_DIR="${LINUXCNCSETUP_CACHE_DIR:-${XDG_CACHE_HOME:-${HOME}/.cache}/${APP_NAME}}"
BIN_DIR="${LINUXCNCSETUP_BIN_DIR:-${HOME}/.local/bin}"
TOOLCHAIN_DIR="${DATA_DIR}/toolchains/go${GO_VERSION}"
TARGET_BINARY="${BIN_DIR}/${APP_NAME}"

temporary_dir=""
temporary_binary=""
run_after_install=false
selected_go_binary=""

log() {
	printf '==> %s\n' "$*"
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<EOF
Usage: ./install.sh [--run]

Installs ${APP_NAME} for the current user.

Options:
  --run     Launch the TUI after installation
  -h        Show this help

Optional environment overrides:
  LINUXCNCSETUP_DATA_DIR   Go toolchain and module data directory
  LINUXCNCSETUP_CACHE_DIR  Go build cache directory
  LINUXCNCSETUP_BIN_DIR    Application binary directory
EOF
}

cleanup() {
	if [[ -n "${temporary_binary}" && -f "${temporary_binary}" ]]; then
		rm -f -- "${temporary_binary}"
	fi

	if [[ -n "${temporary_dir}" && -d "${temporary_dir}" ]]; then
		local temp_base="${TMPDIR:-/tmp}"
		case "${temporary_dir}" in
		"${temp_base}"/linuxcncsetup-install.*)
			chmod -R u+w "${temporary_dir}" 2>/dev/null || true
			rm -rf -- "${temporary_dir}"
			;;
		esac
	fi
}

trap cleanup EXIT

version_at_least() {
	local actual="$1"
	local required="$2"
	[[ "$(printf '%s\n%s\n' "${required}" "${actual}" | sort -V | head -n 1)" == "${required}" ]]
}

installed_go_version() {
	local go_binary="$1"
	"${go_binary}" version 2>/dev/null | awk '{sub(/^go/, "", $3); print $3}'
}

download() {
	local url="$1"
	local destination="$2"

	if command -v curl >/dev/null 2>&1; then
		curl --fail --location --proto '=https' --tlsv1.2 \
			--output "${destination}" "${url}"
	elif command -v wget >/dev/null 2>&1; then
		wget --https-only --output-document="${destination}" "${url}"
	else
		die "curl or wget is required to download Go"
	fi
}

install_local_go() {
	[[ "$(uname -s)" == "Linux" ]] || die "automatic Go installation currently supports Linux only"

	local go_arch
	local expected_sha256
	case "$(uname -m)" in
	x86_64 | amd64)
		go_arch="amd64"
		expected_sha256="5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053"
		;;
	aarch64 | arm64)
		go_arch="arm64"
		expected_sha256="fe4789e92b1f33358680864bbe8704289e7bb5fc207d80623c308935bd696d49"
		;;
	*)
		die "unsupported CPU architecture: $(uname -m)"
		;;
	esac

	command -v tar >/dev/null 2>&1 || die "tar is required to install Go"
	command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required to verify Go"

	local filename="go${GO_VERSION}.linux-${go_arch}.tar.gz"
	local url="https://go.dev/dl/${filename}"
	temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/linuxcncsetup-install.XXXXXX")"
	local archive="${temporary_dir}/${filename}"

	log "Downloading Go ${GO_VERSION} for linux/${go_arch}"
	download "${url}" "${archive}"

	printf '%s  %s\n' "${expected_sha256}" "${archive}" | sha256sum --check --status \
		|| die "Go archive checksum verification failed"
	log "Go archive checksum verified"

	tar -C "${temporary_dir}" -xzf "${archive}"
	[[ -x "${temporary_dir}/go/bin/go" ]] || die "downloaded Go archive is incomplete"

	mkdir -p "$(dirname -- "${TOOLCHAIN_DIR}")"
	if [[ -e "${TOOLCHAIN_DIR}" ]]; then
		local backup="${TOOLCHAIN_DIR}.incomplete.$(date +%Y%m%d%H%M%S)"
		mv -- "${TOOLCHAIN_DIR}" "${backup}"
		log "Moved incomplete toolchain to ${backup}"
	fi
	mv -- "${temporary_dir}/go" "${TOOLCHAIN_DIR}"

	local installed_version
	installed_version="$(installed_go_version "${TOOLCHAIN_DIR}/bin/go")"
	[[ "${installed_version}" == "${GO_VERSION}" ]] \
		|| die "installed Go version ${installed_version} does not match ${GO_VERSION}"
}

select_go() {
	local candidate
	local version

	if candidate="$(command -v go 2>/dev/null)"; then
		version="$(installed_go_version "${candidate}")"
		if [[ -n "${version}" ]] && version_at_least "${version}" "${MIN_GO_VERSION}"; then
			log "Using Go ${version} from ${candidate}"
			selected_go_binary="${candidate}"
			return
		fi
		log "Existing Go ${version:-unknown} is older than ${MIN_GO_VERSION}"
	fi

	if [[ -x "${TOOLCHAIN_DIR}/bin/go" ]]; then
		version="$(installed_go_version "${TOOLCHAIN_DIR}/bin/go")"
		if [[ -n "${version}" ]] && version_at_least "${version}" "${MIN_GO_VERSION}"; then
			log "Using local Go ${version}"
			selected_go_binary="${TOOLCHAIN_DIR}/bin/go"
			return
		fi
	fi

	install_local_go
	log "Installed Go ${GO_VERSION} in ${TOOLCHAIN_DIR}"
	selected_go_binary="${TOOLCHAIN_DIR}/bin/go"
}

main() {
	while (($#)); do
		case "$1" in
		--run)
			run_after_install=true
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			die "unknown option: $1"
			;;
		esac
		shift
	done

	((EUID != 0)) || die "run this installer as your normal user, not with sudo"

	select_go
	local go_binary="${selected_go_binary}"
	[[ -x "${go_binary}" ]] || die "no usable Go toolchain was selected"

	mkdir -p "${DATA_DIR}/gopath" "${CACHE_DIR}/go-build" "${BIN_DIR}"
	[[ ! -d "${TARGET_BINARY}" ]] || die "${TARGET_BINARY} is a directory"
	export GOPATH="${DATA_DIR}/gopath"
	export GOMODCACHE="${DATA_DIR}/gopath/pkg/mod"
	export GOCACHE="${CACHE_DIR}/go-build"
	export GOTOOLCHAIN=local

	log "Downloading Go module dependencies"
	"${go_binary}" -C "${SCRIPT_DIR}" mod download

	temporary_binary="$(mktemp "${BIN_DIR}/.${APP_NAME}.XXXXXX")"
	log "Building ${APP_NAME}"
	CGO_ENABLED=0 "${go_binary}" -C "${SCRIPT_DIR}" build \
		-trimpath \
		-ldflags="-s -w" \
		-o "${temporary_binary}" \
		./cmd/linuxcncsetup

	chmod 0755 "${temporary_binary}"
	mv -- "${temporary_binary}" "${TARGET_BINARY}"
	temporary_binary=""

	log "Installed ${TARGET_BINARY}"
	if [[ ":${PATH}:" != *":${BIN_DIR}:"* ]]; then
		printf '\nAdd this directory to PATH if needed:\n'
		printf '  export PATH="%s:$PATH"\n' "${BIN_DIR}"
	fi

	if [[ "${run_after_install}" == true ]]; then
		log "Launching ${APP_NAME}"
		cleanup
		trap - EXIT
		exec "${TARGET_BINARY}"
	fi

	printf '\nLaunch with:\n  %s\n' "${TARGET_BINARY}"
}

main "$@"
