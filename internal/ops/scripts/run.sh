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
#   cleanup prints  cleanup started=<yes|no|-> exit=<code or -> killed=<yes|no|unknown|survived>
#
# The directory of a task is root-owned with mode 0710 and the group of
# the vcap account, inside runcmd, which is root-owned with mode 0711. A
# user without the group can pass through runcmd by name but cannot list
# it, and vcap can open cmd.sh by name but cannot list, replace, write,
# or remove anything on the way to it. The task directory also holds the
# files a later operation writes, which are marker, started, starttime,
# pgid, and exit, the fence that cleanup leaves for a command that never
# started, and the survivors record of processes that outlived KILL.
# Beside the task directories, runcmd holds the done records that cleanup
# leaves, a root-owned file of mode 0600 named <task>.done, as cleanup
# describes.
#
# The wrapper needs GNU coreutils for mv -T, which the stemcell has.
# It needs util-linux flock too, for the lock that _rc_lock explains.
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
# The line a done record may hold, which is a cleanup line that shows the
# command ended, and the age in minutes, seven days, after which any
# cleanup removes a done record.
readonly RC_DONE_RE='^cleanup started=(yes|no) exit=(-|[0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5]) killed=(yes|no)$'
readonly RC_DONE_MINUTES=10080
# exec puts the entry RC_MARK=<task> into the command's environment, so
# cleanup can prove a process is the command's once the leader of its
# session has gone, as _rc_ours explains.
readonly RC_MARK=BLACKSMITH_RUN_TASK

# The directory that the EXIT trap removes when an operation stops before
# it is done. prepare sets it while the directory is half made, and
# cleanup sets it while the directory is being removed.
_RC_DROP=

# The descriptor of the task directory that _rc_lock holds the lock on.
_RC_LOCK=

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

# _rc_lock <dir> opens the task directory on descriptor _RC_LOCK and takes
# an exclusive flock on it. exec holds the lock from its last checks until
# it has written started, and cleanup holds it while it reads started and,
# when there is none, fences the task. So either exec writes started
# first and cleanup checks the command's session, or cleanup fences the
# task first and exec refuses. It fails when flock is missing or the lock
# can't be taken, and the caller then refuses.
_rc_lock() {
	local flock held now

	flock=$(_rc_tool flock) || return 1
	command -v -- "$flock" > /dev/null || return 1
	{ exec {_RC_LOCK}< "$1"; } 2> /dev/null || return 1
	"$flock" -x "$_RC_LOCK" 2> /dev/null || return 1

	# A directory removed or replaced while the lock was awaited is not
	# the one the caller checked. The inode number tells them apart,
	# because the open descriptor keeps the old one from being reused.
	held=$(stat -c %i -L -- "/dev/fd/$_RC_LOCK" 2> /dev/null) || return 1
	now=$(stat -c %i -- "$1" 2> /dev/null) || return 1
	[ "$held" = "$now" ]
}

# _rc_unlock closes the descriptor that _rc_lock opened, which releases
# the lock, so nothing started after it inherits the descriptor.
_rc_unlock() {
	exec {_RC_LOCK}<&-
	_RC_LOCK=
}

