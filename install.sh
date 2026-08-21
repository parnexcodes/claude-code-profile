#!/usr/bin/env bash
#
# ccp installer.
#
# One-liner:
#   curl -fsSL https://raw.githubusercontent.com/parnexcodes/claude-code-profile/master/install.sh | bash
#
# Or from a clone of this repo:
#   ./install.sh
#
# Environment overrides:
#   CCP_BINDIR   where to put the binary   (default: ~/.local/bin)
#   REF          branch or tag to install  (default: master)

set -euo pipefail

REPO="parnexcodes/claude-code-profile"
REF="${REF:-master}"
BINDIR="${CCP_BINDIR:-${HOME}/.local/bin}"
MIN_GO_MAJOR=1
MIN_GO_MINOR=25

log() { printf '[ccp] %s\n' "$*" >&2; }
die() { printf '[ccp] error: %s\n' "$*" >&2; exit 1; }

WORK=""
trap 'rm -rf "$WORK"' EXIT

fetch() { # fetch <url> -> stdout
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO- "$1"
	else
		die "neither curl nor wget found; install one and retry"
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

main() {
	local srcdir scriptdir
	WORK="$(mktemp -d)"

	# If this script lives next to go.mod, build the checkout directly.
	srcdir=""
	scriptdir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
	if [ -n "$scriptdir" ] && [ -f "${scriptdir}/go.mod" ] \
		&& grep -q '^module ccp$' "${scriptdir}/go.mod"; then
		log "building from local checkout: ${scriptdir}"
		srcdir="$scriptdir"
	else
		log "checking Go toolchain (>= ${MIN_GO_MAJOR}.${MIN_GO_MINOR} required)"
		go_version_ok || die "Go >= ${MIN_GO_MAJOR}.${MIN_GO_MINOR} is required; get it from https://go.dev/dl/"

		log "downloading source (${REPO}@${REF})"
		fetch "https://github.com/${REPO}/archive/${REF}.tar.gz" > "${WORK}/src.tar.gz"
		mkdir -p "${WORK}/src"
		tar -xzf "${WORK}/src.tar.gz" -C "${WORK}/src" --strip-components=1
		srcdir="${WORK}/src"
	fi

	log "compiling"
	(cd "$srcdir" && go build -o "${WORK}/ccp" .) || die "go build failed"

	mkdir -p "$BINDIR"
	install -m 0755 "${WORK}/ccp" "${BINDIR}/ccp"
	log "installed ${BINDIR}/ccp"

	case ":${PATH}:" in
	*":${BINDIR}:"*) ;;
	*) log "note: ${BINDIR} is not on your PATH; add 'export PATH=${BINDIR}:\$PATH' to your shell rc" ;;
	esac

	log "done. next steps:"
	log "  ccp list            # see profiles (glm/kimi seeds created on first run)"
	log "  ccp proxy install   # optional: CLIProxyAPI for OpenAI-style models"
	log "  ccp completion zsh >> ~/.zshrc   # or: ccp completion bash >> ~/.bashrc"
}

main "$@"
