# patch.sh changes the certificate and key files of one VM in place. Each
# operation takes the task ID first and checks it before anything else.
#
#   prepare <task>
#   stage <task> <staged>
#   check <task> <jobdir> <path> <old> <new> <backup>
#   stagecheck <task> <staged> <new>
#   backup <task> <jobdir> <path> <old> <backup>
#   replace <task> <jobdir> <path> <staged> <old> <new>
#   restore <task> <jobdir> <path> <backup> <old> <new>
#   restart <task> <process>...
#   cleanup <task> [<jobdir> <path>]...
#
# <path> is a pinned target under <jobdir>, which discovery recorded.
# <staged> is the task's staging path s/<n> and <backup> its backup path
# b/<n>. <old> and <new> are normalised hashes. Each operation prints
# lines that start with ok, drift, or unrecovered and name the file or
# process, and never prints file content.
#
# The broker runs prepare, then stage, then the other operations, all on
# one connection to the VM. prepare makes the task's directories through
# make_work_dir, and stage runs valid_work on them before it writes, so
# nothing is staged into a directory that is not private to root.
#
# stage reads the content that follows this script on stdin. The script
# starts its operation from its last line, so bash has read every line
# of the script before stage reads, and the content is never run.
#
# Every write goes to a new temporary file that is synced and then
# renamed over its target, so a target always holds either its whole old
# content or its whole new content. A temporary file beside a target is
# named .certpatch.<task>.<random>, so cleanup can find what an
# interrupted operation of the task left there.

# The temporary file that the EXIT trap removes when an operation stops
# before its rename.
_PT_TMP=

# The EXIT trap calls this, which shellcheck does not follow.
# shellcheck disable=SC2329
_pt_drop_tmp() {
	if [ -n "$_PT_TMP" ]; then
		rm -f -- "$_PT_TMP"
	fi
}

trap _pt_drop_tmp EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

readonly PT_PROC_RE='^[A-Za-z0-9][A-Za-z0-9_.:@+-]*$'
readonly PT_TIMEOUT_RE='^[1-9][0-9]{0,3}$'
readonly PT_MONIT=/var/vcap/bosh/bin/monit

# _pt_args <want> <got> returns the usage code unless the counts match.
_pt_args() {
	[ "$1" -eq "$2" ] || return "$EXIT_USAGE"
}

# _pt_hashes <old> <new> accepts two well-formed hashes that differ.
_pt_hashes() {
	valid_hash "$1" || return
	valid_hash "$2" || return
	[ "$1" != "$2" ] || return "$EXIT_USAGE"
}

# _pt_work <path> <task> <kind> accepts the task's work path in the s or
# b directory only, so a staging path never stands in for a backup or
# the other way round.
_pt_work() {
	[[ $1 == "$CP_WORK_ROOT/$2/$3/"* ]] || return "$EXIT_REFUSED"
	valid_work "$1" "$2"
}

# _pt_hash <file> prints the file's normalised hash, or nothing when it
# is missing or cannot be hashed, which every caller reads as drift.
_pt_hash() {
	norm_hash "$1" 2> /dev/null || true
}

# _pt_sync <file>... flushes each file, or directory, to disk.
_pt_sync() {
	sync -- "$@"
}