# _rc_fence <dir> marks a task whose command never started, so an exec that
# comes later refuses it. It writes the fence file, or removes cmd.sh when
# the fence can't be written, and fails when neither is done.
_rc_fence() {
	[ -f "$1/fence" ] && [ ! -L "$1/fence" ] && return 0
	_rc_write "$1/fence" < /dev/null && return 0
	rm -f -- "$1/cmd.sh" 2> /dev/null
	[ ! -e "$1/cmd.sh" ] && [ ! -L "$1/cmd.sh" ]
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

# _rc_leader_ok <pgid> <starttime> succeeds unless a process holds the
# leader's pid with another start time than exec recorded. Such a process
# shows that the command's group and session had no member left when it
# was forked, because the kernel never hands out the number of a live
# group or session as a pid. Linux fork(2) says the child's "PID does not
# match the ID of any existing process group (setpgid(2)) or session"
# (https://man7.org/linux/man-pages/man2/fork.2.html), and POSIX fork()
# says "the child process ID shall not match any active process group ID"
# (https://pubs.opengroup.org/onlinepubs/9799919799/functions/fork.html).
# A group that has emptied never gains a member again, because setpgid
# only joins a group that exists or makes one that the caller leads. A
# leader that is gone passes, because while the session has a member its
# number can't have been reused. Once the session has emptied, though, a
# stranger can take the number for a session of its own, as a daemon does
# when it forks, calls setsid, forks again, and exits, so a session whose
# leader is gone holds only the members _rc_ours proves are the command's.
_rc_leader_ok() {
	local lead f

	_rc_stat "/proc/$1/stat" lead || return 0
	read -ra f <<< "$lead"
	[ "${f[19]:-}" = "$2" ]
}

# _rc_led <pgid> <starttime> succeeds while the recorded leader still
# holds its pid with the start time exec recorded, as a zombie too. The
# session is then the command's, and so is every member of it.
_rc_led() {
	local lead f

	_rc_stat "/proc/$1/stat" lead || return 1
	read -ra f <<< "$lead"
	[ "${f[19]:-}" = "$2" ]
}

# _rc_ours <dir> <pid> succeeds when the task directory holds the marker
# file and the environment of the process holds the entry RC_MARK=<task>
# exactly. exec writes the marker file before it starts a command with
# that entry, so a directory from before the entry existed proves nothing.
# Cleanup reads the environment as root, and a read that fails proves
# nothing, nor does a process that cleared its environment or exec'd with
# another one.
_rc_ours() {
	local entry=$RC_MARK=${1##*/} e

	[ -f "$1/marker" ] && [ ! -L "$1/marker" ] || return 1

	{
		while IFS= read -r -d '' e || [ -n "$e" ]; do
			[ "$e" != "$entry" ] || return 0
		done < "/proc/$2/environ"
	} 2> /dev/null

	return 1
}

# _rc_proven <dir> <sid> sets proven, which the caller declares, to the
# pids of the live members of the session that _rc_ours proves are the
# command's, and fails when a live member can't be proven. A member's stat
# is read again after its environment, so a pid that a stranger took in
# between, with another start time, is never proven. A member that is a
# zombie on that second read, or ended in between, counts as neither.
_rc_proven() {
	local f pid fields ok unproven=0
	local -a p q

	proven=()

	for f in /proc/[0-9]*/stat; do
		_rc_stat "$f" fields || continue
		read -ra p <<< "$fields"

		[ "${p[3]:-}" = "$2" ] || continue

		pid=${f#/proc/}
		pid=${pid%/stat}
		ok=no
		! _rc_ours "$1" "$pid" || ok=yes

		_rc_stat "$f" fields || continue
		read -ra q <<< "$fields"
		[ "${q[0]}" != Z ] && [ "${q[3]:-}" = "$2" ] && [ "${q[19]:-}" = "${p[19]:-}" ] || continue

		if [ "$ok" = yes ]; then
			proven+=("$pid")
		else
			unproven=1
		fi
	done

	return "$unproven"
}

# _rc_unproven <pgid> <starttime> <dir> succeeds when the leader is gone
# and a live member of the session can't be proven the command's, so
# cleanup must not signal it and can't say the command ended. The caller
# checks _rc_alive first, which rules out a leader pid another process
# holds.
_rc_unproven() {
	local -a proven

	! _rc_led "$1" "$2" && ! _rc_proven "$3" "$1"
}

# _rc_reachable <pgid> <starttime> <dir> succeeds while the session has a
# member that _rc_signal would signal, which is any member while the
# leader lives, and only a proven one once it has gone.
_rc_reachable() {
	local -a proven

	_rc_alive "$1" "$2" || return 1
	! _rc_led "$1" "$2" || return 0
	_rc_proven "$3" "$1" || true
	[ "${#proven[@]}" -gt 0 ]
}

# _rc_alive <pgid> <starttime> succeeds while the session of the command
# still has a member.
_rc_alive() {
	local state

	_rc_leader_ok "$1" "$2" || return 1
	state=$(_rc_group_state "$1")
	[[ $state == live || $state == "live "* ]]
}

# _rc_signal <kill> <signal> <pgid> <starttime> <dir> sends a signal to
# the group and to the members of its session that are outside the group
# while the leader lives. Once the leader has gone, it sends the signal
# only to the members _rc_proven proves are the command's, and nothing to
# the rest. It checks the leader again first, so a number reused during
# the wait is never signalled.
_rc_signal() {
	local state extra
	local -a proven

	_rc_leader_ok "$3" "$4" || return 0
	state=$(_rc_group_state "$3")
	extra=${state#live}
	[ "$extra" != "$state" ] || return 0

	if _rc_led "$3" "$4"; then
		# shellcheck disable=SC2086
		"$1" "-$2" -- "-$3" $extra > /dev/null 2>&1 || true
		return 0
	fi

	_rc_proven "$5" "$3" || true
	[ "${#proven[@]}" -eq 0 ] || "$1" "-$2" -- "${proven[@]}" > /dev/null 2>&1 || true
}

# _rc_note_survivors <dir> <sid> writes the survivors record, one line of
# pid and start time, field 22 of its stat file, for each member of the
# session that is not a zombie, so a later cleanup can tell whether the
# same processes still live after their pids could have been reused. It
# fails when the session has no such member left. The record is written to
# survivors.new and renamed to survivors, so survivors never holds part of
# a record, as a write that fails on a full disk would leave it. A record
# that can't be written or renamed still counts as survivors, because the
# processes do live, and the survivors.new it leaves keeps every later
# cleanup reporting them, as cleanup describes.
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

	{ printf '%s' "$members" | _rc_write "$dir/survivors.new"; } &&
		mv -fT -- "$dir/survivors.new" "$dir/survivors" 2> /dev/null || true
}

# _rc_survivors_live <file> prints the pids of the survivors file whose
# process still lives with the start time the file recorded. A pid whose
# process is gone, is a zombie, or started at another time is one the
# kernel may have handed to a stranger, and is left out. It fails when the
# file is not a plain file, is empty, ends without a newline, or holds a
# line that is not a pid above 1 and a start time, because such a record
# proves nothing. An empty record or an unterminated last line may be the
# start of a record whose write failed, and read would skip that last line.
_rc_survivors_live() {
	local file=$1 pid want fields whole=''
	local -a p

	[ -f "$file" ] && [ ! -L "$file" ] || return 1
	{ IFS= read -r -d '' whole < "$file"; } 2> /dev/null
	[ -n "$whole" ] && [ "${whole: -1}" = $'\n' ] || return 1

	while read -r pid want; do
		_rc_id_ok "$pid" && [ "$((10#$pid))" -gt 1 ] && [[ $want =~ ^[1-9][0-9]{0,19}$ ]] || return 1

		_rc_stat "/proc/$pid/stat" fields || continue
		read -ra p <<< "$fields"
		[ "${p[0]}" != Z ] && [ "${p[19]:-}" = "$want" ] || continue

		printf '%s\n' "$pid"
	done < "$file"
}

# _rc_read_ids <dir> sets pg and want, which the caller declares, from
# the pgid and starttime files, and fails when either file is missing, is
# not a regular file, or does not hold a number that exec could have
# written. A pgid that is not a canonical number above 1 is never used,
# because kill -- -1 reaches every process the caller may signal.
_rc_read_ids() {
	pg='' want=''

	[ -f "$1/pgid" ] && [ ! -L "$1/pgid" ] && [ -f "$1/starttime" ] && [ ! -L "$1/starttime" ] || return 1

	IFS= read -r pg < "$1/pgid" || true
	IFS= read -r want < "$1/starttime" || true

	_rc_id_ok "$pg" && [ "$((10#$pg))" -gt 1 ] && [[ $want =~ ^[1-9][0-9]{0,19}$ ]]
}

# _rc_ended <pgid> <starttime> succeeds when nothing of the command's
# session is left but zombies, or a process holds the leader's pid with
# another start time, which _rc_leader_ok explains.
_rc_ended() {
	! _rc_leader_ok "$1" "$2" || [ "$(_rc_group_state "$1")" = none ]
}

# _rc_recheck <dir> prints yes or survived for a task whose earlier
# cleanup found processes that outlived KILL. The session is the unit,
# because a survivor that forked before it ended leaves children the
# survivors record never named. The processes the record names, with the
# start times it holds, get KILL again, and so does the command's group
# and the rest of its session, as _rc_signal sends it, whether or not a
# recorded survivor still lives. Once the leader has gone, a recorded
# survivor gets KILL only when _rc_ours proves it is the command's, as
# _rc_leader_ok explains. The task is reported yes only once none of the
# recorded survivors lives and the session has no member but zombies, or
# the leader's pid is held by another process. It is reported survived
# while a member lives, whether or not it could be signalled, and also
# when the record, the pgid file, or the starttime file can't be read or
# the kill can't be run, because nothing then shows that the processes
# ended.
_rc_recheck() {
	local dir=$1 kill live pg want ticks i pid
	local -a pids ours

	kill=$(_rc_tool kill) || { printf 'survived'; return 0; }
	command -v -- "$kill" > /dev/null || { printf 'survived'; return 0; }
	_rc_read_ids "$dir" || { printf 'survived'; return 0; }

	live=$(_rc_survivors_live "$dir/survivors") || { printf 'survived'; return 0; }

	if [ -n "$live" ]; then
		mapfile -t pids <<< "$live"
		ours=("${pids[@]}")

		if ! _rc_led "$pg" "$want"; then
			ours=()

			for pid in "${pids[@]}"; do
				! _rc_ours "$dir" "$pid" || ours+=("$pid")
			done
		fi

		[ "${#ours[@]}" -eq 0 ] || "$kill" -KILL -- "${ours[@]}" > /dev/null 2>&1 || true
	fi

	_rc_signal "$kill" KILL "$pg" "$want" "$dir"

	ticks=$(_rc_kill_ticks)

	for ((i = 0; i < ticks / 5 + 1; i++)); do
		live=$(_rc_survivors_live "$dir/survivors") || { printf 'survived'; return 0; }

		if [ -z "$live" ] && _rc_ended "$pg" "$want"; then
			printf 'yes'
			return 0
		fi

		sleep 0.1
	done

	printf 'survived'
}

# _rc_kill_group <dir> prints yes, no, unknown, or survived. It ends what
# is left of the command of a task whose exec started, whether or not the
# command recorded its exit, because nothing the command started should
# outlive it. The pgid and starttime files are untrusted, and when either
# is missing or invalid nothing is checked or signalled, and the answer
# is unknown.
#
# no means cleanup found nothing of the command's session alive, or found
# the leader's pid held by another process, which _rc_leader_ok explains,
# so nothing the command started still runs. A session that still has a
# member gets TERM, and KILL after the wait, as _rc_signal sends them, and
# is reported yes once it is gone, or survived when a member outlives the
# KILL, which leaves the survivors record that _rc_note_survivors writes.
# A session whose leader has gone and that still has a member _rc_ours
# can't prove is the command's is unknown, because that member may be a
# stranger's, and it gets no signal. A session that is
# cleanup's own, or a group with a member outside the session, which only
# a stranger that took the number can make, is unknown, as is a live
# session when there is no kill tool. None of these is signalled.
_rc_kill_group() {
	local dir=$1 pg want kill ticks i

	_rc_read_ids "$dir" || { printf 'unknown'; return 0; }
	_rc_leader_ok "$pg" "$want" || { printf 'no'; return 0; }

	case $(_rc_group_state "$pg") in
		none) printf 'no'; return 0 ;;
		live | "live "*) ;;
		*) printf 'unknown'; return 0 ;;
	esac

	kill=$(_rc_tool kill) || { printf 'unknown'; return 0; }
	command -v -- "$kill" > /dev/null || { printf 'unknown'; return 0; }

	ticks=$(_rc_kill_ticks)

	_rc_signal "$kill" TERM "$pg" "$want" "$dir"

	for ((i = 0; i < ticks; i++)); do
		_rc_reachable "$pg" "$want" "$dir" || break
		sleep 0.1
	done

	if _rc_reachable "$pg" "$want" "$dir"; then
		_rc_signal "$kill" KILL "$pg" "$want" "$dir"

		for ((i = 0; i < ticks / 5 + 1; i++)); do
			_rc_reachable "$pg" "$want" "$dir" || break
			sleep 0.1
		done
	fi

	if _rc_alive "$pg" "$want" && _rc_unproven "$pg" "$want" "$dir"; then
		printf 'unknown'
		return 0
	fi

	if _rc_alive "$pg" "$want" && _rc_note_survivors "$dir" "$pg"; then
		printf 'survived'
		return 0
	fi

	printf 'yes'
}

