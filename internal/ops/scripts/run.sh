# run.sh stages and cleans up after one operator command on one VM. Each
# operation takes the task ID first and checks it before anything else.
#
#   prepare <task> <user>
#   cleanup <task>
#   exec <task> <user> <seconds> <sha256>
#
# <user> is root or vcap, the account the command will run as. prepare
# reads the command from the stdin that follows this script and writes it
# byte for byte to /var/vcap/data/runcmd/<task>/cmd.sh. The command
# arrives as data on stdin and is never part of a command line, an
# argument, or a shell string, and nothing here runs, evals, or sources
# it.
#
#   prepare prints  staged bytes=<n> sha256=<hex>
#   cleanup prints  cleanup started=<yes|no|-> exit=<code or -> killed=<yes|no>
#
# The directory of a task is root-owned with mode 0710 and the group of
# the vcap account, inside runcmd, which is root-owned with mode 0711. A
# user without the group can pass through runcmd by name but cannot list
# it, and vcap can open cmd.sh by name but cannot list, replace, write,
# or remove anything on the way to it. The task directory also holds the
# files a later operation writes, which are started, pgid, and exit.
#
# CERTPATCH_ROOT lets the tests put the VM's files under a temporary
# directory, as lib.sh explains.

readonly RC_DATA=/var/vcap/data
readonly RC_USER_RE='^(root|vcap)$'
readonly RC_SECS_RE='^[1-9][0-9]{0,5}$'
readonly RC_EXIT_RE='^([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])$'

# The directory that the EXIT trap removes when an operation stops before
# it is done. prepare sets it while the directory is half made, and
# cleanup sets it while the directory is being removed.
_RC_DROP=

# The EXIT trap calls this, which shellcheck does not follow.
# shellcheck disable=SC2329
_rc_drop() {
	if [ -n "$_RC_DROP" ]; then
		rm -rf -- "$_RC_DROP"
	fi
}

trap _rc_drop EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# _rc_tool <name> prints how to run an external tool. Under the test root
# it is the tool's path beneath that root, because lib.sh fixes PATH to
# the real directories and a fake in the root would never be found there.
# In production sudo resets the environment, CERTPATCH_ROOT is unset, and
# the bare name resolves on the fixed PATH.
_rc_tool() {
	local root

	if [ -z "${CERTPATCH_ROOT:-}" ]; then
		printf '%s' "$1"
		return 0
	fi

	root=$(_cp_root) || return 1
	printf '%s/usr/bin/%s' "$root" "$1"
}

# _rc_vcap_gid prints the group ID of the vcap account. In production it
# comes from getent. Under the test root there is no account database, so
# it is the group of the fake home/vcap directory, which stands in for
# the account. A group that is not a number, and group 0, are refused.
_rc_vcap_gid() {
	local root gid entry

	if [ -n "${CERTPATCH_ROOT:-}" ]; then
		root=$(_cp_root) || return 1
		gid=$(stat -c %g -- "$root/home/vcap") || return 1
	else
		entry=$("$(_rc_tool getent)" passwd vcap) || return 1
		IFS=: read -r _ _ _ gid _ <<< "$entry"
	fi

	[[ $gid =~ ^[0-9]+$ ]] || return 1
	[ "$gid" != 0 ] || return 1
	printf '%s' "$gid"
}

# _rc_match <info> <uid> <gid> <mode> compares the "uid gid mode" that
# stat printed with what is expected. A "-" accepts any value.
_rc_match() {
	local uid gid mode

	IFS=' ' read -r uid gid mode <<< "$1"

	{ [ "$2" = - ] || [ "$uid" = "$2" ]; } || return 1
	{ [ "$3" = - ] || [ "$gid" = "$3" ]; } || return 1
	{ [ "$4" = - ] || [ "$mode" = "$4" ]; }
}

# _rc_check_dir <path> <uid> <gid> <mode> accepts a directory that is not
# a symlink and has exactly that owner, group, and mode. The mode is
# written as stat -c %a prints it, such as 710. stat reports on the name
# itself, not on what it points to.
_rc_check_dir() {
	local info

	[ -d "$1" ] && [ ! -L "$1" ] || return 1
	info=$(stat -c '%u %g %a' -- "$1") || return 1
	_rc_match "$info" "$2" "$3" "$4"
}

# _rc_check_file <path> <uid> <gid> <mode> does the same for a regular
# file.
_rc_check_file() {
	local info

	[ -f "$1" ] && [ ! -L "$1" ] || return 1
	info=$(stat -c '%u %g %a' -- "$1") || return 1
	_rc_match "$info" "$2" "$3" "$4"
}

