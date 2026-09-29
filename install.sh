#!/bin/sh
# Install kcac, the Keycloak effective-access exporter.
#
#   curl -fsSL https://raw.githubusercontent.com/softika/kcac/main/install.sh | sh
#
# Piping a script from the internet into a shell deserves a second of thought,
# so this one is kept short enough to read first:
#
#   curl -fsSL https://raw.githubusercontent.com/softika/kcac/main/install.sh | less
#
# It downloads one release archive over HTTPS, checks it against the published
# SHA-256 checksums, and copies a single binary into place. It uses no sudo,
# writes nothing else, and touches no shell profile.
#
# Environment:
#   KCAC_VERSION   version to install, e.g. v0.1.0   (default: latest)
#   KCAC_BINDIR    where to install                  (default: /usr/local/bin,
#                                                     or ~/.local/bin if that is
#                                                     not writable)
set -eu

REPO="softika/kcac"
BIN="kcac"
BASE_URL="${KCAC_BASE_URL:-https://github.com/${REPO}/releases}"

die() {
	echo "kcac install: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"
}

need curl
need tar

# ---- platform ---------------------------------------------------------------
# The release archives are named after uname output, so this is mostly a
# passthrough. Linux reports aarch64 where Go and macOS both say arm64.
os="$(uname -s)"
arch="$(uname -m)"

case "$os" in
Linux | Darwin) ;;
*) die "unsupported operating system '$os'. Windows builds are on the releases page: ${BASE_URL}" ;;
esac

case "$arch" in
x86_64 | amd64) arch="x86_64" ;;
aarch64 | arm64) arch="arm64" ;;
*) die "unsupported architecture '$arch'. Build from source: go install github.com/${REPO}/cmd/kcac@latest" ;;
esac

# ---- version ----------------------------------------------------------------
version="${KCAC_VERSION:-latest}"
if [ "$version" = "latest" ]; then
	# Follow the /releases/latest redirect rather than calling the API, which
	# rate-limits unauthenticated callers to 60 requests an hour.
	version="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "${BASE_URL}/latest" | sed 's#.*/tag/##')"
	[ -n "$version" ] || die "could not work out the latest version. Set KCAC_VERSION to install a specific one."
fi

archive="${BIN}_${os}_${arch}.tar.gz"
url="${BASE_URL}/download/${version}/${archive}"

# ---- download and verify ----------------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "kcac install: downloading ${version} for ${os}/${arch}"
curl -fsSL "$url" -o "${tmp}/${archive}" ||
	die "could not download ${url}. Check that ${version} exists on the releases page."
curl -fsSL "${BASE_URL}/download/${version}/checksums.txt" -o "${tmp}/checksums.txt" ||
	die "could not download checksums for ${version}"

expected="$(grep " ${archive}\$" "${tmp}/checksums.txt" | awk '{print $1}')"
[ -n "$expected" ] || die "${archive} is not listed in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
	actual="$(sha256sum "${tmp}/${archive}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
	actual="$(shasum -a 256 "${tmp}/${archive}" | awk '{print $1}')"
else
	die "need sha256sum or shasum to verify the download"
fi

[ "$actual" = "$expected" ] ||
	die "checksum mismatch for ${archive}. Expected ${expected}, got ${actual}. Do not use this download."

echo "kcac install: checksum verified"

# ---- install ----------------------------------------------------------------
tar -xzf "${tmp}/${archive}" -C "$tmp" "$BIN" || die "could not extract ${BIN} from ${archive}"

on_path() {
	case ":${PATH}:" in
	*":$1:"*) return 0 ;;
	*) return 1 ;;
	esac
}

writable() {
	# A directory that does not exist yet counts as writable if it can be
	# created, so walk up to the first ancestor that does exist. Checking only
	# the immediate parent would reject ~/.local/bin on a machine where ~/.local
	# is also absent, even though mkdir -p handles both.
	d="$1"
	while [ ! -e "$d" ]; do
		parent="$(dirname "$d")"
		[ "$parent" = "$d" ] && return 1
		d="$parent"
	done
	[ -d "$d" ] && [ -w "$d" ]
}

bindir="${KCAC_BINDIR:-}"
if [ -z "$bindir" ]; then
	# No sudo: a script piped from the internet should not ask for a password.
	#
	# Prefer somewhere that is both writable AND already on PATH, so the very
	# next command works. On Apple Silicon /usr/local/bin is on PATH but root
	# owned, and ~/.local/bin is writable but usually absent from PATH, so
	# picking on writability alone installs a binary the shell cannot find.
	for candidate in /usr/local/bin "${HOME}/.local/bin" "${HOME}/bin"; do
		if writable "$candidate" && on_path "$candidate"; then
			bindir="$candidate"
			break
		fi
	done
fi
if [ -z "$bindir" ]; then
	# Nothing on PATH is writable. Install anyway and say clearly what to do.
	for candidate in "${HOME}/.local/bin" "${HOME}/bin" /usr/local/bin; do
		if writable "$candidate"; then
			bindir="$candidate"
			break
		fi
	done
fi
[ -n "$bindir" ] || die "found nowhere writable to install into. Set KCAC_BINDIR."

mkdir -p "$bindir" || die "could not create ${bindir}"
install -m 0755 "${tmp}/${BIN}" "${bindir}/${BIN}" 2>/dev/null ||
	{ cp "${tmp}/${BIN}" "${bindir}/${BIN}" && chmod 0755 "${bindir}/${BIN}"; } ||
	die "could not write to ${bindir}. Set KCAC_BINDIR to somewhere you can write."

echo "kcac install: installed ${bindir}/${BIN}"
echo
"${bindir}/${BIN}" version || true

# The PATH notice goes last, on purpose: it is the one thing the reader has to
# act on, and above the version output it just scrolls away.
if on_path "$bindir"; then
	echo
	echo "  Next: kcac --help"
	echo "  kcac only ever reads from Keycloak."
	exit 0
fi

rc=""
case "${SHELL:-}" in
*/zsh) rc="${ZDOTDIR:-$HOME}/.zshrc" ;;
*/bash) [ "$(uname -s)" = "Darwin" ] && rc="${HOME}/.bash_profile" || rc="${HOME}/.bashrc" ;;
*/fish) rc="${HOME}/.config/fish/config.fish" ;;
esac

echo
echo "  ACTION NEEDED: ${bindir} is not on your PATH, so 'kcac' will not be found yet."
echo
if [ "${rc##*/}" = "config.fish" ]; then
	echo "    fish_add_path ${bindir}"
elif [ -n "$rc" ]; then
	echo "    echo 'export PATH=\"${bindir}:\$PATH\"' >> ${rc}"
	echo "    source ${rc}"
else
	echo "    export PATH=\"${bindir}:\$PATH\""
	echo "    (add that line to your shell's startup file to make it stick)"
fi
echo
echo "  Or install somewhere already on your PATH instead:"
echo "    curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sudo KCAC_BINDIR=/usr/local/bin sh"
echo
echo "  Then: kcac --help"
echo "  kcac only ever reads from Keycloak."