# _rc_replay <file> prints the line of a done record and succeeds, or
# fails when the file is not a record that cleanup could have written: a
# root-owned regular file of mode 0600 that holds exactly one line, ending
# in a newline, with killed=yes or no.
_rc_replay() {
	local whole=''

	_rc_check_file "$1" 0 - 600 || return 1
	{ IFS= read -r -d '' whole < "$1"; } 2> /dev/null
	[ "${whole: -1}" = $'\n' ] || return 1
	[[ ${whole%$'\n'} =~ $RC_DONE_RE ]] || return 1
	printf '%s' "$whole"
}

# _rc_write_done <base> <task> <line> writes the done record of a task. It
# writes <task>.done.new, which it clears first of what an earlier failed
# write left, and renames it to <task>.done, so the record never holds
# part of a line under its own name. It fails, and leaves neither name,
# when the write or the rename fails.
_rc_write_done() {
	local new=$1/$2.done.new

	rm -f -- "$new" 2> /dev/null

	if ! printf '%s\n' "$3" | _rc_write "$new" ||
		! mv -fT -- "$new" "$1/$2.done" 2> /dev/null; then
		rm -f -- "$new" 2> /dev/null
		return 1
	fi
}

# _rc_prune_done <base> removes the done records, and the part-written
# ones that a failed write leaves, that are older than RC_DONE_MINUTES.
# It removes only regular files with those names, never a task directory,
# because a directory that cleanup kept may still hold a command it could
# not check.
_rc_prune_done() {
	find "$1" -mindepth 1 -maxdepth 1 -type f \( -name '*.done' -o -name '*.done.new' \) \
		-mmin "+$RC_DONE_MINUTES" -delete 2> /dev/null || true
}

