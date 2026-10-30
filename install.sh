#!/bin/sh
# install.sh builds and installs ektara from source.
#
# It is meant to be fetched and run in one line:
#
#   curl -fsSL https://raw.githubusercontent.com/korrykatti/ektara/main/install.sh | sh
#
# It checks for the things a build needs, says how to get anything that is
# missing, then builds ektara and puts it in ~/.local/bin. It does not touch the
# system unless --install-deps is passed, which installs missing packages.

set -eu

install_deps=0
for arg in "$@"; do
	case $arg in
	--install-deps) install_deps=1 ;;
	*)
		printf 'unknown option: %s\n' "$arg" >&2
		exit 2
		;;
	esac
done

# The wordmark, in colour. printf %b is what makes the escapes work in POSIX
# sh; when the output is not a terminal, fall back to the name alone.
logo() {
	if [ ! -t 1 ]; then
		printf '\nektara\n\n'
		return
	fi
	printf '%b\n' "$(cat <<'EOF'
\033[0;38;2;170;170;170m               \033[0;38;2;0;0;170m▄▄▄\033[0;38;2;0;0;170;48;2;170;170;170m▀▀▀\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m         \033[0;38;2;0;0;170m▄▄▄\033[0;38;2;0;0;170;48;2;170;170;170m▀▀▀\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m                                               \033[0m
\033[0;38;2;170;170;170m                \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m           \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m                                                \033[0m
\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m▄\033[0;38;2;0;0;170;48;2;170;170;170m▀▀\033[0;38;2;170;170;170;48;2;0;0;170m█▀▀█\033[0;38;2;0;0;170;48;2;170;170;170m▀\033[0;38;2;170;170;170;48;2;0;0;170m▄\033[0;38;2;0;0;170m▄\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m ░ \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m▄▄\033[0;38;2;170;170;170;48;2;0;0;170m▄▄\033[0;38;2;0;0;170m▄\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m ░ \033[0;38;2;0;0;170m█▄\033[0;38;2;170;170;170;48;2;0;0;170m▄\033[0;38;2;0;0;170m▄\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m▄\033[0;38;2;0;0;170;48;2;170;170;170m▀▀\033[0;38;2;170;170;170m▒▀▀▄\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m▄▄\033[0;38;2;0;0;170m▄▄▄\033[0;38;2;170;170;170m   \033[0;38;2;0;0;0;48;2;0;0;170m▀\033[0;38;2;170;170;170;48;2;0;0;170m▄▄▄▄▄▄▄\033[0;38;2;0;0;0;48;2;0;0;170m▀\033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m▄\033[0;38;2;0;0;170;48;2;170;170;170m▀▀\033[0;38;2;170;170;170m▒▀▀▄\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m▄▄\033[0;38;2;0;0;170m▄▄▄\033[0m
\033[0;38;2;170;170;170m \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m ░\033[0;38;2;170;170;170;48;2;0;0;170m▒\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m  \033[0;38;2;170;170;170;48;2;0;0;170m▒█▓█\033[0;38;2;170;0;170;48;2;0;0;170m░\033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m░▒░\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m▄\033[0;38;2;170;170;170;48;2;0;0;170m▄▓▀\033[0;38;2;0;0;170m▀▀\033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m░▒░\033[0;38;2;170;170;170;48;2;0;0;170m▀▓█▀\033[0;38;2;0;0;170m▀\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m ░ \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m   ░\033[0;38;2;170;170;170;48;2;0;0;170m█▓█\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m█▓█\033[0;38;2;0;0;170m█▀█\033[0;38;2;170;170;170;48;2;0;0;170m█▓█\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m ░ \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m   ░\033[0;38;2;170;170;170;48;2;0;0;170m█▓█\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m \033[0m
\033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▒░▒\033[0;38;2;170;170;170;48;2;0;0;170m░\033[0;38;2;170;170;170m   \033[0;38;2;170;170;170;48;2;0;0;170m░▓▀▀\033[0;38;2;0;0;170m▀\033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▒▓▒\033[0;38;2;170;170;170m▄▀▀▓██\033[0;38;2;170;170;170;48;2;0;0;170m▄\033[0;38;2;0;0;170m■▄\033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▒▓▒\033[0;38;2;0;0;170m██\033[0;38;2;170;170;170;48;2;0;0;170m░\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m░▒░\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m▓▒▓\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m▓▒▓ \033[0;38;2;0;0;170m▌\033[0;38;2;170;170;170m \033[0;38;2;0;0;170m▐\033[0;38;2;170;170;170;48;2;0;0;170m ▓▒\033[0;38;2;170;170;170m \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m░▒░\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m▓▒▓\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m \033[0m
\033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▒▓▒\033[0;38;2;0;0;170m█▀■▀▀▀\033[0;38;2;170;170;170m      \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▓█▓\033[0;38;2;170;0;170;48;2;0;0;170m░\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m▐\033[0;38;2;170;170;170;48;2;0;0;170m▐\033[0;38;2;0;0;170;48;2;170;170;170m░\033[0;38;2;170;170;170;48;2;0;0;170m▓█▌\033[0;38;2;0;0;170m▌\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▓█▓\033[0;38;2;170;0;170;48;2;0;0;170m░\033[0;38;2;0;0;170m▒▀\033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▒▓▒\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m▒░▒\033[0;38;2;170;0;170;48;2;0;0;170m░\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m▒░▒\033[0;38;2;0;0;170m▓\033[0;38;2;170;170;170m       \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▒▓▒\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m▒░▒\033[0;38;2;170;0;170;48;2;0;0;170m░\033[0;38;2;170;170;170m \033[0m
\033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▓█▓\033[0;38;2;170;0;170;48;2;0;0;170m░\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m▄▄■▄\033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;0;0;170m \033[0;38;2;85;85;255;48;2;0;0;170m░ \033[0;38;2;170;0;170;48;2;0;0;170m▒\033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m▓▒▓█\033[0;38;2;170;0;170;48;2;0;0;170m░\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;0;0;170m \033[0;38;2;85;85;255;48;2;0;0;170m░ \033[0;38;2;170;0;170;48;2;0;0;170m▒\033[0;38;2;0;0;170m░\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▓█▓\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m░ ░\033[0;38;2;170;0;170;48;2;0;0;170m▒\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m░ ░\033[0;38;2;0;0;170m▓\033[0;38;2;170;170;170m       \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;170;170m▓█▓\033[0;38;2;0;0;170m█\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m░ ░\033[0;38;2;170;0;170;48;2;0;0;170m▒\033[0;38;2;170;170;170m \033[0m
\033[0;38;2;170;170;170m \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;0;0;170m \033[0;38;2;85;85;255;48;2;0;0;170m░\033[0;38;2;170;0;170;48;2;0;0;170m▒\033[0;38;2;0;0;170m▄\033[0;38;2;170;170;170m \033[0;38;2;0;0;170m▄\033[0;38;2;85;85;255;48;2;0;0;170m▒░░▒\033[0;38;2;170;0;170;48;2;0;0;170m▓\033[0;38;2;170;0;170m▌\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;85;85;255;48;2;0;0;170m░▒░\033[0;38;2;170;0;170;48;2;0;0;170m▓\033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;170;170;170;48;2;0;0;170m▒░░▒\033[0;38;2;170;0;170;48;2;0;0;170m▒\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;85;85;255;48;2;0;0;170m░▒░\033[0;38;2;170;0;170;48;2;0;0;170m▓\033[0;38;2;0;0;170m▓\033[0;38;2;170;0;170;48;2;0;0;170m▄■▄\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m█\033[0;38;2;85;85;255;48;2;0;0;170m░▒░ \033[0;38;2;0;0;170m▄\033[0;38;2;170;170;170m \033[0;38;2;170;0;170m▄\033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;0;0;170m \033[0;38;2;85;85;255;48;2;0;0;170m░ \033[0;38;2;170;0;170;48;2;0;0;170m▓\033[0;38;2;170;170;170m  \033[0;38;2;0;0;170m██\033[0;38;2;0;0;170;48;2;0;0;170m \033[0;38;2;85;85;255;48;2;0;0;170m░░\033[0;38;2;170;170;170m        \033[0;38;2;0;0;170m█\033[0;38;2;85;85;255;48;2;0;0;170m░▒░ \033[0;38;2;0;0;170m▄\033[0;38;2;170;170;170m \033[0;38;2;170;0;170m▄\033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;0;0;170m \033[0;38;2;85;85;255;48;2;0;0;170m░ \033[0;38;2;170;0;170;48;2;0;0;170m▓\033[0;38;2;170;170;170m \033[0m
\033[0;38;2;170;170;170m   \033[0;38;2;170;0;170m▀\033[0;38;2;170;0;170;48;2;0;0;170m▄▄▄▄\033[0;38;2;170;0;170m▀▀▀▀\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m█\033[0;38;2;0;0;170;48;2;170;0;170m▀▀▀ \033[0;38;2;170;170;170m   \033[0;38;2;0;0;170m█\033[0;38;2;85;85;255;48;2;0;0;170m░\033[0;38;2;170;0;170;48;2;0;0;170m▄▄▄▓\033[0;38;2;170;170;170m  \033[0;38;2;170;0;170;48;2;0;0;170m■\033[0;38;2;0;0;170;48;2;170;0;170m▀▀▀\033[0;38;2;170;0;170;48;2;0;0;170m▄▀\033[0;38;2;0;0;170m▀▀▀\033[0;38;2;170;170;170m    \033[0;38;2;0;0;170m▀\033[0;38;2;170;0;170m▀▀▀▀\033[0;38;2;170;170;170m  \033[0;38;2;170;0;170m▀■\033[0;38;2;170;0;170;48;2;0;0;170m▄\033[0;38;2;170;0;170m█▄\033[0;38;2;170;170;170m \033[0;38;2;170;0;170;48;2;0;0;170m▄▄\033[0;38;2;170;0;170m▀\033[0;38;2;170;0;170;48;2;0;0;170m▄▄\033[0;38;2;170;170;170m          \033[0;38;2;0;0;170m▀\033[0;38;2;170;0;170m▀▀▀▀\033[0;38;2;170;170;170m  \033[0;38;2;170;0;170m▀■\033[0;38;2;170;0;170;48;2;0;0;170m▄\033[0;38;2;170;0;170m█▄\033[0m
\033[0;38;2;170;170;170m               \033[0;38;2;0;0;170m▀▀\033[0;38;2;170;170;170m     \033[0;38;2;0;0;170m■▀\033[0;38;2;170;0;170m▀▀\033[0;38;2;170;170;170m     \033[0;38;2;0;0;170m▀▀\033[0;38;2;170;170;170m                                                    \033[0m
EOF
)"
}
logo

repo=korrykatti/ektara
branch=main
bindir=${BINDIR:-$HOME/.local/bin}

tmpdir=""
tmpbin=""
cleanup() {
	if [ -n "$tmpdir" ]; then rm -rf "$tmpdir"; fi
	if [ -n "$tmpbin" ]; then rm -f "$tmpbin"; fi
}
trap cleanup EXIT

have() { command -v "$1" >/dev/null 2>&1; }

# Detect the package manager with command -v, not by guessing the distro: a
# container or a minimal install may not have the tool the distro is known for.
manager=""
for candidate in pacman apt-get dnf zypper apk; do
	if have "$candidate"; then
		manager=$candidate
		break
	fi
done

# The package that provides each dependency. Go, the C compiler and libmpv are
# named differently across the managers we know; ffmpeg is the same.
package_name() {
	case "$1" in
	go)
		case $manager in
		apt-get) printf 'golang-go' ;;
		dnf) printf 'golang' ;;
		*) printf 'go' ;;
		esac
		;;
	cc)
		case $manager in
		apt-get) printf 'build-essential' ;;
		apk) printf 'build-base' ;;
		*) printf 'gcc' ;;
		esac
		;;
	mpv)
		case $manager in
		apt-get | zypper) printf 'libmpv2' ;;
		dnf | apk) printf 'mpv-libs' ;;
		*) printf 'mpv' ;;
		esac
		;;
	ffmpeg) printf 'ffmpeg' ;;
	curl) printf 'curl' ;;
	*) return 1 ;;
	esac
}

# Root needs no prefix, and some systems have doas instead of sudo.
privilege() {
	if [ "$(id -u)" -eq 0 ]; then
		return 0
	elif have sudo; then
		printf 'sudo '
	elif have doas; then
		printf 'doas '
	fi
	return 0
}

# The command a reader can paste, or nothing when the manager is unknown.
install_hint() {
	pkg=$(package_name "$1") || return 0
	if [ -z "$pkg" ]; then
		return 0
	fi
	prefix=$(privilege)
	case $manager in
	pacman) printf '%spacman -S --needed %s' "$prefix" "$pkg" ;;
	apt-get) printf '%sapt install %s' "$prefix" "$pkg" ;;
	dnf) printf '%sdnf install %s' "$prefix" "$pkg" ;;
	zypper) printf '%szypper install %s' "$prefix" "$pkg" ;;
	apk) printf '%sapk add %s' "$prefix" "$pkg" ;;
	*) return 0 ;;
	esac
}

# One line per missing thing. The reader is in the middle of a pipe, so the
# hint has to be something they can paste, not a paragraph.
missing=0
needed=""
need() {
	printf 'ektara needs %s.\n' "$1" >&2
	if [ -n "${2:-}" ]; then
		printf '  %s\n' "$2" >&2
	fi
	if [ -n "${3:-}" ]; then
		printf '  %s\n' "$3" >&2
	fi
	missing=1
}

need_install() {
	needed="$needed $2"
	hint=$(install_hint "$2")
	if [ -n "$hint" ]; then
		need "$1" "install it with: $hint" "${3:-}"
	else
		need "$1" "${3:-}"
	fi
}

# Run a command as root without touching stdin: sudo and doas read a password
# from the terminal, not from the script arriving on the pipe.
run_privileged() {
	if [ "$(id -u)" -eq 0 ]; then
		"$@"
	elif have sudo; then
		sudo "$@"
	elif have doas; then
		doas "$@"
	else
		printf 'ektara needs root to install packages, but sudo and doas are missing.\n' >&2
		return 1
	fi
}

install_missing_deps() {
	packages=""
	for dep in $needed; do
		if [ "$dep" = go ]; then
			printf 'Go 1.27+ is not installed by --install-deps; get it from https://go.dev/dl/ first.\n' >&2
			return 1
		fi
		pkg=$(package_name "$dep") || continue
		case " $packages " in
		*" $pkg "*) ;;
		*) packages="$packages $pkg" ;;
		esac
	done
	packages=${packages# }
	if [ -z "$packages" ]; then
		return 0
	fi

	case $manager in
	pacman) set -- pacman -S --needed --noconfirm $packages ;;
	apt-get)
		printf 'refreshing package lists...\n'
		printf '  %sapt-get update\n' "$(privilege)"
		run_privileged apt-get update || return 1
		set -- apt-get install -y $packages
		;;
	dnf) set -- dnf install -y $packages ;;
	zypper) set -- zypper --non-interactive install $packages ;;
	apk) set -- apk add $packages ;;
	*)
		printf 'no supported package manager found; install these yourself: %s\n' "$packages" >&2
		return 1
		;;
	esac

	printf 'installing: %s\n' "$packages"
	printf '  %s%s\n' "$(privilege)" "$*"
	run_privileged "$@"
}

# The build uses -tags nocgo, which opens libmpv when the program starts rather
# than linking it, so only the shared library is needed and not the -dev
# headers. ldconfig knows the path on glibc; Alpine has no ldconfig, so fall
# back to the usual library directories.
libmpv_path() {
	lib=$(ldconfig -p 2>/dev/null | awk '/libmpv\.so/ { print $NF; exit }')
	if [ -n "$lib" ] && [ -e "$lib" ]; then
		printf '%s\n' "$lib"
		return 0
	fi
	for lib in /usr/lib/libmpv.so* /usr/lib64/libmpv.so* /lib/libmpv.so* \
		/lib64/libmpv.so* /usr/lib/*-linux-gnu*/libmpv.so*; do
		if [ -e "$lib" ]; then
			printf '%s\n' "$lib"
			return 0
		fi
	done
	return 1
}

mpv_present() {
	libmpv_path >/dev/null 2>&1 || pkg-config --exists mpv 2>/dev/null
}

# go-mpv 0.4 asks for mpv_get_time_ns, which arrived in mpv 0.37. Prefer the
# player's own version, then look for the version string in the library.
mpv_version() {
	if have mpv; then
		version=$(mpv --version 2>/dev/null | sed -n '1s/^mpv \([0-9][0-9.]*\).*/\1/p')
		if [ -n "$version" ]; then
			printf '%s\n' "$version"
			return 0
		fi
	fi
	lib=$(libmpv_path) || return 1
	grep -a -o -m1 'mpv [0-9][0-9.]*' "$lib" 2>/dev/null | sed 's/^mpv //'
}

check_mpv() {
	if ! mpv_present; then
		need_install "libmpv" mpv
		return
	fi
	version=$(mpv_version)
	case $version in
	'' | *[!0-9.]*) return ;;
	esac
	major=${version%%.*}
	rest=${version#*.}
	minor=${rest%%.*}
	if [ "$major" -lt 0 ] || { [ "$major" -eq 0 ] && [ "$minor" -lt 37 ]; }; then
		need "libmpv 0.37 or newer (found $version)" \
			"upgrade it, or build mpv from source: https://mpv.io/installation/"
	fi
}

# Build from this checkout when the script sits in one. That is also what makes
# the script testable without a network: outside a checkout it fetches the
# source into a temporary directory.
in_checkout=0
if [ -f go.mod ] && grep -qx 'module ektara' go.mod 2>/dev/null; then
	in_checkout=1
fi

check_deps() {
	missing=0
	needed=""

	# go.mod asks for 1.27. A distro Go is often older, so a version that is
	# too old points at the official tarball instead of the package manager.
	if ! have go; then
		need_install "Go 1.27 or newer" go "or get the tarball from https://go.dev/dl/"
	else
		go_version=$(go version 2>/dev/null | sed -n 's/.*go\([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p')
		if [ -z "$go_version" ]; then
			needed="$needed go"
			need "Go 1.27 or newer (could not read 'go version')" "download it from https://go.dev/dl/"
		else
			go_major=${go_version%%.*}
			go_minor=${go_version#*.}
			if [ "$go_major" -lt 1 ] || { [ "$go_major" -eq 1 ] && [ "$go_minor" -lt 27 ]; }; then
				needed="$needed go"
				need "Go 1.27 or newer (found $go_version)" "download it from https://go.dev/dl/"
			fi
		fi
	fi

	if ! have cc && ! have gcc && ! have clang; then
		need_install "a C compiler for the database" cc
	fi

	check_mpv

	# ffmpeg is only needed to download songs. Streaming and your own files
	# work without it, so this is a note rather than a stop.
	if ! have ffmpeg; then
		needed="$needed ffmpeg"
		printf 'note: ffmpeg is missing, so downloading songs will not work.\n' >&2
		hint=$(install_hint ffmpeg)
		if [ -n "$hint" ]; then
			printf '  install it with: %s\n' "$hint" >&2
		fi
	fi

	if [ "$in_checkout" -eq 0 ] && ! have curl && ! have wget; then
		need_install "curl or wget to download the source" curl
	fi
}

check_deps
if [ "$install_deps" -eq 1 ] && [ -n "$needed" ]; then
	install_missing_deps || exit 1
	check_deps
fi
if [ "$missing" -ne 0 ]; then
	exit 1
fi

if [ "$in_checkout" -eq 1 ]; then
	src=$PWD
else
	tmpdir=$(mktemp -d)
	printf 'downloading %s ...\n' "$repo"
	if have curl; then
		curl -fsSL "https://github.com/$repo/archive/refs/heads/$branch.tar.gz" | tar -xz -C "$tmpdir"
	else
		wget -qO- "https://github.com/$repo/archive/refs/heads/$branch.tar.gz" | tar -xz -C "$tmpdir"
	fi
	src=$tmpdir/ektara-$branch
fi

# Stamp the version the same way build.sh does, when there is a git tree to ask.
ldflags="-s -w"
if [ -d "$src/.git" ] && have git; then
	version=$(cd "$src" && git describe --tags --always --dirty)
	commit=$(cd "$src" && git rev-parse --short HEAD)
	ldflags="-s -w -X main.version=$version -X main.commit=$commit"
fi

mkdir -p "$bindir"
tmpbin="$bindir/.ektara.$$"

printf 'building ektara ...\n'
(cd "$src" && CGO_ENABLED=1 go build -tags nocgo -trimpath -ldflags "$ldflags" -o "$tmpbin" .)

# Run the new binary before putting it in place: a libmpv that is present but
# too old only shows up when the program starts. A failed check leaves the old
# install untouched, and the trap removes the temporary binary.
if ! output=$("$tmpbin" --version 2>&1); then
	printf '\nektara built, but it could not start:\n%s\n' "$output" >&2
	exit 1
fi

# Rename rather than building straight onto the installed path, so a build that
# fails halfway does not leave a broken binary behind.
mv "$tmpbin" "$bindir/ektara"
tmpbin=""

printf 'installed %s\n' "$bindir/ektara"
printf '%s\n' "$output"

case ":$PATH:" in
*":$bindir:"*) ;;
*)
	printf '\n%s is not on your PATH. Add this to your shell profile:\n  export PATH="%s:$PATH"\n' "$bindir" "$bindir"
	;;
esac
