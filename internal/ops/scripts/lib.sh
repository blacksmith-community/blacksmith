# lib.sh holds the shared functions of the certificate patch helpers. The
# broker sends it, followed by one script, to `sudo -n /bin/bash -s --`
# on stdin, so nothing here is ever installed on the VM.
#
# Every script prints its results one line at a time. A line is either
# key=value pairs, or a word that names the kind of line followed by
# key=value pairs, and no value holds a space. Every script exits with
# one of these codes.
#
#   0   success
#   1   any other failure, after which the state is unknown, so the broker
#       runs check before it goes on
#   2   a usage error
#   10  drift, meaning a hash that is neither the old one nor the new one
#   11  an existing backup with the wrong hash
#   12  a restart that timed out
#   13  a path that a validator refused
#   14  a new file that openssl cannot read as a certificate or a key

set -u -o pipefail

# sudo resets the environment, but the scripts never rely on that. They
# use a fixed search path, the C locale so that character ranges and
# blank classes mean bytes, and a umask that keeps new files private.
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
LC_ALL=C
export LC_ALL
umask 077

# The scripts that follow this library use these, so shellcheck cannot
# see the uses from here.
# shellcheck disable=SC2034
readonly EXIT_USAGE=2 EXIT_DRIFT=10 EXIT_BACKUP=11 EXIT_RESTART=12
# shellcheck disable=SC2034
readonly EXIT_REFUSED=13 EXIT_INVALID=14

# NORM_LEAD_AWK drops the empty lines before the first line that holds
# anything, for norm_hash. A line of blanks holds something, so blanks
# before a BEGIN marker still change the hash. It works line by line, so
# a large file costs one pass.
# shellcheck disable=SC2016
readonly NORM_LEAD_AWK='
started { print; next }
length($0) > 0 { started = 1; print }
'

# norm_hash prints the lowercase hex SHA-256 of a file after the same
# normalising that certpatch.NormalisedHash applies in the broker. It
# removes every carriage return, drops the newlines before the first
# other byte, strips trailing spaces and tabs from each
# line, drops trailing newlines, and appends exactly one newline. A file
# that cannot be read, or that holds a NUL byte, prints nothing and
# returns 1, because the shell cannot carry a NUL through the steps below
# and would otherwise hash different text from the broker.
#
# norm_hash follows symlinks, as [ -f ] and the reads below do, so on its
# own it would hash any file root can read. Every caller runs valid_pinned
# or valid_work on the path first, and never passes a path that has not
# been validated.
norm_hash() {
	local file=$1 size kept text

	[ -f "$file" ] && [ -r "$file" ] || return 1

	size=$(wc -c < "$file") || return 1
	kept=$(tr -d '\000' < "$file" | wc -c) || return 1
	[ "$size" -eq "$kept" ] || return 1

	# awk drops the empty lines at the start, before sed strips blanks,
	# so a line of blanks there is kept. The command substitution drops
	# the trailing newlines, and printf puts exactly one back. printf is a
	# builtin, so the text never appears on a command line.
	text=$(tr -d '\r' < "$file" | awk "$NORM_LEAD_AWK" | sed -e 's/[[:blank:]]*$//') || return 1
	printf '%s\n' "$text" | sha256sum | cut -d ' ' -f 1
}

# raw_hash prints the lowercase hex SHA-256 of a file's bytes exactly as
# they are, as certpatch.RawHash does in the broker. Normalised hashes
# find the files that hold a certificate, and raw hashes then pin the
# exact bytes that discovery saw and that a replacement writes. Like
# norm_hash, it follows symlinks, so every caller validates the path
# first.
raw_hash() {
	[ -f "$1" ] && [ -r "$1" ] || return 1
	sha256sum < "$1" | cut -d ' ' -f 1
}

# The shared validators. Each one returns 0 when it accepts and 13 when it
# refuses, and a script exits with that code before it touches the path.
# A call with the wrong number of arguments is a fault in the calling
# script, not a refused path, so it returns the usage code 2.
#
# CERTPATCH_ROOT lets the tests put the VM's files under a temporary
# directory. sudo resets the environment, so in production it is unset
# and every path is the real one.

readonly CP_TASK_RE='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
readonly CP_MAX_PATH=1024
readonly CP_HASH_RE='^[0-9a-f]{64}$'

# _cp_shaped <path> <pattern> accepts a path of the right length that
# matches the pattern and has no empty, ".", or ".." segment.
_cp_shaped() {
	[ "${#1}" -le "$CP_MAX_PATH" ] || return 1
	[[ $1 =~ $2 ]] || return 1

	case "$1/" in
	*//* | */./* | */../*) return 1 ;;
	esac
}

# _cp_root prints the canonical test root, or nothing in production.
_cp_root() {
	local root=${CERTPATCH_ROOT:-}

	[ -n "$root" ] || return 0
	root=$(realpath -e -- "$root") || return 1
	[ -d "$root" ] || return 1
	[ "$root" = / ] && root=
	printf '%s' "$root"
}

# _cp_secure_dir <dir> accepts a directory that is not a symlink, is
# owned by root, and has neither the group nor the other write bit. stat
# reports on the name itself, not on what it points to. No exception is
# made for a sticky directory, because none belongs on these paths.
_cp_secure_dir() {
	local info uid mode

	[ -d "$1" ] && [ ! -L "$1" ] || return 1
	info=$(stat -c '%u %a' -- "$1") || return 1
	uid=${info% *}
	mode=${info#* }
	[ "$uid" = 0 ] || return 1
	(((8#$mode & 8#022) == 0))
}

# _cp_secure_chain <root> <dir> accepts the absolute directory <dir> only
# when it and every directory above it, up to and including /, pass
# _cp_secure_dir. Only root can then rename, replace, or add an entry
# anywhere on the way down, so a check made here still holds when the
# script writes. <root> stands in for / in the tests.
_cp_secure_chain() {
	local root=$1 rest=${2#/} dir=

	_cp_secure_dir "${root:-/}" || return 1

	while [ -n "$rest" ]; do
		dir=$dir/${rest%%/*}

		case $rest in
		*/*) rest=${rest#*/} ;;
		*) rest= ;;
		esac

		_cp_secure_dir "$root$dir" || return 1
	done
}

# _cp_private_dir <dir> accepts a directory that passes _cp_secure_dir
# and whose mode is exactly 0700.
_cp_private_dir() {
	_cp_secure_dir "$1" || return 1
	[ "$(stat -c %a -- "$1")" = 700 ]
}

# _cp_make_private <dir> creates a private directory with plain mkdir,
# never mkdir -p, or accepts one that already exists only when it is
# private. A name that appears between the check and mkdir makes mkdir
# fail, which refuses.
_cp_make_private() {
	if [ ! -e "$1" ] && [ ! -L "$1" ]; then
		mkdir -m 0700 -- "$1" 2> /dev/null || return 1
	fi

	_cp_private_dir "$1"
}

# valid_task <task> accepts only a lowercase canonical UUID.
valid_task() {
	[ "$#" -eq 1 ] || return "$EXIT_USAGE"
	[[ $1 =~ $CP_TASK_RE ]] || return "$EXIT_REFUSED"
}

# valid_hash <hash> accepts a lowercase hex SHA-256, the form norm_hash
# prints. A hash is an argument from the broker and never a path, so a
# malformed one is a usage error.
valid_hash() {
	[ "$#" -eq 1 ] || return "$EXIT_USAGE"
	[[ $1 =~ $CP_HASH_RE ]] || return "$EXIT_USAGE"
}