# _rc_write <file> copies the rest of stdin to a file that must not
# exist. Any existing name is refused first, a symlink included, because
# the shell's noclobber check skips O_EXCL when the name leads to
# something that is not a regular file, such as a FIFO or a device, and
# would then follow the link. noclobber still closes the race between
# that test and the open.
_rc_write() {
	[ ! -e "$1" ] && [ ! -L "$1" ] || return 1

	(
		set -C
		cat > "$1"
	) 2> /dev/null
}

# prepare writes the command on stdin to cmd.sh in a new directory for
# the task and prints its size and digest. It refuses a task directory
# that already exists as anything, and removes what it made when it stops
# before it is done.
prepare() {
	[ "$#" -eq 2 ] || return "$EXIT_USAGE"
	valid_task "$1" || return "$EXIT_USAGE"
	[[ $2 =~ $RC_USER_RE ]] || return "$EXIT_USAGE"

	local task=$1 root gid base dir file bytes digest

	root=$(_cp_root) || return "$EXIT_REFUSED"
	_cp_secure_chain "$root" "$RC_DATA" || return "$EXIT_REFUSED"
	gid=$(_rc_vcap_gid) || return "$EXIT_REFUSED"

	base=$root$RC_DATA/runcmd
	dir=$base/$task
	file=$dir/cmd.sh

	if [ ! -e "$base" ] && [ ! -L "$base" ]; then
		mkdir -m 0711 -- "$base" 2> /dev/null || return "$EXIT_REFUSED"
		chmod -- 00711 "$base" || return 1
	fi

	_rc_check_dir "$base" 0 - 711 || return "$EXIT_REFUSED"

	if [ -e "$dir" ] || [ -L "$dir" ]; then
		return "$EXIT_REFUSED"
	fi

	mkdir -m 0700 -- "$dir" 2> /dev/null || return "$EXIT_REFUSED"
	_RC_DROP=$dir

	chgrp -- "$gid" "$dir" || return 1
	chmod -- 00710 "$dir" || return 1
	_rc_check_dir "$dir" 0 "$gid" 710 || return "$EXIT_REFUSED"

	_rc_write "$file" || return "$EXIT_REFUSED"
	_rc_check_file "$file" 0 - 600 || return "$EXIT_REFUSED"
	chgrp -- "$gid" "$file" || return 1
	chmod -- 00640 "$file" || return 1
	_rc_check_file "$file" 0 "$gid" 640 || return "$EXIT_REFUSED"

	# Some wc builds pad the count with blanks, which arithmetic drops.
	bytes=$(($(wc -c < "$file")))
	digest=$(raw_hash "$file") || return 1

	_RC_DROP=
	printf 'staged bytes=%d sha256=%s\n' "$bytes" "$digest"
}

# cleanup prints whether the command started and how it ended, and then
# removes the task directory. started is yes when exec left its marker,
# and exit is the code exec recorded, or - when there is none. It always
# prints exactly one record. A task with no directory prints
# started=- and succeeds, because the directory may be missing either
# because prepare never arrived or because an earlier cleanup removed it,
# and the caller must treat the outcome as unknown. A directory that is a
# symlink, or is not root's, is refused and never removed.
cleanup() {
	[ "$#" -eq 1 ] || return "$EXIT_USAGE"
	valid_task "$1" || return "$EXIT_USAGE"

	local task=$1 root base dir started=no code=-

	root=$(_cp_root) || return "$EXIT_REFUSED"
	_cp_secure_chain "$root" "$RC_DATA" || return "$EXIT_REFUSED"

	base=$root$RC_DATA/runcmd
	dir=$base/$task

	if [ ! -e "$dir" ] && [ ! -L "$dir" ]; then
		printf 'cleanup started=- exit=- killed=no\n'
		return 0
	fi

	_rc_check_dir "$base" 0 - 711 || return "$EXIT_REFUSED"
	_rc_check_dir "$dir" 0 - - || return "$EXIT_REFUSED"

	if [ -f "$dir/started" ] && [ ! -L "$dir/started" ]; then
		started=yes
	fi

	if [ -f "$dir/exit" ] && [ ! -L "$dir/exit" ]; then
		IFS= read -r code < "$dir/exit" || true
		[[ $code =~ $RC_EXIT_RE ]] || code=-
	fi

	_RC_DROP=$dir
	printf 'cleanup started=%s exit=%s killed=no\n' "$started" "$code"
	rm -rf -- "$dir" || return 1
	_RC_DROP=
}

