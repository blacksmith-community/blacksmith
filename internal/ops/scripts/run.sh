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
#   cleanup prints  cleanup started=<yes|no|-> exit=<code or -> killed=<yes|no|survived>
#
# The directory of a task is root-owned with mode 0710 and the group of
# the vcap account, inside runcmd, which is root-owned with mode 0711. A
# user without the group can pass through runcmd by name but cannot list
# it, and vcap can open cmd.sh by name but cannot list, replace, write,
# or remove anything on the way to it. The task directory also holds the
# files a later operation writes, which are started, starttime, pgid, and
# exit.
#
# CERTPATCH_ROOT lets the tests put the VM's files under a temporary
# directory, as lib.sh explains.

readonly RC_DATA=/var/vcap/data
readonly RC_USER_RE='^(root|vcap)$'
readonly RC_SECS_RE='^[1-9][0-9]{0,5}$'
# A user or group ID the script will pass to setpriv: a canonical decimal
# that is not zero and is below 4294967295, which setpriv reads as
# "unchanged". Larger values wrap around in setpriv, so they are refused.
readonly RC_ID_RE='^[1-9][0-9]{0,9}$'
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

# _rc_id_ok <id> accepts a user or group ID that setpriv can take.
_rc_id_ok() {
	[[ $1 =~ $RC_ID_RE ]] && ((10#$1 < 4294967295))
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

	_rc_id_ok "$gid" || return 1
	printf '%s' "$gid"
}

# _rc_vcap_entry prints "uid gid home" for the vcap account from getent,
# which the tests replace with a fake under the test root. It refuses an
# entry with no vcap name, a uid or gid that is not a canonical number or
# is 0, and a home that is not a plain absolute path, because the home
# goes into the command's environment.
_rc_vcap_entry() {
	local entry name uid gid home

	entry=$("$(_rc_tool getent)" passwd vcap) || return 1
	IFS=: read -r name _ uid gid _ home _ <<< "$entry"

	[ "$name" = vcap ] || return 1
	_rc_id_ok "$uid" || return 1
	_rc_id_ok "$gid" || return 1
	[[ $home =~ ^/[A-Za-z0-9._/-]+$ ]] || return 1
	[[ /$home/ != */../* ]] || return 1

	printf '%s %s %s' "$uid" "$gid" "$home"
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
# that test and the open. A refusal exits 13, and a write that fails
# after the name was free, such as a full disk, exits 1.
_rc_write() {
	[ ! -e "$1" ] && [ ! -L "$1" ] || return "$EXIT_REFUSED"

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

	_rc_write "$file" || return $?
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

# _rc_kill_ticks prints how many tenths of a second cleanup waits for a
# group to end after TERM. It is a hundred, ten seconds. The tests may
# shorten it through RC_KILL_TICKS, but only under the test root, because
# in production sudo resets the environment and the value is never read.
_rc_kill_ticks() {
	if [ -n "${CERTPATCH_ROOT:-}" ] && [[ ${RC_KILL_TICKS:-} =~ ^[1-9][0-9]{0,3}$ ]]; then
		printf '%s' "$RC_KILL_TICKS"
		return 0
	fi

	printf '100'
}

# _rc_stat <path> <var> sets var to the fields of a /proc stat file that
# follow the command name, which start with the state. The file is read
# whole and split after its last ") ", because the name may hold a
# newline or a ")" that any process can plant by writing to its comm, and
# a line-by-line read would take the fields from the planted text. The
# kernel's own fields after the name hold no parenthesis.
_rc_stat() {
	local rec=''

	{ IFS= read -r -d '' rec < "$1"; } 2> /dev/null || [ -n "$rec" ] || return 1
	printf -v "$2" '%s' "${rec##*) }"
}

# _rc_group_state <pgid> prints what cleanup may do about the processes
# of the command's session, and after the word "live" the pids of the
# members that are not in the group.
#
#   none  no process outside the zombies is in the session
#   bad   the pgid is not a number above 1
#   self  the group or its session is the one cleanup runs in
#   other the group has a member outside the session that setsid made
#   live  the session is alive, which is the command and its children
#
# setsid made the leader of the command's group a session leader, so
# every member of the group has the group ID as its session ID. A group
# ID that a stranger's process group has taken over since will have
# another session, and is never signalled. A child that moved to a group
# of its own with setpgid stays in the session and is found by it. One
# that called setsid again has left the session and is out of reach.
_rc_group_state() {
	local pg=$1 fields f state pgrp sess found=none mypg mysid extra=''

	if ! _rc_id_ok "$pg" || [ "$((10#$pg))" -le 1 ]; then
		printf 'bad'
		return 0
	fi

	_rc_stat "/proc/$BASHPID/stat" fields || { printf 'self'; return 0; }
	read -r _ _ mypg mysid _ <<< "$fields"

	if [ "$pg" = "$mypg" ] || [ "$pg" = "$mysid" ]; then
		printf 'self'
		return 0
	fi

	for f in /proc/[0-9]*/stat; do
		_rc_stat "$f" fields || continue
		read -r state _ pgrp sess _ <<< "$fields"

		[ "$state" != Z ] || continue

		if [ "$pgrp" = "$pg" ] && [ "$sess" != "$pg" ]; then
			printf 'other'
			return 0
		fi

		[ "$sess" = "$pg" ] || continue

		found=live

		if [ "$pgrp" != "$pg" ]; then
			f=${f#/proc/}
			extra+=" ${f%/stat}"
		fi
	done

	printf '%s%s' "$found" "$extra"
}

# _rc_leader_ok <pgid> <starttime> succeeds unless the process now
# leading the group started at another time than exec recorded. It runs
# only after cleanup has found the recorded leader, so a leader that has
# gone since then passes, because the session has been unbroken from the
# moment it was shown to be the command's. A different start time means
# the session ended and a stranger took the number.
_rc_leader_ok() {
	local lead f

	_rc_stat "/proc/$1/stat" lead || return 0
	read -ra f <<< "$lead"
	[ "${f[19]:-}" = "$2" ]
}

# _rc_alive <pgid> <starttime> succeeds while the session of the command
# still has a member.
_rc_alive() {
	local state

	_rc_leader_ok "$1" "$2" || return 1
	state=$(_rc_group_state "$1")
	[[ $state == live || $state == "live "* ]]
}

# _rc_signal <kill> <signal> <pgid> <starttime> sends a signal to the
# group and to the members of its session that are outside the group. It
# checks the leader again first, so a number reused during the wait is
# never signalled.
_rc_signal() {
	local state extra

	_rc_leader_ok "$3" "$4" || return 0
	state=$(_rc_group_state "$3")
	extra=${state#live}
	[ "$extra" != "$state" ] || return 0

	# shellcheck disable=SC2086
	"$1" "-$2" -- "-$3" $extra > /dev/null 2>&1 || true
}

# _rc_note_survivors <dir> <sid> writes the survivors record, one line of
# pid and start time, field 22 of its stat file, for each member of the
# session that is not a zombie, so a later cleanup can tell whether the
# same processes still live after their pids could have been reused. It
# fails when the session has no such member left. A record that can't be
# written still counts as survivors, because the processes do live.
_rc_note_survivors() {
	local dir=$1 sid=$2 f fields members=''
	local -a p

	for f in /proc/[0-9]*/stat; do
		_rc_stat "$f" fields || continue
		read -ra p <<< "$fields"

		[ "${p[0]}" != Z ] && [ "${p[3]:-}" = "$sid" ] && [ -n "${p[19]:-}" ] || continue

		f=${f#/proc/}
		members+="${f%/stat} ${p[19]}"$'\n'
	done

	[ -n "$members" ] || return 1

	printf '%s' "$members" | _rc_write "$dir/survivors" || true
}

# _rc_survivors_live <file> prints the pids of the survivors file whose
# process still lives with the start time the file recorded. A pid whose
# process is gone, is a zombie, or started at another time is one the
# kernel may have handed to a stranger, and is left out. It fails when the
# file is not a plain file or holds a line that is not a pid above 1 and a
# start time, because such a record proves nothing.
_rc_survivors_live() {
	local file=$1 pid want fields
	local -a p

	[ -f "$file" ] && [ ! -L "$file" ] || return 1

	while read -r pid want; do
		_rc_id_ok "$pid" && [ "$((10#$pid))" -gt 1 ] && [[ $want =~ ^[1-9][0-9]{0,19}$ ]] || return 1

		_rc_stat "/proc/$pid/stat" fields || continue
		read -ra p <<< "$fields"
		[ "${p[0]}" != Z ] && [ "${p[19]:-}" = "$want" ] || continue

		printf '%s\n' "$pid"
	done < "$file"
}

# _rc_recheck <dir> prints yes or survived for a task whose earlier
# cleanup found processes that outlived KILL. Only the processes the
# survivors record names, with the start times it holds, are ours. Those
# that still live get KILL again, and the task is reported yes once none
# lives, or survived while any does. A record that can't be read, or a
# kill that can't be run, is reported survived, because nothing then
# shows that the processes ended.
_rc_recheck() {
	local dir=$1 kill live ticks i
	local -a pids

	kill=$(_rc_tool kill) || { printf 'survived'; return 0; }
	command -v -- "$kill" > /dev/null || { printf 'survived'; return 0; }

	live=$(_rc_survivors_live "$dir/survivors") || { printf 'survived'; return 0; }
	[ -n "$live" ] || { printf 'yes'; return 0; }

	mapfile -t pids <<< "$live"
	"$kill" -KILL -- "${pids[@]}" > /dev/null 2>&1 || true

	ticks=$(_rc_kill_ticks)

	for ((i = 0; i < ticks / 5 + 1; i++)); do
		live=$(_rc_survivors_live "$dir/survivors") || { printf 'survived'; return 0; }
		[ -n "$live" ] || { printf 'yes'; return 0; }
		sleep 0.1
	done

	printf 'survived'
}

# _rc_kill_group <dir> prints yes, no, or survived. It ends the command
# of a task whose exec started and left no exit. The pgid and starttime
# files are untrusted. A pgid that is not a canonical number above 1 is
# never used, because kill -- -1 reaches every process the caller may
# signal. The only proof that a session is the command's is its leader,
# a live process with the start time exec recorded. A session whose
# leader has gone is left alone and reported no, because a pid that was
# reused since, by a daemon that called setsid and whose leader then
# exited, looks the same. A session that is gone, that is cleanup's own,
# or whose leader is not the process exec recorded is left alone too. A live
# session gets TERM, and KILL after the wait, and is reported yes once it
# is gone, or survived when a member outlives the KILL, which leaves the
# survivors record that _rc_note_survivors writes.
_rc_kill_group() {
	local dir=$1 pg='' want='' kill lead f ticks i

	if [ ! -f "$dir/pgid" ] || [ -L "$dir/pgid" ] ||
		[ ! -f "$dir/starttime" ] || [ -L "$dir/starttime" ]; then
		printf 'no'
		return 0
	fi

	IFS= read -r pg < "$dir/pgid" || true
	IFS= read -r want < "$dir/starttime" || true

	if ! _rc_id_ok "$pg" || [ "$((10#$pg))" -le 1 ] || [[ ! $want =~ ^[1-9][0-9]{0,19}$ ]]; then
		printf 'no'
		return 0
	fi

	kill=$(_rc_tool kill) || { printf 'no'; return 0; }
	command -v -- "$kill" > /dev/null || { printf 'no'; return 0; }

	# The leader must be there and be the process exec recorded. Field 22
	# of its stat file is the 20th after the name. Without the leader's
	# start time nothing proves that a session is ours, and its members
	# are left running.
	_rc_stat "/proc/$pg/stat" lead || { printf 'no'; return 0; }
	read -ra f <<< "$lead"
	[ "${f[19]:-}" = "$want" ] || { printf 'no'; return 0; }

	_rc_alive "$pg" "$want" || { printf 'no'; return 0; }

	ticks=$(_rc_kill_ticks)

	_rc_signal "$kill" TERM "$pg" "$want"

	for ((i = 0; i < ticks; i++)); do
		_rc_alive "$pg" "$want" || break
		sleep 0.1
	done

	if _rc_alive "$pg" "$want"; then
		_rc_signal "$kill" KILL "$pg" "$want"

		for ((i = 0; i < ticks / 5 + 1; i++)); do
			_rc_alive "$pg" "$want" || break
			sleep 0.1
		done
	fi

	if _rc_alive "$pg" "$want" && _rc_note_survivors "$dir" "$pg"; then
		printf 'survived'
		return 0
	fi

	printf 'yes'
}

# cleanup prints whether the command started and how it ended, and then
# removes the task directory. started is yes when exec left its marker,
# and exit is the code exec recorded, or - when there is none. A command
# that started and has no exit is still running, or was left by a wrapper
# that died, and cleanup ends its process group before it reports, so
# killed is yes, or survived when the group outlives KILL. Survivors keep
# the directory, with their record in place of the command's text, so the
# next cleanup checks the same processes again, as _rc_recheck does,
# whatever the exit file holds, and removes the directory only once they
# have ended. It always prints exactly one record. A task with no directory prints
# started=- and succeeds, because the directory may be missing either
# because prepare never arrived or because an earlier cleanup removed it,
# and the caller must treat the outcome as unknown. A directory that is a
# symlink, or is not root's, is refused and never removed.
cleanup() {
	[ "$#" -eq 1 ] || return "$EXIT_USAGE"
	valid_task "$1" || return "$EXIT_USAGE"

	local task=$1 root base dir started=no code=- killed=no

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

	if [ -e "$dir/survivors" ] || [ -L "$dir/survivors" ]; then
		killed=$(_rc_recheck "$dir")
	elif [ "$started" = yes ] && [ ! -e "$dir/exit" ] && [ ! -L "$dir/exit" ]; then
		killed=$(_rc_kill_group "$dir")
	fi

	if [ "$killed" = survived ]; then
		rm -f -- "$dir/cmd.sh" || true
		printf 'cleanup started=%s exit=%s killed=%s\n' "$started" "$code" "$killed"
		return 0
	fi

	_RC_DROP=$dir
	printf 'cleanup started=%s exit=%s killed=%s\n' "$started" "$code" "$killed"
	# A command that was just ended lets its wrapper write the exit file
	# while the removal runs, which can fail the removal once.
	rm -rf -- "$dir" || { sleep 0.2; rm -rf -- "$dir"; } || return 1
	_RC_DROP=
}

# execute runs the staged command as root or vcap. It checks again
# everything prepare made, compares the digest of cmd.sh with the one the
# broker measured, and refuses without leaving a started marker when
# anything is off. The refusal never names the command or its digest. For
# vcap it also refuses an account that getent does not know, one with uid
# or gid 0, a home that is missing, a symlink, or owned by anyone but the
# uid and gid of the account, and a home whose parent directories are not
# all root-owned and closed to writes by anyone else. The command gets a
# new session, a deadline, an empty environment with fixed values, umask
# 022, stdin at end of file, and the home of its user as its working
# directory. The vcap command runs under setpriv, inside timeout, with no
# supplementary groups and no way to gain privileges through setuid
# programs. The wrapper exits with the code the command ended with, which
# it also records in the exit file.
execute() {
	[ "$#" -eq 4 ] || return "$EXIT_USAGE"
	valid_task "$1" || return "$EXIT_USAGE"
	[[ $2 =~ $RC_USER_RE ]] || return "$EXIT_USAGE"
	[[ $3 =~ $RC_SECS_RE ]] || return "$EXIT_USAGE"
	valid_hash "$4" || return "$EXIT_USAGE"

	local task=$1 user=$2 secs=$3 want=$4 root gid base dir file home digest setsid timeout code
	local uid entry homepath setpriv path
	local -a priv=()

	root=$(_cp_root) || return "$EXIT_REFUSED"
	_cp_secure_chain "$root" "$RC_DATA" || return "$EXIT_REFUSED"

	base=$root$RC_DATA/runcmd
	dir=$base/$task
	file=$dir/cmd.sh
	home=$root/root
	homepath=/root
	path=/var/vcap/bosh/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

	if [ "$user" = vcap ]; then
		entry=$(_rc_vcap_entry) || return "$EXIT_REFUSED"
		IFS=' ' read -r uid gid homepath <<< "$entry"
		home=$root$homepath
		path=/var/vcap/bosh/bin:/usr/local/bin:/usr/bin:/bin

		# Only root may swap a directory on the way to the home between
		# this check and the cd.
		_cp_secure_chain "$root" "${homepath%/*}" || return "$EXIT_REFUSED"
		_rc_check_dir "$home" "$uid" "$gid" - || return "$EXIT_REFUSED"
	else
		gid=$(_rc_vcap_gid) || return "$EXIT_REFUSED"
	fi

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

	if [ "$user" = vcap ]; then
		setpriv=$(_rc_tool setpriv) || return "$EXIT_REFUSED"
		command -v -- "$setpriv" > /dev/null || return "$EXIT_REFUSED"
		priv=("$setpriv" "--reuid=$uid" "--regid=$gid" --clear-groups --no-new-privs --)
	fi

	_rc_write "$dir/started" < /dev/null || return "$EXIT_REFUSED"

	# The pid that bash -c records is the process group ID only because
	# setsid makes that process a session leader, and a session leader's
	# pid is its group ID. The start time of that process, field 22 of its
	# stat file, goes into the starttime file first, so cleanup can tell
	# the process from a later one that reuses its pid, and a pgid file
	# always has one beside it. A start time that cannot be read or
	# written ends in 125 like a pgid write that fails. Keep both writes
	# ahead of the exec in that same process. timeout then fails its own
	# setpgid as a session leader, which is harmless, because the group is
	# already the one to signal.
	# --wait keeps setsid from forking and returning at once, which would
	# hide the exit status of the command. A pgid write or cd that fails
	# after started ends with 125 and one fixed line, so it is not taken
	# for a command that exited 1, and it removes the started marker
	# first, so cleanup reports started=no and the broker tells the failed
	# launch from a command that ran and exited 125. The brace group sends the command's
	# stderr through descriptor 3 and the shell's own job notice, such as
	# "Killed", to /dev/null. The launch closes descriptor 3 for everything
	# it starts, so the command cannot write to the notice channel. A
	# command that outlives the kill-after is killed together with timeout,
	# and shows up as 137, not 124.
	# shellcheck disable=SC2016
	{
		"$setsid" --wait /bin/bash -c 'mark=$4; nostart() { rm -f -- "$mark"; echo "run.sh: the command was not started" >&2; exit 125; }; set -C; { IFS= read -r -d "" rec < /proc/$$/stat; rec=${rec##*) }; read -ra f <<< "$rec"; [ -n "${f[19]:-}" ] && echo "${f[19]}" > "$2" && echo $$ > "$1"; } 2> /dev/null || nostart; cd -- "$3" 2> /dev/null || nostart; umask 022; shift 4; exec "$@"' \
			_ "$dir/pgid" "$dir/starttime" "$home" "$dir/started" \
			"$timeout" -k 10 "$secs" ${priv[@]+"${priv[@]}"} \
			env -i HOME="$homepath" USER="$user" LOGNAME="$user" SHELL=/bin/bash \
			PATH="$path" \
			LANG=C.UTF-8 LC_ALL=C.UTF-8 TMPDIR=/var/vcap/data/tmp \
			/bin/bash -- "$file" < /dev/null 2>&3 3>&-
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