# cleanup prints whether the command started and how it ended, and then
# replaces the task directory with a done record of what it printed.
# started is yes when exec left its marker, and exit is the code exec
# recorded, or - when there is none. It reads started under the lock that
# _rc_lock takes, and fences a task without it before it lets the lock go,
# so a command it reports as never started can't start afterwards. An exit
# file that is empty or holds anything but a status, as a write on a full
# disk can leave, counts as none. Whenever the command started, cleanup
# checks its session, whatever the exit file holds, because a command with
# no exit is still running or was left by a wrapper that died, and one
# with an exit may have left children behind. It ends what it finds before
# it reports, so killed is yes, or survived when the session outlives
# KILL. killed is no when cleanup found the command's session empty, or
# the command never started, and unknown when it could not look, as
# _rc_kill_group says.
#
# An unknown keeps the directory without cmd.sh, as survivors do, so the
# next cleanup looks again and removes the directory once it can tell.
# Survivors keep the directory, with their record in place of the
# command's text, so the next cleanup checks the same processes again, as
# _rc_recheck does, whatever the exit file holds, and removes the
# directory only once they have ended. A survivors.new with no survivors
# beside it is a record that could not be written or renamed, which names
# no processes that can be checked, so every later cleanup reports
# survived and keeps the directory.
#
# Any other answer shows the command ended. cleanup writes it to the done
# record <task>.done, as _rc_write_done does, and only then removes the
# directory, so a cleanup asked again for the task prints the same line,
# even when the first answer was lost on the way or the broker could not
# save it. When the record can't be written, cleanup prints the line and
# keeps the directory as it was, so the next cleanup works the answer out
# again. A done record that is not one cleanup could have written is
# ignored. A done record whose task directory still holds started is not
# replayed, because only a check of the session shows that command ended,
# so cleanup answers from the directory as if there were no record. Every
# cleanup first removes done records older than seven days, as
# _rc_prune_done does, and never a task directory.
#
# It prints exactly one record unless it refuses. A task with neither a
# done record nor a directory prints started=- and succeeds, because the
# directory may be missing either because prepare never arrived or
# because its done record was pruned, and the caller must treat the
# outcome as unknown. A directory that is a symlink, or is not root's, is
# refused and never removed, and so is a task whose lock can't be taken,
# which includes a VM without flock.
cleanup() {
	[ "$#" -eq 1 ] || return "$EXIT_USAGE"
	valid_task "$1" || return "$EXIT_USAGE"

	local task=$1 root base dir line started=no code=- killed=no

	root=$(_cp_root) || return "$EXIT_REFUSED"
	_cp_secure_chain "$root" "$RC_DATA" || return "$EXIT_REFUSED"

	base=$root$RC_DATA/runcmd
	dir=$base/$task

	if [ -e "$base" ] || [ -L "$base" ]; then
		_rc_check_dir "$base" 0 - 711 || return "$EXIT_REFUSED"
		_rc_prune_done "$base"

		if line=$(_rc_replay "$base/$task.done"); then
			if ! _rc_check_dir "$dir" 0 - -; then
				printf '%s\n' "$line"
				return 0
			fi

			_rc_lock "$dir" || return "$EXIT_REFUSED"

			if [ ! -e "$dir/started" ] && [ ! -L "$dir/started" ]; then
				printf '%s\n' "$line"
				rm -rf -- "$dir" 2> /dev/null || true
				return 0
			fi

			_rc_unlock
		fi
	fi

	if [ ! -e "$dir" ] && [ ! -L "$dir" ]; then
		printf 'cleanup started=- exit=- killed=no\n'
		return 0
	fi

	_rc_check_dir "$dir" 0 - - || return "$EXIT_REFUSED"
	_rc_lock "$dir" || return "$EXIT_REFUSED"

	if [ -f "$dir/started" ] && [ ! -L "$dir/started" ]; then
		started=yes
	else
		_rc_fence "$dir" || return "$EXIT_REFUSED"
	fi

	_rc_unlock

	if [ -f "$dir/exit" ] && [ ! -L "$dir/exit" ]; then
		IFS= read -r code < "$dir/exit" || true
		[[ $code =~ $RC_EXIT_RE ]] || code=-
	fi

	if [ -e "$dir/survivors" ] || [ -L "$dir/survivors" ]; then
		killed=$(_rc_recheck "$dir")
	elif [ -e "$dir/survivors.new" ] || [ -L "$dir/survivors.new" ]; then
		killed=survived
	elif [ "$started" = yes ]; then
		killed=$(_rc_kill_group "$dir")
	fi

	if [ "$killed" = survived ] || [ "$killed" = unknown ]; then
		rm -f -- "$dir/cmd.sh" || true
		printf 'cleanup started=%s exit=%s killed=%s\n' "$started" "$code" "$killed"
		return 0
	fi

	line="cleanup started=$started exit=$code killed=$killed"

	if ! _rc_write_done "$base" "$task" "$line"; then
		printf '%s\n' "$line"
		return 0
	fi

	_RC_DROP=$dir
	printf '%s\n' "$line"
	# A command that was just ended lets its wrapper write the exit file
	# while the removal runs, which can fail the removal once.
	rm -rf -- "$dir" || { sleep 0.2; rm -rf -- "$dir"; } || return 1
	_RC_DROP=
}

