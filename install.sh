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
#   KCAC_VERSION       version to install, e.g. v0.1.0   (default: latest)
#   KCAC_BINDIR        where to install                  (default: the first
#                                                         writable directory on
#                                                         your PATH, else
#                                                         ~/.local/bin)
#   KCAC_ADD_TO_PATH   1 or 0, to answer the PATH question without being asked.
#                      Unset means ask if there is a terminal, and do nothing if
#                      there is not, so this never hangs a CI job or an image
#                      build.
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

# shell_rc prints the startup file for the current shell, or nothing if it is
# not one we know how to edit safely.
shell_rc() {
	case "${SHELL:-}" in
	*/zsh) echo "${ZDOTDIR:-$HOME}/.zshrc" ;;
	*/bash)
		# macOS login shells read .bash_profile, most Linux ones .bashrc.
		if [ "$(uname -s)" = "Darwin" ]; then echo "${HOME}/.bash_profile"; else echo "${HOME}/.bashrc"; fi
		;;
	*/fish) echo "${HOME}/.config/fish/config.fish" ;;
	esac
}

# path_line prints the line that would put $2 on the PATH, in $1's syntax.
path_line() {
	case "${1##*/}" in
	config.fish) echo "fish_add_path $2" ;;
	*) echo "export PATH=\"$2:\$PATH\"" ;;
	esac
}

# add_to_path appends that line, once.
add_to_path() {
	rcfile="$1"
	dir="$2"

	mkdir -p "$(dirname "$rcfile")" 2>/dev/null || true

	# Re-running the installer must not stack up duplicate lines, so any
	# existing mention of the directory is treated as done.
	if [ -f "$rcfile" ] && grep -Fq "$dir" "$rcfile" 2>/dev/null; then
		echo "kcac install: ${rcfile} already refers to ${dir}, left unchanged"
		return 0
	fi

	{
		echo ""
		echo "# Added by the kcac installer. Safe to delete."
		path_line "$rcfile" "$dir"
	} >>"$rcfile" 2>/dev/null || return 1

	echo "kcac install: added two lines to ${rcfile}"
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
echo "kcac install: it only ever reads from Keycloak"
echo
"${bindir}/${BIN}" version || true

# The PATH notice goes last, on purpose: it is the one thing the reader has to
# act on, and above the version output it just scrolls away.
if on_path "$bindir"; then
	echo
	echo "  Ready. Try: kcac --help"
	exit 0
fi

rc="$(shell_rc)"

echo
echo "  ${bindir} is not on your PATH, so 'kcac' will not be found yet."

# Decide whether to edit the shell startup file.
#
# Asking rather than assuming: this script is piped from the internet, and
# writing to somebody's dotfiles uninvited is not a thing a tool that reads your
# whole user directory should do. KCAC_ADD_TO_PATH answers in advance for
# unattended installs, and with no answer and no terminal nothing is written, so
# a CI job or an image build neither hangs nor gets edited behind its back.
decision=""
case "${KCAC_ADD_TO_PATH:-}" in
1 | y | Y | yes | YES | true) decision="yes" ;;
0 | n | N | no | NO | false) decision="no" ;;
*)
	if [ -n "$rc" ] && [ -r /dev/tty ]; then
		echo
		printf '  Add it to %s? [y/N]: ' "$rc"
		read -r reply </dev/tty || reply=""
		case "$reply" in
		y | Y | yes | YES) decision="yes" ;;
		*) decision="no" ;;
		esac
	else
		decision="no"
	fi
	;;
esac

if [ "$decision" = "yes" ] && [ -n "$rc" ] && add_to_path "$rc" "$bindir"; then
	echo
	echo "  ONE STEP LEFT. That file is only read when a shell starts, so the"
	echo "  shell you are in now still cannot see kcac. Run:"
	echo
	if [ "${rc##*/}" = "config.fish" ]; then
		echo "      exec fish"
	else
		echo "      source ${rc}"
	fi
	echo
	echo "  Then: kcac --help"
	exit 0
fi

# Nothing was written, so say exactly what to do by hand.
echo
echo "  Add it yourself with:"
echo
if [ "${rc##*/}" = "config.fish" ]; then
	echo "    fish_add_path ${bindir}"
elif [ -n "$rc" ]; then
	echo "    echo '$(path_line "$rc" "$bindir")' >> ${rc}"
	echo "    source ${rc}"
else
	echo "    export PATH=\"${bindir}:\$PATH\""
	echo "    (put that in your shell's startup file to make it stick)"
fi
echo
echo "  Or reinstall straight into a directory that is already on your PATH:"
echo
echo "      curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sudo KCAC_BINDIR=/usr/local/bin sh"
