# lib.sh holds the shared functions of the certificate patch helpers. The
# broker sends it, followed by one script, to `sudo -n /bin/bash -s --`
# on stdin, so nothing here is ever installed on the VM.
#
# Every script prints its results one line at a time as key=value pairs
# whose values hold no spaces, and exits with one of these codes.
#
#   0   success
#   2   a usage error
#   10  drift, meaning a hash that is neither the old one nor the new one
#   11  an existing backup with the wrong hash
#   12  a restart that timed out
#   13  a path that a validator refused

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
readonly EXIT_REFUSED=13

# norm_hash prints the lowercase hex SHA-256 of a file after the same
# normalising that certpatch.NormalisedHash applies in the broker. It
# removes every carriage return, strips trailing spaces and tabs from each
# line, drops trailing newlines, and appends exactly one newline. A file
# that cannot be read, or that holds a NUL byte, prints nothing and
# returns 1, because the shell cannot carry a NUL through the steps below
# and would otherwise hash different text from the broker.
norm_hash() {
	local file=$1 size kept text

	[ -f "$file" ] && [ -r "$file" ] || return 1

	size=$(wc -c < "$file") || return 1
	kept=$(tr -d '\000' < "$file" | wc -c) || return 1
	[ "$size" -eq "$kept" ] || return 1

	# The command substitution drops the trailing newlines, and printf
	# puts exactly one back. printf is a builtin, so the text never
	# appears on a command line.
	text=$(tr -d '\r' < "$file" | sed -e 's/[[:blank:]]*$//') || return 1
	printf '%s\n' "$text" | sha256sum | cut -d ' ' -f 1
}

# The path validators. Each one returns 0 when it accepts and 13 when it
# refuses, and a script exits with that code before it touches the path.
# The patterns are the ones in paths.go. The checks of the VM's files
# then add what only the VM can see.
#
# CERTPATCH_ROOT lets the tests put the VM's files under a temporary
# directory. sudo resets the environment, so in production it is unset
# and every path is the real one.

readonly CP_LOGICAL_RE='^/var/vcap/jobs/[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+$'
readonly CP_PINNED_RE='^/var/vcap/data/jobs/[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+$'
readonly CP_JOBDIR_RE='^/var/vcap/data/jobs/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'
readonly CP_TASK_RE='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
readonly CP_WORK_TAIL_RE='^[sb]/(0|[1-9][0-9]{0,8})$'
readonly CP_MAX_PATH=1024
readonly CP_WORK_ROOT=/var/vcap/data/certpatch

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
	[ "$#" -eq 1 ] || return "$EXIT_REFUSED"
	[[ $1 =~ $CP_TASK_RE ]] || return "$EXIT_REFUSED"
}

# valid_logical <path> accepts a path under /var/vcap/jobs/<job>/. Only
# discovery reports such paths, and nothing writes to one.
valid_logical() {
	[ "$#" -eq 1 ] || return "$EXIT_REFUSED"
	_cp_shaped "$1" "$CP_LOGICAL_RE" || return "$EXIT_REFUSED"
}

# valid_pinned <path> <jobdir> accepts a certificate or key file under
# the job's resolved directory that discovery recorded. On top of the
# pattern it requires that /var/vcap/jobs/<job> is a symlink that still
# points at jobdir, that every directory from / down to the file's parent
# is owned by root, is not a symlink, and is not writable by group or
# other, and that the file is a regular file whose real path is the path
# itself.
valid_pinned() {
	[ "$#" -eq 2 ] || return "$EXIT_REFUSED"

	local path=$1 jobdir=$2 root rest job link full real

	_cp_shaped "$jobdir" "$CP_JOBDIR_RE" || return "$EXIT_REFUSED"
	_cp_shaped "$path" "$CP_PINNED_RE" || return "$EXIT_REFUSED"
	[[ $path == "$jobdir"/* ]] || return "$EXIT_REFUSED"

	root=$(_cp_root) || return "$EXIT_REFUSED"

	rest=${jobdir#/var/vcap/data/jobs/}
	job=${rest%%/*}
	link=$root/var/vcap/jobs/$job
	[ -L "$link" ] || return "$EXIT_REFUSED"
	[ "$(readlink -- "$link")" = "$root$jobdir" ] || return "$EXIT_REFUSED"

	_cp_secure_chain "$root" "${path%/*}" || return "$EXIT_REFUSED"

	full=$root$path
	[ -f "$full" ] && [ ! -L "$full" ] || return "$EXIT_REFUSED"
	real=$(realpath -e -- "$full") || return "$EXIT_REFUSED"
	[ "$real" = "$full" ] || return "$EXIT_REFUSED"
}

# valid_work <path> <task> accepts only the task's staging path s/<n> or
# backup path b/<n>. It requires that every directory from / down to
# /var/vcap/data is secure, as for a pinned target, that the certpatch
# directory, the task's directory, and the s or b directory are all
# private to root and none is a symlink, and that anything already at
# the path is a regular file and not a symlink.
valid_work() {
	[ "$#" -eq 2 ] || return "$EXIT_REFUSED"

	local path=$1 task=$2 root tail full

	valid_task "$task" || return "$EXIT_REFUSED"
	[ "${#path}" -le "$CP_MAX_PATH" ] || return "$EXIT_REFUSED"
	tail=${path#"$CP_WORK_ROOT/$task/"}
	[ "$tail" != "$path" ] || return "$EXIT_REFUSED"
	[[ $tail =~ $CP_WORK_TAIL_RE ]] || return "$EXIT_REFUSED"

	root=$(_cp_root) || return "$EXIT_REFUSED"

	_cp_secure_chain "$root" /var/vcap/data || return "$EXIT_REFUSED"
	_cp_private_dir "$root$CP_WORK_ROOT" || return "$EXIT_REFUSED"
	_cp_private_dir "$root$CP_WORK_ROOT/$task" || return "$EXIT_REFUSED"
	_cp_private_dir "$root$CP_WORK_ROOT/$task/${tail%%/*}" || return "$EXIT_REFUSED"

	full=$root$path

	if [ -e "$full" ] || [ -L "$full" ]; then
		[ -f "$full" ] && [ ! -L "$full" ] || return "$EXIT_REFUSED"
	fi
}

# make_work_dir <task> creates the task's private directory and its s
# and b directories under /var/vcap/data/certpatch, after checking that
# every directory from / down to /var/vcap/data is secure. A directory
# that already exists is accepted only when it is root's, mode 0700, and
# not a symlink, so a task that resumes reuses its own directory and a
# planted one stops the script.
make_work_dir() {
	[ "$#" -eq 1 ] || return "$EXIT_REFUSED"

	local task=$1 root base kind

	valid_task "$task" || return "$EXIT_REFUSED"
	root=$(_cp_root) || return "$EXIT_REFUSED"
	base=$root$CP_WORK_ROOT

	_cp_secure_chain "$root" /var/vcap/data || return "$EXIT_REFUSED"
	_cp_make_private "$base" || return "$EXIT_REFUSED"
	_cp_make_private "$base/$task" || return "$EXIT_REFUSED"

	for kind in s b; do
		_cp_make_private "$base/$task/$kind" || return "$EXIT_REFUSED"
	done
}