# execute runs the staged command as root or vcap. It checks again
# everything prepare made, compares the digest of cmd.sh with the one the
# broker measured, and refuses without leaving a started marker when
# anything is off. It makes those checks and writes started under the lock
# that _rc_lock takes, and it also refuses there a task that cleanup
# fenced or wrote a done record for, so a command never starts after a
# cleanup reported that it had not. The refusal never names the command or
# its digest. For vcap it also refuses an account that getent does not
# know, one with uid or gid 0, a home that is missing, a symlink, or owned
# by anyone but the uid and gid of the account, and a home whose parent
# directories are not all root-owned and closed to writes by anyone else.
# The command gets a new session, a deadline, an empty environment with
# fixed values, umask 022, stdin at end of file, and the home of its user
# as its working directory. The vcap command runs under setpriv, inside
# timeout, with no supplementary groups and no way to gain privileges
# through setuid programs. The wrapper exits with the code the command
# ended with, which it also records in the exit file.
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

	setsid=$(_rc_tool setsid) || return "$EXIT_REFUSED"
	timeout=$(_rc_tool timeout) || return "$EXIT_REFUSED"
	command -v -- "$setsid" > /dev/null || return "$EXIT_REFUSED"
	command -v -- "$timeout" > /dev/null || return "$EXIT_REFUSED"

	if [ "$user" = vcap ]; then
		setpriv=$(_rc_tool setpriv) || return "$EXIT_REFUSED"
		command -v -- "$setpriv" > /dev/null || return "$EXIT_REFUSED"
		priv=("$setpriv" "--reuid=$uid" "--regid=$gid" --clear-groups --no-new-privs --)
	fi

	_rc_check_dir "$base" 0 - 711 || return "$EXIT_REFUSED"
	_rc_check_dir "$dir" 0 "$gid" 710 || return "$EXIT_REFUSED"

	# A refusal below leaves the lock to the exit of the wrapper, which
	# starts nothing first.
	_rc_lock "$dir" || return "$EXIT_REFUSED"
	[ ! -e "$dir/fence" ] && [ ! -L "$dir/fence" ] || return "$EXIT_REFUSED"
	[ ! -e "$base/$task.done" ] && [ ! -L "$base/$task.done" ] || return "$EXIT_REFUSED"
	_rc_check_dir "$dir" 0 "$gid" 710 || return "$EXIT_REFUSED"
	_rc_check_file "$file" 0 "$gid" 640 || return "$EXIT_REFUSED"
	[ -d "$home" ] || return "$EXIT_REFUSED"

	digest=$(raw_hash "$file") || return "$EXIT_REFUSED"
	[ "$digest" = "$want" ] || return "$EXIT_REFUSED"

	# The marker file says the command gets the RC_MARK entry, so it goes
	# in ahead of started, and a started directory without it is one from
	# before the entry, whose members _rc_ours can't prove. The lock goes
	# before the launch, so the command never holds it.
	_rc_write "$dir/marker" < /dev/null || return "$EXIT_REFUSED"
	_rc_write "$dir/started" < /dev/null || return "$EXIT_REFUSED"
	_rc_unlock

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
			"$RC_MARK=$task" \
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
