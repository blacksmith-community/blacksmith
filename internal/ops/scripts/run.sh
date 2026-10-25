# run.sh stages and cleans up after one operator command on one VM. Each
# operation takes the task ID first and checks it before anything else.
#
#   prepare <task> <user>
#   cleanup <task>
#
# <user> is root or vcap, the account the command will run as. prepare
# reads the command from the stdin that follows this script and writes it
# byte for byte to /var/vcap/data/runcmd/<task>/cmd.sh. The command
# arrives as data on stdin and is never part of a command line, an
# argument, or a shell string, and nothing here runs, evals, or sources
# it.
#
#   prepare prints  staged bytes=<n> sha256=<hex>
#   cleanup prints  cleanup started=<yes|no> exit=<code or -> killed=<yes|no>
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
readonly RC_EXIT_RE='^[0-9]{1,3}$'

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
# exist. noclobber makes the shell open it with O_EXCL, which fails on
# any existing name and does not follow a symlink that is already there.
_rc_write() {
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
	fi

	_rc_check_dir "$base" 0 - 711 || return "$EXIT_REFUSED"

	if [ -e "$dir" ] || [ -L "$dir" ]; then
		return "$EXIT_REFUSED"
	fi

	mkdir -m 0700 -- "$dir" 2> /dev/null || return "$EXIT_REFUSED"
	_RC_DROP=$dir

	chgrp -- "$gid" "$dir" || return 1
	chmod -- 0710 "$dir" || return 1
	_rc_check_dir "$dir" 0 "$gid" 710 || return "$EXIT_REFUSED"

	_rc_write "$file" || return 1
	chgrp -- "$gid" "$file" || return 1
	chmod -- 0640 "$file" || return 1
	_rc_check_file "$file" 0 "$gid" 640 || return "$EXIT_REFUSED"

	# Some wc builds pad the count with blanks, which arithmetic drops.
	bytes=$(($(wc -c < "$file")))
	digest=$(raw_hash "$file") || return 1

	_RC_DROP=
	printf 'staged bytes=%d sha256=%s\n' "$bytes" "$digest"
}

# cleanup prints whether the command started and how it ended, and then
# removes the task directory. started is yes when exec left its marker,
# and exit is the code exec recorded, or - when there is none. A task
# with no directory has nothing to report, so it prints nothing and
# succeeds, which lets a repeated cleanup finish quietly. A directory
# that is a symlink, or is not root's, is refused and never removed.
cleanup() {
	[ "$#" -eq 1 ] || return "$EXIT_USAGE"
	valid_task "$1" || return "$EXIT_USAGE"

	local task=$1 root base dir started=no code=-

	root=$(_cp_root) || return "$EXIT_REFUSED"
	_cp_secure_chain "$root" "$RC_DATA" || return "$EXIT_REFUSED"

	base=$root$RC_DATA/runcmd
	dir=$base/$task

	if [ ! -e "$dir" ] && [ ! -L "$dir" ]; then
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

# _rc_main runs the operation that its first argument names.
_rc_main() {
	local operation=${1:-}

	[ "$#" -eq 0 ] || shift

	case $operation in
	prepare) prepare "$@" ;;
	cleanup) cleanup "$@" ;;
	*) return "$EXIT_USAGE" ;;
	esac
}

# This line must stay the last line of the script, on one line with
# nothing after it. prepare reads the command from the stdin that follows
# the script, and bash reads a script from stdin one line at a time. Bash
# runs this line only after it has read all of it, and it has read every
# line above by then, so the command is still unread when _rc_main starts
# and is never parsed as shell. Were the call split over lines, bash could
# read into the command. exit with no argument keeps the operation's
# status, which the EXIT trap does not change.
_rc_main "$@"; exit
