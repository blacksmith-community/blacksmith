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

# The path validators. Each one returns 0 when it accepts and 13 when it
# refuses, and a script exits with that code before it touches the path.
# A call with the wrong number of arguments is a fault in the calling
# script, not a refused path, so it returns the usage code 2.
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

# valid_logical <path> accepts a path under /var/vcap/jobs/<job>/. Only
# discovery reports such paths, and nothing writes to one.
valid_logical() {
	[ "$#" -eq 1 ] || return "$EXIT_USAGE"
	_cp_shaped "$1" "$CP_LOGICAL_RE" || return "$EXIT_REFUSED"
}

# valid_pinned <path> <jobdir> accepts a certificate or key file under
# the job's resolved directory that discovery recorded. On top of the
# pattern it requires that /var/vcap/jobs/<job> is a symlink that still
# points at jobdir, that every directory from / down to the file's parent
# is owned by root, is not a symlink, and is not writable by group or
# other, and that the file is a regular file with one link whose real
# path is the path itself. A second link would let the write change a
# file outside the job's tree.
valid_pinned() {
	[ "$#" -eq 2 ] || return "$EXIT_USAGE"

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
	[ "$(stat -c %h -- "$full")" = 1 ] || return "$EXIT_REFUSED"
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
	[ "$#" -eq 2 ] || return "$EXIT_USAGE"
	_cp_valid_work "$1" "$2" no
}

# valid_work_unmade <path> <task> accepts what valid_work accepts, and
# also a path of the right shape whose directories prepare has not made
# yet, so a read-only step can run before prepare. The directories from
# / down to /var/vcap/data must still be secure, so only root can create
# the certpatch directory or anything in it. Each of the certpatch
# directory, the task's directory, and the s or b directory that exists
# must be private to root and not a symlink, and the walk stops at the
# first that does not exist, because nothing can be under it. A dangling
# symlink exists for this purpose, and is refused.
valid_work_unmade() {
	[ "$#" -eq 2 ] || return "$EXIT_USAGE"
	_cp_valid_work "$1" "$2" yes
}