# _pt_swap <task> <source> <full> <want> writes the content of source
# over the target full through a temporary file in the target's
# directory, named for the task. The new file takes the target's owner,
# group, and mode, and must hash to want before the rename.
_pt_swap() {
	local task=$1 source=$2 full=$3 want=$4 dir info owner mode

	dir=${full%/*}
	info=$(stat -c '%u:%g %a' -- "$full") || return 1
	owner=${info% *}
	mode=${info#* }

	_PT_TMP=$(mktemp -- "$dir/.certpatch.$task.XXXXXXXXXX") || return 1
	cat -- "$source" > "$_PT_TMP" || return 1
	chown -- "$owner" "$_PT_TMP" || return 1
	chmod -- "$mode" "$_PT_TMP" || return 1
	[ "$(_pt_hash "$_PT_TMP")" = "$want" ] || return 1
	_pt_sync "$_PT_TMP" || return 1
	mv -f -T -- "$_PT_TMP" "$full" || return 1
	_PT_TMP=
	_pt_sync "$dir"
}

prepare() {
	_pt_args 1 "$#" || return
	valid_task "$1" || return

	local task=$1

	make_work_dir "$task" || return
	valid_work "$CP_WORK_ROOT/$task/s/0" "$task" || return
	valid_work "$CP_WORK_ROOT/$task/b/0" "$task" || return

	printf 'ok path=%s\n' "$CP_WORK_ROOT/$task"
}

# stage writes the content that follows the script on stdin to the
# task's staging path. It refuses a name that already exists as anything.
# The content goes to a temporary file that is synced and then linked to
# the staging path, so the path holds the whole content or nothing.
stage() {
	_pt_args 2 "$#" || return
	valid_task "$1" || return

	local task=$1 staged=$2 root full

	_pt_work "$staged" "$task" s || return
	root=$(_cp_root) || return "$EXIT_REFUSED"
	full=$root$staged

	if [ -e "$full" ] || [ -L "$full" ]; then
		return "$EXIT_REFUSED"
	fi

	_PT_TMP=$(mktemp -- "${full%/*}/.tmp.XXXXXXXXXX") || return 1
	cat > "$_PT_TMP" || return 1
	_pt_sync "$_PT_TMP" || return 1
	ln -- "$_PT_TMP" "$full" || return 1
	rm -f -- "$_PT_TMP" || return 1
	_PT_TMP=
	_pt_sync "${full%/*}" || return 1

	printf 'ok path=%s\n' "$staged"
}

check() {
	_pt_args 6 "$#" || return
	valid_task "$1" || return

	local task=$1 jobdir=$2 path=$3 old=$4 new=$5 backup=$6 root hash state saved=none

	_pt_hashes "$old" "$new" || return
	valid_pinned "$path" "$jobdir" || return
	_pt_work "$backup" "$task" b || return
	root=$(_cp_root) || return "$EXIT_REFUSED"

	hash=$(_pt_hash "$root$path")

	case $hash in
	"$old") state=old ;;
	"$new") state=new ;;
	*)
		printf 'drift path=%s\n' "$path"
		return "$EXIT_DRIFT"
		;;
	esac

	if [ -e "$root$backup" ]; then
		if [ "$(_pt_hash "$root$backup")" != "$old" ]; then
			printf 'drift path=%s\n' "$backup"
			return "$EXIT_BACKUP"
		fi

		saved=old
	fi

	printf 'ok path=%s state=%s backup=%s\n' "$path" "$state" "$saved"
}

stagecheck() {
	_pt_args 3 "$#" || return
	valid_task "$1" || return

	local task=$1 staged=$2 new=$3 root

	valid_hash "$new" || return
	_pt_work "$staged" "$task" s || return
	root=$(_cp_root) || return "$EXIT_REFUSED"

	if [ "$(_pt_hash "$root$staged")" != "$new" ]; then
		printf 'drift path=%s\n' "$staged"
		return "$EXIT_DRIFT"
	fi

	printf 'ok path=%s\n' "$staged"
}

# backup copies the target to a temporary file in the task's b directory
# and links it to the backup path. ln refuses a name that exists, so a
# backup is written once and never changed.
backup() {
	_pt_args 5 "$#" || return
	valid_task "$1" || return

	local task=$1 jobdir=$2 path=$3 old=$4 backup=$5 root full saved

	valid_hash "$old" || return
	valid_pinned "$path" "$jobdir" || return
	_pt_work "$backup" "$task" b || return
	root=$(_cp_root) || return "$EXIT_REFUSED"

	full=$root$path
	saved=$root$backup

	if [ -e "$saved" ]; then
		if [ "$(_pt_hash "$saved")" != "$old" ]; then
			printf 'drift path=%s\n' "$backup"
			return "$EXIT_BACKUP"
		fi

		printf 'ok path=%s backup=%s action=exists\n' "$path" "$backup"
		return 0
	fi

	if [ "$(_pt_hash "$full")" != "$old" ]; then
		printf 'drift path=%s\n' "$path"
		return "$EXIT_DRIFT"
	fi

	_PT_TMP=$(mktemp -- "${saved%/*}/.tmp.XXXXXXXXXX") || return 1
	cat -- "$full" > "$_PT_TMP" || return 1

	# The job can write the target while it is copied, so the copy is
	# checked, not the target.
	if [ "$(_pt_hash "$_PT_TMP")" != "$old" ]; then
		printf 'drift path=%s\n' "$path"
		return "$EXIT_DRIFT"
	fi

	_pt_sync "$_PT_TMP" || return 1
	ln -- "$_PT_TMP" "$saved" || return 1
	rm -f -- "$_PT_TMP" || return 1
	_PT_TMP=
	_pt_sync "${saved%/*}" || return 1

	printf 'ok path=%s backup=%s action=created\n' "$path" "$backup"
}

replace() {
	_pt_args 6 "$#" || return
	valid_task "$1" || return

	local task=$1 jobdir=$2 path=$3 staged=$4 old=$5 new=$6 root full hash

	_pt_hashes "$old" "$new" || return
	valid_pinned "$path" "$jobdir" || return
	_pt_work "$staged" "$task" s || return
	root=$(_cp_root) || return "$EXIT_REFUSED"

	full=$root$path
	hash=$(_pt_hash "$full")

	if [ "$hash" = "$new" ]; then
		printf 'ok path=%s state=new action=unchanged\n' "$path"
		return 0
	fi

	if [ "$hash" != "$old" ]; then
		printf 'drift path=%s\n' "$path"
		return "$EXIT_DRIFT"
	fi

	if [ "$(_pt_hash "$root$staged")" != "$new" ]; then
		printf 'drift path=%s\n' "$staged"
		return "$EXIT_DRIFT"
	fi

	_pt_swap "$task" "$root$staged" "$full" "$new" || return 1

	printf 'ok path=%s state=new action=replaced\n' "$path"
}

# restore puts the backup's content back over a target that holds the
# new content, and keeps the backup.
restore() {
	_pt_args 6 "$#" || return
	valid_task "$1" || return

	local task=$1 jobdir=$2 path=$3 backup=$4 old=$5 new=$6 root full hash

	_pt_hashes "$old" "$new" || return
	valid_pinned "$path" "$jobdir" || return
	_pt_work "$backup" "$task" b || return
	root=$(_cp_root) || return "$EXIT_REFUSED"

	full=$root$path
	hash=$(_pt_hash "$full")

	if [ "$hash" = "$old" ]; then
		printf 'ok path=%s state=old action=unchanged\n' "$path"
		return 0
	fi

	if [ "$hash" != "$new" ]; then
		printf 'unrecovered path=%s\n' "$path"
		return "$EXIT_DRIFT"
	fi

	if [ "$(_pt_hash "$root$backup")" != "$old" ]; then
		printf 'unrecovered path=%s\n' "$path"
		return "$EXIT_BACKUP"
	fi

	_pt_swap "$task" "$root$backup" "$full" "$old" || return 1

	printf 'ok path=%s state=old action=restored\n' "$path"
}

# _pt_running <summary> <process> succeeds when monit's summary shows
# the process as running. monit 5.2.5 prints a process as
# `Process 'name'` followed by blanks and its status.
_pt_running() {
	local line pattern

	pattern="^Process '([^']+)'[[:blank:]]+(.*[^[:blank:]])[[:blank:]]*$"

	while IFS= read -r line; do
		line=${line%$'\r'}

		if [[ $line =~ $pattern ]] && [ "${BASH_REMATCH[1]}" = "$2" ]; then
			[ "${BASH_REMATCH[2]}" = running ]
			return
		fi
	done <<< "$1"

	return 1
}

# _pt_pid <status> <process> prints the pid that monit's status gives the
# process, or nothing when it gives none. monit 5.2.5 prints a process as
# a `Process 'name'` line followed by indented lines, one of which is
# `pid` followed by blanks and the number.
_pt_pid() {
	local line inside=0 header field

	header="^Process '([^']+)'[[:blank:]]*$"
	field='^[[:blank:]]+pid[[:blank:]]+([0-9]+)[[:blank:]]*$'

	while IFS= read -r line; do
		line=${line%$'\r'}

		if [[ $line =~ ^[^[:blank:]] ]]; then
			inside=0
			[[ $line =~ $header ]] && [ "${BASH_REMATCH[1]}" = "$2" ] && inside=1
		elif [ "$inside" = 1 ] && [[ $line =~ $field ]]; then
			printf '%s' "${BASH_REMATCH[1]}"
			return 0
		fi
	done <<< "$1"
}

# restart records each process's pid from monit's status, asks monit to
# restart each process, and then polls every 2 seconds until monit's
# summary shows every one running and its status gives every one a pid
# other than the recorded one. A restart that monit accepts but never
# carries out leaves the old pid, so it never counts as done. restart
# gives up after 300 seconds, or CERTPATCH_RESTART_TIMEOUT in the tests,
# and names each process that is still waiting.
restart() {
	[ "$#" -ge 2 ] || return "$EXIT_USAGE"
	valid_task "$1" || return
	shift

	local root monit name status summary limit deadline pid i before=() waiting=()

	for name in "$@"; do
		[[ $name =~ $PT_PROC_RE ]] || return "$EXIT_USAGE"
	done

	limit=${CERTPATCH_RESTART_TIMEOUT:-300}
	[[ $limit =~ $PT_TIMEOUT_RE ]] || limit=300

	root=$(_cp_root) || return "$EXIT_REFUSED"
	monit=$root$PT_MONIT
	[ -f "$monit" ] && [ -x "$monit" ] || return "$EXIT_RESTART"

	deadline=$((SECONDS + limit))

	if ! status=$(timeout 30 "$monit" status 2> /dev/null); then
		printf 'drift name=%s\n' "$@"
		return "$EXIT_RESTART"
	fi

	for name in "$@"; do
		before+=("$(_pt_pid "$status" "$name")")
	done

	for name in "$@"; do
		if ! timeout 60 "$monit" restart "$name" > /dev/null 2>&1; then
			printf 'drift name=%s\n' "$name"
			return "$EXIT_RESTART"
		fi
	done

	while :; do
		sleep 2

		summary=$(timeout 30 "$monit" summary 2> /dev/null) || summary=
		status=$(timeout 30 "$monit" status 2> /dev/null) || status=
		waiting=()
		i=0

		for name in "$@"; do
			pid=$(_pt_pid "$status" "$name")

			if ! _pt_running "$summary" "$name" || [ -z "$pid" ] || [ "$pid" = "${before[i]}" ]; then
				waiting+=("$name")
			fi

			i=$((i + 1))
		done

		[ "${#waiting[@]}" -gt 0 ] || break

		if [ "$SECONDS" -ge "$deadline" ]; then
			printf 'drift name=%s\n' "${waiting[@]}"
			return "$EXIT_RESTART"
		fi
	done

	printf 'ok name=%s\n' "$@"
}

# cleanup removes the task's temporary files beside each target it is
# given, and then the task's directory after valid_work accepts its s and
# b directories. Each target is checked with valid_pinned before anything
# is removed, and only regular files named for the task are removed, so a
# symlink is never followed. A task directory that is already gone is
# cleaned.
cleanup() {
	[ "$#" -ge 1 ] && [ $(($# % 2)) -eq 1 ] || return "$EXIT_USAGE"
	valid_task "$1" || return

	local task=$1 root dir file
	local -a dirs=()

	shift

	while [ "$#" -gt 0 ]; do
		valid_pinned "$2" "$1" || return
		dirs+=("${2%/*}")
		shift 2
	done

	root=$(_cp_root) || return "$EXIT_REFUSED"

	for dir in "${dirs[@]}"; do
		for file in "$root$dir/.certpatch.$task."*; do
			if [ -f "$file" ] && [ ! -L "$file" ]; then
				rm -f -- "$file" || return 1
			fi
		done

		_pt_sync "$root$dir" || return 1
	done

	dir=$root$CP_WORK_ROOT/$task

	if [ ! -e "$dir" ] && [ ! -L "$dir" ]; then
		printf 'ok path=%s\n' "$CP_WORK_ROOT/$task"
		return 0
	fi

	valid_work "$CP_WORK_ROOT/$task/s/0" "$task" || return
	valid_work "$CP_WORK_ROOT/$task/b/0" "$task" || return

	rm -rf -- "$dir" || return 1
	_pt_sync "${dir%/*}" || return 1

	printf 'ok path=%s\n' "$CP_WORK_ROOT/$task"
}

# _pt_main runs the operation that its first argument names.
_pt_main() {
	local operation=${1:-}

	[ "$#" -eq 0 ] || shift

	case $operation in
	prepare) prepare "$@" ;;
	stage) stage "$@" ;;
	check) check "$@" ;;
	stagecheck) stagecheck "$@" ;;
	backup) backup "$@" ;;
	replace) replace "$@" ;;
	restore) restore "$@" ;;
	restart) restart "$@" ;;
	cleanup) cleanup "$@" ;;
	*) return "$EXIT_USAGE" ;;
	esac
}

# This line must stay the last line of the script, with nothing after it,
# because stage reads what follows it on stdin. exit with no argument
# keeps the operation's status, which the EXIT trap does not change.
_pt_main "$@"; exit