# execute runs the staged command as root. It checks again everything
# prepare made, compares the digest of cmd.sh with the one the broker
# measured, and refuses without leaving a started marker when anything is
# off. The refusal never names the command or its digest. Only root is
# accepted here. The command gets a new session, a deadline, an empty
# environment with fixed values, umask 022, stdin at end of file, and the
# root home as its working directory. The wrapper exits with the code the
# command ended with, which it also records in the exit file.
execute() {
	[ "$#" -eq 4 ] || return "$EXIT_USAGE"
	valid_task "$1" || return "$EXIT_USAGE"
	[ "$2" = root ] || return "$EXIT_USAGE"
	[[ $3 =~ $RC_SECS_RE ]] || return "$EXIT_USAGE"
	valid_hash "$4" || return "$EXIT_USAGE"

	local task=$1 secs=$3 want=$4 root gid base dir file home digest setsid timeout code

	root=$(_cp_root) || return "$EXIT_REFUSED"
	_cp_secure_chain "$root" "$RC_DATA" || return "$EXIT_REFUSED"
	gid=$(_rc_vcap_gid) || return "$EXIT_REFUSED"

	base=$root$RC_DATA/runcmd
	dir=$base/$task
	file=$dir/cmd.sh
	home=$root/root

	_rc_check_dir "$base" 0 - 711 || return "$EXIT_REFUSED"
	_rc_check_dir "$dir" 0 "$gid" 710 || return "$EXIT_REFUSED"
	_rc_check_file "$file" 0 "$gid" 640 || return "$EXIT_REFUSED"
	[ -d "$home" ] || return "$EXIT_REFUSED"

	digest=$(raw_hash "$file") || return "$EXIT_REFUSED"
	[ "$digest" = "$want" ] || return "$EXIT_REFUSED"

	setsid=$(_rc_tool setsid) || return "$EXIT_REFUSED"
	timeout=$(_rc_tool timeout) || return "$EXIT_REFUSED"
	command -v -- "$setsid" > /dev/null || return "$EXIT_REFUSED"
	command -v -- "$timeout" > /dev/null || return "$EXIT_REFUSED"

	_rc_write "$dir/started" < /dev/null || return "$EXIT_REFUSED"

	# The pid that bash -c records is the process group ID only because
	# setsid makes that process a session leader, and a session leader's
	# pid is its group ID. Keep the echo ahead of the exec in that same
	# process. timeout then fails its own setpgid as a session leader,
	# which is harmless, because the group is already the one to signal.
	# --wait keeps setsid from forking and returning at once, which would
	# hide the exit status of the command. A pgid write or cd that fails
	# after started ends with 125 and one fixed line, so it is not taken
	# for a command that exited 1. The brace group sends the command's
	# stderr through descriptor 3 and the shell's own job notice, such as
	# "Killed", to /dev/null. A command that outlives the kill-after is
	# killed together with timeout, and shows up as 137, not 124.
	# shellcheck disable=SC2016
	{
		"$setsid" --wait /bin/bash -c 'nostart() { echo "run.sh: the command was not started" >&2; exit 125; }; set -C; { echo $$ > "$1"; } 2> /dev/null || nostart; cd -- "$2" 2> /dev/null || nostart; umask 022; shift 2; exec "$@"' \
			_ "$dir/pgid" "$home" \
			"$timeout" -k 10 "$secs" \
			env -i HOME=/root USER=root LOGNAME=root SHELL=/bin/bash \
			PATH=/var/vcap/bosh/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
			LANG=C.UTF-8 LC_ALL=C.UTF-8 TMPDIR=/var/vcap/data/tmp \
			/bin/bash -- "$file" < /dev/null 2>&3
	} 3>&2 2> /dev/null
	code=$?

	# A failed write of the exit file leaves the command's own code as
	# the wrapper's, which the SSH status carries to the broker.
	printf '%s\n' "$code" | _rc_write "$dir/exit" || true
	return "$code"
}

# _rc_main runs the operation that its first argument names.
_rc_main() {
	local operation=${1:-}

	[ "$#" -eq 0 ] || shift

	case $operation in
	prepare) prepare "$@" ;;
	cleanup) cleanup "$@" ;;
	exec) execute "$@" ;;
	*) return "$EXIT_USAGE" ;;
	esac
}

# This line must stay the last line of the script, on one line, and end
# with a newline. prepare reads the command from the stdin that follows
# the script, and bash reads a script from stdin one line at a time. Bash
# runs this line only after it has read all of it, so the command is
# still unread when _rc_main starts and is never parsed as shell. Were
# the call and the exit on two lines, a successful cat would copy the
# exit line into cmd.sh as part of the command. Were the final newline
# missing, bash would glue the exit onto the first line of the command.
# exit with no argument keeps the operation's status, which the EXIT trap
# does not change.
_rc_main "$@"; exit