# _cp_valid_work <path> <task> <unmade> is valid_work when unmade is no,
# and valid_work_unmade when it is yes.
_cp_valid_work() {
	local path=$1 task=$2 unmade=$3 root tail full dir

	valid_task "$task" || return "$EXIT_REFUSED"
	[ "${#path}" -le "$CP_MAX_PATH" ] || return "$EXIT_REFUSED"
	tail=${path#"$CP_WORK_ROOT/$task/"}
	[ "$tail" != "$path" ] || return "$EXIT_REFUSED"
	[[ $tail =~ $CP_WORK_TAIL_RE ]] || return "$EXIT_REFUSED"

	root=$(_cp_root) || return "$EXIT_REFUSED"

	_cp_secure_chain "$root" /var/vcap/data || return "$EXIT_REFUSED"

	for dir in "$CP_WORK_ROOT" "$CP_WORK_ROOT/$task" "$CP_WORK_ROOT/$task/${tail%%/*}"; do
		if [ "$unmade" = yes ] && [ ! -e "$root$dir" ] && [ ! -L "$root$dir" ]; then
			return 0
		fi

		_cp_private_dir "$root$dir" || return "$EXIT_REFUSED"
	done

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
	[ "$#" -eq 1 ] || return "$EXIT_USAGE"

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
# discover.sh finds the files on this VM that hold an old certificate or
# an old key, and the monit processes of every job. It only reads.
#
#   discover <old-hash>...
#
# Each hash is the normalised hash of an old certificate or an old key.
# The script prints these lines, which ParseDiscovery reads.
#
#   jobdir job=<job> path=<resolved job directory>
#   match job=<job> path=<pinned target> hash=<h> raw=<r> lead=<l> tail=<t> owner=<user:group> mode=<octal> rootdirs=<yes|no>
#   embedded job=<job> path=<file> hash=<h>
#   proc job=<job> name=<process>
#
# A match is a regular file, not a symlink, under the job's resolved
# directory whose whole normalised content has one of the hashes. raw is
# the SHA-256 of its bytes as they are. lead spells out the blank lines
# before its first other byte and tail the whitespace after its last, one
# letter a byte, n for a newline, r for a carriage return, s for a space,
# and t for a tab, or - for none. Only whole blank lines, LF or CRLF, may
# come before the PEM, so a file with anything else there, such as a
# space before its BEGIN marker, or with more whitespace around it than
# the patterns allow, is reported as embedded instead.
# rootdirs=yes says that valid_pinned accepts the file, so every
# directory from / down to it is root's and not writable by group or
# other, and the file has a single link. A rootdirs=no line ends with
# why=, the first condition that failed, and at= for the path it failed
# on. The token is shape, job-link, dir-symlink, dir-missing, dir-owner,
# dir-writable, not-regular, many-links, realpath, or other. The two
# directory tokens that concern ownership and mode add by=, the
# directory's owner:group, and bits=, its mode in octal. An embedded line is a file that
# holds a PEM block with one of the hashes inside larger content, which
# patching cannot replace. A value that is not safe to print, such as a
# path with a space, is printed as !, which the broker refuses.

readonly DC_JOB_RE='^[A-Za-z0-9_.-]+$'
readonly DC_PROC_RE='^[A-Za-z0-9][A-Za-z0-9_.:@+-]*$'
readonly DC_OWNER_RE='^[A-Za-z0-9_.-]+:[A-Za-z0-9_.-]+$'
readonly DC_DIR_RE='^/[A-Za-z0-9_./:-]*$'

# A certificate or key file is a few kilobytes, so a larger file is only
# searched for embedded blocks and never hashed whole.
readonly DC_MAX_SIZE=1048576

# DC_BLOCKS_AWK prints the SHA-256 of each PEM block in a file, after the
# normalising norm_hash applies to a whole file. Leading and trailing
# blanks go from every line, so a block indented in YAML hashes the same
# as the bare block, and a literal \n, as JSON escapes a newline, splits
# a line. Text before a BEGIN marker or after an END marker on the same
# line is dropped.
# shellcheck disable=SC2016
readonly DC_BLOCKS_AWK='
function piece(s,   at) {
	gsub(/\r/, "", s)
	sub(/^[ \t]+/, "", s)
	sub(/[ \t]+$/, "", s)

	if (!open) {
		at = index(s, "-----BEGIN ")
		if (at == 0) return
		s = substr(s, at)
		if (s !~ /^-----BEGIN [A-Z0-9 ]+-----$/) return
		open = 1
		block = s "\n"
		return
	}

	at = index(s, "-----END ")
	if (at > 0) {
		s = substr(s, at)
		if (match(s, /^-----END [A-Z0-9 ]+-----/)) {
			block = block substr(s, 1, RLENGTH) "\n"
			printf "%s", block | "sha256sum"
			close("sha256sum")
		}
		open = 0
		block = ""
		return
	}

	block = block s "\n"
}

{
	line = $0
	n = split(line, parts, /\\n/)
	for (i = 1; i <= n; i++) piece(parts[i])
}
'

# The forms of lead and tail that a match may have. ParseDiscovery
# checks the same forms again in the broker.
readonly DC_LEAD_RE='^(-|(rn|n){1,32})$'
readonly DC_TAIL_RE='^(-|[nrst]{1,64})$'

# DC_EDGES_AWK reads od's decimal bytes of a file and prints its lead and
# its tail, in the letters above. It exits 1 for a file that holds only
# carriage returns and newlines.
# shellcheck disable=SC2016
readonly DC_EDGES_AWK='
{
	for (i = 1; i <= NF; i++) {
		c = $i + 0

		if (!started) {
			if (c == 10) { lead = lead "n"; continue }
			if (c == 13) { lead = lead "r"; continue }
			started = 1
		}

		if (c == 9) tail = tail "t"
		else if (c == 10) tail = tail "n"
		else if (c == 13) tail = tail "r"
		else if (c == 32) tail = tail "s"
		else tail = ""
	}
}

END {
	if (!started) exit 1
	if (lead == "") lead = "-"
	if (tail == "") tail = "-"
	print lead, tail
}
'

# _dc_word <value> <pattern> prints the value when it matches the
# pattern, and ! when it does not.
_dc_word() {
	if [[ $1 =~ $2 ]]; then
		printf '%s' "$1"
	else
		printf '!'
	fi
}

# _dc_path <path> prints a pinned path that is safe to print, and !
# otherwise.
_dc_path() {
	if _cp_shaped "$1" "$CP_PINNED_RE"; then
		printf '%s' "$1"
	else
		printf '!'
	fi
}

# _dc_why <path> <jobdir> prints the first condition that makes valid_pinned
# refuse the file, as the fields why=, at=, and for a directory by= and
# bits=, with a space before each. It checks in the order valid_pinned
# does, and it runs only after valid_pinned has refused, so it explains
# a refusal and never grants anything. A refusal that none of its checks
# reproduces, such as a file that changed in between, is why=other.
_dc_why() {
	local path=$1 jobdir=$2 root rest job link full real dir at info uid mode

	if ! _cp_shaped "$jobdir" "$CP_JOBDIR_RE" || ! _cp_shaped "$path" "$CP_PINNED_RE" || [[ $path != "$jobdir"/* ]]; then
		printf ' why=shape'
		return 0
	fi

	root=$(_cp_root) || {
		printf ' why=other'
		return 0
	}

	rest=${jobdir#/var/vcap/data/jobs/}
	job=${rest%%/*}
	link=$root/var/vcap/jobs/$job

	if [ ! -L "$link" ] || [ "$(readlink -- "$link")" != "$root$jobdir" ]; then
		printf ' why=job-link at=%s' "$(_dc_word "/var/vcap/jobs/$job" "$DC_DIR_RE")"
		return 0
	fi

	# Walk the directories from / down to the file's parent.
	rest=${path%/*}
	rest=${rest#/}
	dir=

	while :; do
		at=$(_dc_word "${dir:-/}" "$DC_DIR_RE")

		if [ -L "$root${dir:-/}" ]; then
			printf ' why=dir-symlink at=%s' "$at"
			return 0
		fi

		if [ ! -d "$root${dir:-/}" ]; then
			printf ' why=dir-missing at=%s' "$at"
			return 0
		fi

		info=$(stat -c '%u %a %U:%G' -- "$root${dir:-/}") || {
			printf ' why=other'
			return 0
		}
		uid=${info%% *}
		mode=${info#* }
		mode=${mode%% *}

		if [ "$uid" != 0 ] || (((8#$mode & 8#022) != 0)); then
			if [ "$uid" != 0 ]; then
				printf ' why=dir-owner'
			else
				printf ' why=dir-writable'
			fi

			printf ' at=%s by=%s bits=%s' "$at" "$(_dc_word "${info##* }" "$DC_OWNER_RE")" "$(printf '%04o' "$((8#$mode))")"
			return 0
		fi

		[ -n "$rest" ] || break

		dir=$dir/${rest%%/*}

		case $rest in
		*/*) rest=${rest#*/} ;;
		*) rest= ;;
		esac
	done

	full=$root$path

	if [ ! -f "$full" ] || [ -L "$full" ]; then
		printf ' why=not-regular at=%s' "$(_dc_path "$path")"
	elif [ "$(stat -c %h -- "$full")" != 1 ]; then
		printf ' why=many-links at=%s' "$(_dc_path "$path")"
	elif ! real=$(realpath -e -- "$full") || [ "$real" != "$full" ]; then
		printf ' why=realpath at=%s' "$(_dc_path "$path")"
	else
		printf ' why=other'
	fi
}

# _dc_regular <file> succeeds when the file is a regular file and not a
# symlink. find listed it as one, but it can change before it is read,
# and the reads below would follow a symlink.
_dc_regular() {
	[ -f "$1" ] && [ ! -L "$1" ]
}

# _dc_file <root> <job> <jobdir> <file> reports one regular file under a
# job's resolved directory. It reads the caller's wanted array. It checks
# the file with _dc_regular right before each read, and a file that is
# no longer a regular file fails discovery.
_dc_file() {
	local root=$1 job=$2 jobdir=$3 file=$4 path size hash raw edges lead tail info owner mode rootdirs=no why=

	path=${file#"$root"}
	_dc_regular "$file" || return 1
	size=$(stat -c %s -- "$file") || return 1

	if [ "$size" -le "$DC_MAX_SIZE" ] && _dc_regular "$file" && hash=$(norm_hash "$file") && [ -n "${wanted[$hash]:-}" ]; then
		info=$(stat -c '%U:%G %a' -- "$file") || return 1
		owner=$(_dc_word "${info% *}" "$DC_OWNER_RE")
		mode=$(printf '%04o' "$((8#${info#* }))")

		if valid_pinned "$path" "$jobdir"; then
			rootdirs=yes
		else
			why=$(_dc_why "$path" "$jobdir")
		fi

		_dc_regular "$file" || return 1
		raw=$(raw_hash "$file") || return 1
		_dc_regular "$file" || return 1
		edges=$(od -An -v -tu1 -- "$file" | awk "$DC_EDGES_AWK") || edges=
		lead=${edges% *}
		tail=${edges#* }

		if [[ $lead =~ $DC_LEAD_RE ]] && [[ $tail =~ $DC_TAIL_RE ]]; then
			printf 'match job=%s path=%s hash=%s raw=%s lead=%s tail=%s owner=%s mode=%s rootdirs=%s%s\n' \
				"$job" "$(_dc_path "$path")" "$hash" "$raw" "$lead" "$tail" "$owner" "$mode" "$rootdirs" "$why"
		else
			printf 'embedded job=%s path=%s hash=%s\n' "$job" "$(_dc_path "$path")" "$hash"
		fi

		return 0
	fi

	# Patching covers PEM blocks only, and a copy in another form that
	# this misses still holds the old certificate, which the verification
	# probe then catches.
	_dc_regular "$file" || return 1
	grep -qa -e '-----BEGIN ' -- "$file" || return 0

	local hashes
	_dc_regular "$file" || return 1
	hashes=$(awk "$DC_BLOCKS_AWK" "$file" | cut -d ' ' -f 1 | sort -u) || return 1

	for hash in $hashes; do
		[ -n "${wanted[$hash]:-}" ] || continue
		printf 'embedded job=%s path=%s hash=%s\n' "$job" "$(_dc_path "$path")" "$hash"
	done
}

# _dc_job <root> <entry> reports one entry of /var/vcap/jobs. BOSH makes
# each one a symlink to /var/vcap/data/jobs/<job>/<version>. An entry
# that is anything else is printed with path=!, which the broker
# refuses, and is not searched.
_dc_job() {
	local root=$1 entry=$2 job target jobdir file files

	job=$(_dc_word "${entry##*/}" "$DC_JOB_RE")

	if [ ! -L "$entry" ] || [ "$job" = '!' ]; then
		printf 'jobdir job=%s path=!\n' "$job"
		return 0
	fi

	target=$(readlink -- "$entry") || return 1
	jobdir=${target#"$root"}

	if [ -n "$root" ] && [ "$jobdir" = "$target" ]; then
		jobdir='!'
	fi

	if ! _cp_shaped "$jobdir" "$CP_JOBDIR_RE"; then
		printf 'jobdir job=%s path=%s\n' "$job" "$(_dc_word "$jobdir" "$DC_DIR_RE")"
		return 0
	fi

	printf 'jobdir job=%s path=%s\n' "$job" "$jobdir"

	# find does not follow symlinks, so -type f lists only regular files
	# and never descends through a symlinked directory.
	mapfile -d '' files < <(find "$root$jobdir" -type f -print0)
	wait "$!" || return 1

	for file in "${files[@]}"; do
		_dc_file "$root" "$job" "$jobdir" "$file" || return 1
	done
}

# _dc_monit <root> prints the processes each job's monit file names. The
# file name gives the job, after a leading NNNN_ and the .monitrc suffix
# come off, because BOSH numbers the files by job order.
_dc_monit() {
	local root=$1 file name job line

	for file in "$root"/var/vcap/monit/job/*.monitrc; do
		[ -f "$file" ] && [ ! -L "$file" ] || continue

		name=${file##*/}
		name=${name%.monitrc}

		if [[ $name =~ ^[0-9]{4}_(.+)$ ]]; then
			name=${BASH_REMATCH[1]}
		fi

		job=$(_dc_word "$name" "$DC_JOB_RE")

		# monit reads its keywords in any case.
		shopt -s nocasematch

		while IFS= read -r line || [ -n "$line" ]; do
			if [[ $line =~ ^[[:space:]]*check[[:space:]]+process[[:space:]]+([^[:space:]]+) ]]; then
				printf 'proc job=%s name=%s\n' "$job" "$(_dc_word "${BASH_REMATCH[1]}" "$DC_PROC_RE")"
			fi
		done < "$file"

		shopt -u nocasematch
	done
}

discover() {
	[ "$#" -ge 1 ] || return "$EXIT_USAGE"

	local -A wanted=()
	local hash root entry

	for hash in "$@"; do
		valid_hash "$hash" || return
		wanted[$hash]=1
	done

	root=$(_cp_root) || return "$EXIT_REFUSED"

	shopt -s nullglob dotglob

	for entry in "$root"/var/vcap/jobs/*; do
		_dc_job "$root" "$entry" || return 1
	done

	_dc_monit "$root"
}

case ${1:-} in
discover)
	shift
	discover "$@"
	;;
*)
	exit "$EXIT_USAGE"
	;;
esac
