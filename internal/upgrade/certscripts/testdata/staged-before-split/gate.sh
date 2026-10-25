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
# gate.sh runs a cluster's health check on this VM. It only reads.
#
#   redis-cluster
#   valkey-cluster
#   rabbitmq-cluster
#
# For Redis and Valkey it reads the password and the TLS settings from
# the rendered config under the job's directory, runs CLUSTER INFO
# against the local server, and prints only the CLUSTER INFO fields that
# ParseClusterInfo reads. The password reaches the CLI through its
# environment, so it is never on a command line, and a line that holds
# it is never printed.
#
# For RabbitMQ it runs rabbitmq-diagnostics check_running and
# cluster_status as vcap, and prints
#
#   check_running exit=<n>
#   cluster_status exit=<n>
#   <the cluster status as JSON>
#
# which ParseRabbitGate reads. Nothing else from the job's env file is
# used, because that file also sets passwords.
#
# A gate that cannot find its config or its CLI exits 1.

readonly GT_CLUSTER_RE='^cluster_(state:[a-z]+|(slots_assigned|slots_ok|slots_pfail|slots_fail|known_nodes|size):[0-9]+)$'
readonly GT_PORT_RE='^[0-9]{1,5}$'
readonly GT_NODE_RE='^export RABBITMQ_NODENAME="([A-Za-z0-9_.@-]+)"$'

# _gt_highest <path>... prints the path with the highest version among
# the paths that are regular files. The glob sorts by bytes, which puts
# valkey-10 before valkey-9, so the paths are put in version order first.
_gt_highest() {
	local path found=''

	while IFS= read -r path; do
		[ -f "$path" ] && found=$path
	done < <(printf '%s\n' "$@" | sort -V)

	[ -n "$found" ] || return 1
	printf '%s' "$found"
}

# _gt_redis <config name> <cli name>... runs CLUSTER INFO for a Redis or
# Valkey cluster. The config must be the only file of its name among the
# jobs' config directories.
_gt_redis() {
	local conf=$1 root files line key value password='' port='' tls='' ca='' cli='' name out
	local -a args=()

	shift
	root=$(_cp_root) || return "$EXIT_REFUSED"

	files=("$root"/var/vcap/jobs/*/config/"$conf")
	[ "${#files[@]}" -eq 1 ] && [ -f "${files[0]}" ] || return 1

	while IFS= read -r line || [ -n "$line" ]; do
		line=${line%$'\r'}
		[[ $line =~ ^[[:blank:]]*([A-Za-z-]+)[[:blank:]]+(.*[^[:blank:]])[[:blank:]]*$ ]] || continue

		key=${BASH_REMATCH[1],,}
		value=${BASH_REMATCH[2]}

		if [[ $value =~ ^\"(.*)\"$ ]]; then
			value=${BASH_REMATCH[1]}
		fi

		case $key in
		requirepass) password=$value ;;
		port) port=$value ;;
		tls-port) tls=$value ;;
		tls-ca-cert-file) ca=$value ;;
		esac
	done < "${files[0]}"

	for name in "$@"; do
		cli=$(_gt_highest "$root"/var/vcap/packages/*/bin/"$name") && break
	done

	[ -n "$cli" ] && [ -x "$cli" ] || return 1

	if [ -n "$tls" ] && [ "$tls" != 0 ]; then
		[[ $tls =~ $GT_PORT_RE ]] || return 1
		[[ $ca == /* ]] || return 1
		args=(--tls --cacert "$root$ca" -h 127.0.0.1 -p "$tls")
	else
		port=${port:-6379}
		[[ $port =~ $GT_PORT_RE ]] && [ "$port" != 0 ] || return 1
		args=(-h 127.0.0.1 -p "$port")
	fi

	# export is a builtin, so the password never appears in a process's
	# arguments. The subshell keeps it out of the rest of the script.
	out=$(
		if [ -n "$password" ]; then
			export REDISCLI_AUTH=$password VALKEYCLI_AUTH=$password
		fi

		timeout 20 "$cli" "${args[@]}" CLUSTER INFO 2> /dev/null
	) || true

	while IFS= read -r line; do
		line=${line%$'\r'}

		[[ $line =~ $GT_CLUSTER_RE ]] || continue

		if [ -n "$password" ] && [[ $line == *"$password"* ]]; then
			continue
		fi

		printf '%s\n' "$line"
	done <<< "$out"
}

# _gt_rabbitmq runs the RabbitMQ checks as vcap, never as root, with the
# node name from the job's env file and the Erlang cookie in vcap's home.
# The user is the owner of vcap's home, which pre-start gives to vcap
# along with the cookie, so the gate stops when root owns that home or a
# symlink stands in its place. setpriv keeps the environment set here.
_gt_rabbitmq() {
	local root diag env home owner uid gid line node='' running status code

	root=$(_cp_root) || return "$EXIT_REFUSED"
	diag=$root/var/vcap/packages/rabbitmq/sbin/rabbitmq-diagnostics
	env=$root/var/vcap/jobs/rabbitmq/env
	home=$root/home/vcap

	[ -f "$diag" ] && [ -x "$diag" ] && [ -f "$env" ] || return 1
	[ -d "$home" ] && [ ! -L "$home" ] || return 1

	owner=$(stat -c '%u %g' -- "$home") || return 1
	uid=${owner% *}
	gid=${owner#* }
	[[ $uid =~ ^[1-9][0-9]*$ ]] && [[ $gid =~ ^[1-9][0-9]*$ ]] || return 1

	while IFS= read -r line || [ -n "$line" ]; do
		line=${line%$'\r'}

		if [[ $line =~ $GT_NODE_RE ]]; then
			node=${BASH_REMATCH[1]}
		fi
	done < "$env"

	[ -n "$node" ] || return 1

	(
		# vcap cannot read the caller's working directory, and the Erlang
		# code server prints an error report to stdout for each file it
		# fails to read there.
		cd "$home" || exit 1

		export HOME=$home RABBITMQ_NODENAME=$node RABBITMQ_USE_LONGNAME=true
		export PATH=$root/var/vcap/packages/erlang/bin:$root/var/vcap/packages/rabbitmq/sbin:$PATH

		if [ -f "$root/var/vcap/jobs/rabbitmq/config/erl_inetrc" ]; then
			export ERL_INETRC=$root/var/vcap/jobs/rabbitmq/config/erl_inetrc
		fi

		timeout 60 setpriv --reuid="$uid" --regid="$gid" --clear-groups -- \
			"$diag" check_running > /dev/null 2>&1
		running=$?

		status=$(timeout 60 setpriv --reuid="$uid" --regid="$gid" --clear-groups -- \
			"$diag" cluster_status --formatter json 2> /dev/null)
		code=$?

		printf 'check_running exit=%d\ncluster_status exit=%d\n%s\n' "$running" "$code" "$status"
	)
}

[ "$#" -eq 1 ] || exit "$EXIT_USAGE"

case $1 in
redis-cluster)
	_gt_redis redis.conf redis-cli
	;;
valkey-cluster)
	_gt_redis valkey.conf valkey-cli redis-cli
	;;
rabbitmq-cluster)
	_gt_rabbitmq
	;;
*)
	exit "$EXIT_USAGE"
	;;
esac
