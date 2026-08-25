#!/usr/bin/env bash
#
# ccp installer.
#
# One-liner:
#   curl -fsSL https://raw.githubusercontent.com/parnexcodes/claude-code-profile/master/install.sh | bash
#
# Prefers prebuilt release binaries (no Go needed); falls back to compiling
# from source when there is no matching release, or when run from a clone.
#
# Environment overrides:
#   CCP_BINDIR       where to put the binary     (default: ~/.local/bin)
#   REF              branch/tag to build         (default: master; non-master skips releases)
#   CCP_RELEASE_API  override releases API URL   (for testing)
#   CCP_RELEASE_DL   override releases DL base   (for testing)

set -euo pipefail

REPO="parnexcodes/claude-code-profile"
REF="${REF:-master}"
BINDIR="${CCP_BINDIR:-${HOME}/.local/bin}"
API_URL="${CCP_RELEASE_API:-https://api.github.com/repos/${REPO}/releases/latest}"
DL_URL="${CCP_RELEASE_DL:-https://github.com/${REPO}/releases/download}"
MIN_GO_MAJOR=1
MIN_GO_MINOR=25

WORK=""
EXT=""    # ".exe" on windows
OS=""
ARCH=""

log() { printf '[ccp] %s\n' "$*" >&2; }
die() { printf '[ccp] error: %s\n' "$*" >&2; exit 1; }

trap 'rm -rf "$WORK"' EXIT

fetch() { # url -> stdout
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO- "$1"
	else
		die "neither curl nor wget found; install one and retry"
	fi
}

fetch_to() { # url -> file
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	else
		wget -qO "$2" "$1"
	fi
}

detect_platform() { # sets OS, ARCH, EXT; fails on unsupported combos
	local s m
	s="$(uname -s)"
	m="$(uname -m)"
	case "$s" in
		Linux) OS=linux ;;
		Darwin) OS=darwin ;;
		MINGW* | MSYS* | CYGWIN*) OS=windows ;;
		*) return 1 ;;
	esac
	case "$m" in
		x86_64 | amd64) ARCH=amd64 ;;
		aarch64 | arm64) ARCH=arm64 ;;
		*) return 1 ;;
	esac
	[ "$OS" = "windows" ] && EXT=".exe"
	return 0
}

sha256_of() { # file -> hex digest
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 -r "$1" | cut -d' ' -f1
	else
		return 1
	fi
}

go_version_ok() {
	command -v go >/dev/null 2>&1 || return 1
	local v major minor
	v="$(go env GOVERSION 2>/dev/null | sed 's/^go//')" || return 1
	major="$(printf '%s' "$v" | cut -d. -f1)"
	minor="$(printf '%s' "$v" | cut -d. -f2)"
	[ "${major:-0}" -gt "$MIN_GO_MAJOR" ] && return 0
	[ "${major:-0}" -eq "$MIN_GO_MAJOR" ] && [ "${minor:-0}" -ge "$MIN_GO_MINOR" ]
}

require_go() {
	go_version_ok || die "Go >= ${MIN_GO_MAJOR}.${MIN_GO_MINOR} is required; get it from https://go.dev/dl/"
}

build_source() { # <srcdir>
	require_go
	log "compiling"
	(cd "$1" && go build -o "${WORK}/ccp_bin${EXT}" ./cmd/ccp) || die "go build failed"
}

download_source() { # <ref> : unpack branch/tag tarball into WORK/src
	log "downloading source (${REPO}@${1})"
	fetch_to "https://github.com/${REPO}/archive/${1}.tar.gz" "${WORK}/src.tar.gz"
	mkdir -p "${WORK}/src"
	tar -xzf "${WORK}/src.tar.gz" -C "${WORK}/src" --strip-components=1
	echo "${WORK}/src"
}

try_release_download() { # -> 0 and binary at WORK/ccp_bin$EXT on success
	detect_platform || {
		log "platform $(uname -s)/$(uname -m) has no prebuilt binaries; will build from source"
		return 1
	}
	local tag asset expected actual
	if ! tag="$(fetch "$API_URL" 2>/dev/null |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
		head -n1)"; then
		log "cannot reach the releases API; will build from source"
		return 1
	fi
	[ -n "$tag" ] || {
		log "no GitHub release found yet; will build from source"
		return 1
	}

	asset="ccp_${tag}_${OS}_${ARCH}.tar.gz"
	log "fetching release ${tag} (${asset})"
	if ! fetch_to "${DL_URL}/${tag}/checksums.txt" "${WORK}/checksums.txt" 2>/dev/null; then
		log "release has no checksums.txt; will build from source"
		return 1
	fi
	if ! fetch_to "${DL_URL}/${tag}/${asset}" "${WORK}/${asset}" 2>/dev/null; then
		log "no asset for this platform; will build from source"
		return 1
	fi

	expected="$(grep " ${asset}\$" "${WORK}/checksums.txt" | head -n1 | cut -d' ' -f1)"
	if actual="$(sha256_of "${WORK}/${asset}")" && [ -n "$expected" ] && [ "$actual" != "$expected" ]; then
		die "checksum mismatch for ${asset} (got ${actual:-none}, want ${expected})"
	fi

	mkdir -p "${WORK}/release"
	if [ "$OS" = "windows" ]; then
		command -v unzip >/dev/null 2>&1 || { log "unzip not found; will build from source"; return 1; }
		unzip -q "${WORK}/${asset}" -d "${WORK}/release-unz" || { log "unzip failed"; return 1; }
		mv "${WORK}/release-unz"/*/* "${WORK}/release/" || { log "unexpected archive layout"; return 1; }
	else
		tar -xzf "${WORK}/${asset}" -C "${WORK}/release" --strip-components=1 \
			|| { log "extraction failed"; return 1; }
	fi
	mv "${WORK}/release/ccp${EXT}" "${WORK}/ccp_bin${EXT}" || { log "binary missing from archive"; return 1; }
	log "using prebuilt release ${tag}"
	return 0
}

main() {
	WORK="$(mktemp -d)"

	# Running from a clone? Build that checkout directly.
	local srcdir="" scriptdir
	scriptdir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
	if [ -n "$scriptdir" ] && [ -f "${scriptdir}/go.mod" ] \
		&& grep -q '^module ccp$' "${scriptdir}/go.mod"; then
		log "building from local checkout: ${scriptdir}"
		srcdir="$scriptdir"
	fi

	if [ -n "$srcdir" ]; then
		build_source "$srcdir"
	elif [ "$REF" != "master" ] || ! try_release_download; then
		srcdir="$(download_source "$REF")"
		build_source "$srcdir"
	fi

	mkdir -p "$BINDIR"
	install -m 0755 "${WORK}/ccp_bin${EXT}" "${BINDIR}/ccp${EXT}"
	log "installed ${BINDIR}/ccp${EXT}"

	case ":${PATH}:" in
	*":${BINDIR}:"*) ;;
	*) log "note: ${BINDIR} is not on your PATH; add 'export PATH=${BINDIR}:\$PATH' to your shell rc" ;;
	esac

	log "done. next steps:"
	log "  ccp add myprofile --type anthropic   # create your first profile"
	log "  ccp proxy install   # optional: CLIProxyAPI for OpenAI-style models"
	log "  ccp completion zsh >> ~/.zshrc   # or: ccp completion bash >> ~/.bashrc"
}

main "$@"
