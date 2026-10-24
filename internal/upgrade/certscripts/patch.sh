# patch.sh changes the certificate and key files of one VM in place. Each
# operation takes the task ID first and checks it before anything else.
#
#   prepare <task>
#   stage <task> <staged>
#   check <task> <jobdir> <path> <old> <new> <backup> [<oldraw> <newraw>]
#   stagecheck <task> <staged> <new> [<newraw> <role>]
#   backup <task> <jobdir> <path> <old> <backup> [<oldraw>]
#   replace <task> <jobdir> <path> <staged> <old> <new> [<oldraw> <newraw> <role>]
#   restore <task> <jobdir> <path> <backup> <old> <new> [<oldraw> <newraw>]
#   restart <task> <process>...
#   cleanup <task> [<jobdir> <path>]...
#
# <path> is a pinned target under <jobdir>, which discovery recorded.
# <staged> is the task's staging path s/<n> and <backup> its backup path
# b/<n>. <old> and <new> are normalised hashes. Each operation prints
# lines that start with ok, drift, or unrecovered and name the file or
# process, and never prints file content.
#
# <oldraw> is the raw hash of the target as discovery read it, which is
# also the raw hash its backup must keep, and <newraw> the raw hash of
# the exact bytes the broker staged to replace it. When the broker sends
# them, every comparison of a target, a staged file, or a backup checks
# the raw hash as well as the normalised one, so a byte that changed
# after discovery is drift even when it is only whitespace. A journal
# written before raw hashes existed sends none, and the operations then
# compare normalised hashes as they always did. <role> is certificate or
# key, and names the openssl check a staged file and the file about to
# replace a target must pass.
#
# The broker runs check on every file first, then prepare, then stage,
# then the other operations, all on one connection to the VM. check only
# reads, and on a VM that no task has touched it runs before the task's
# directories exist, so it accepts a backup path whose directories
# prepare has not made yet through valid_work_unmade, and reports no
# backup. prepare makes the task's directories through make_work_dir,
# and stage and every later operation run valid_work on them, so nothing
# is written to or read from a directory that is not private to root.
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

# _pt_work_unmade <path> <task> <kind> is _pt_work for check, which also
# accepts a path whose directories prepare has not made yet.
_pt_work_unmade() {
	[[ $1 == "$CP_WORK_ROOT/$2/$3/"* ]] || return "$EXIT_REFUSED"
	valid_work_unmade "$1" "$2"
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

# _pt_raw <file> prints the file's raw hash, or nothing when it is
# missing or cannot be read, which every caller reads as drift.
_pt_raw() {
	raw_hash "$1" 2> /dev/null || true
}

# _pt_valid <role> <file> succeeds when openssl reads the file as a
# certificate, for the role certificate, or as a private key, for the
# role key. openssl's output goes nowhere, so nothing of a key is ever
# printed, and it never prompts, because stdin is empty and a pass
# phrase is given as empty. It returns 14 when openssl refuses the file
# and 1 when the stemcell's openssl is missing.
_pt_valid() {
	local role=$1 file=$2 root openssl
	local -a args

	case $role in
	certificate) args=(x509 -noout -in "$file") ;;
	key) args=(pkey -noout -passin pass: -in "$file") ;;
	*) return "$EXIT_USAGE" ;;
	esac

	root=$(_cp_root) || return "$EXIT_REFUSED"
	openssl=$root/usr/bin/openssl
	[ -f "$openssl" ] && [ -x "$openssl" ] || return 1

	timeout 30 "$openssl" "${args[@]}" < /dev/null > /dev/null 2>&1 || return "$EXIT_INVALID"
}

# _pt_role <role> accepts the two roles _pt_valid knows.
_pt_role() {
	case $1 in
	certificate | key) return 0 ;;
	*) return "$EXIT_USAGE" ;;
	esac
}

# _pt_swap <task> <source> <full> <want> [<wantraw> [<role>]] writes the
# content of source over the target full through a temporary file in
# the target's directory, named for the task. The new file takes the
# target's owner, group, and mode, and before the rename it must hash to
# want, have the raw hash wantraw when one is given, and pass _pt_valid
# for role when one is given. After the rename the target must have the
# raw hash wantraw. A temporary file that openssl refuses returns 14,
# and the EXIT trap removes it, so the target keeps its content.
_pt_swap() {
	local task=$1 source=$2 full=$3 want=$4 wantraw=${5:-} role=${6:-} dir info owner mode status

	dir=${full%/*}
	info=$(stat -c '%u:%g %a' -- "$full") || return 1
	owner=${info% *}
	mode=${info#* }

	_PT_TMP=$(mktemp -- "$dir/.certpatch.$task.XXXXXXXXXX") || return 1
	cat -- "$source" > "$_PT_TMP" || return 1
	chown -- "$owner" "$_PT_TMP" || return 1
	chmod -- "$mode" "$_PT_TMP" || return 1
	[ "$(_pt_hash "$_PT_TMP")" = "$want" ] || return 1

	if [ -n "$wantraw" ]; then
		[ "$(_pt_raw "$_PT_TMP")" = "$wantraw" ] || return 1
	fi

	if [ -n "$role" ]; then
		_pt_valid "$role" "$_PT_TMP"
		status=$?
		[ "$status" -eq 0 ] || return "$status"
	fi

	_pt_sync "$_PT_TMP" || return 1
	mv -f -T -- "$_PT_TMP" "$full" || return 1
	_PT_TMP=
	_pt_sync "$dir" || return 1

	if [ -n "$wantraw" ]; then
		[ "$(_pt_raw "$full")" = "$wantraw" ] || return 1
	fi
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

# _pt_both <file> prints the file's normalised hash and its raw hash as
# <norm>:<raw>. Matching both means the file holds exactly the bytes the
# raw hash pins, and that those bytes hold the PEM the normalised hash
# names, even when discovery read the file while it changed.
# _pt_pick calls it by name, which shellcheck cannot see.
# shellcheck disable=SC2329
_pt_both() {
	printf '%s:%s\n' "$(_pt_hash "$1")" "$(_pt_raw "$1")"
}

# _pt_pick <raw> sets hashof to the hash function an operation compares
# with: _pt_both when the broker sent raw hashes, and _pt_hash otherwise.
# The caller declares hashof local, and compares with <norm>:<raw> pairs
# when the broker sent raw hashes.
_pt_pick() {
	if [ "$1" = yes ]; then
		hashof=_pt_both
	else
		hashof=_pt_hash
	fi
}

# check reports whether the target holds the old or the new content and
# whether its backup holds the old content, and changes nothing. The
# broker sends it before prepare, so on a VM that no task has touched the
# backup's directories do not exist yet, which means no backup does
# either, and check reports none.
check() {
	local raw=no

	case $# in
	6) ;;
	8) raw=yes ;;
	*) return "$EXIT_USAGE" ;;
	esac

	valid_task "$1" || return

	local task=$1 jobdir=$2 path=$3 old=$4 new=$5 backup=$6 root hash state saved=none hashof

	_pt_hashes "$old" "$new" || return

	if [ "$raw" = yes ]; then
		_pt_hashes "$7" "$8" || return
		old=$old:$7
		new=$new:$8
	fi

	_pt_pick "$raw"
	valid_pinned "$path" "$jobdir" || return
	_pt_work_unmade "$backup" "$task" b || return
	root=$(_cp_root) || return "$EXIT_REFUSED"

	hash=$("$hashof" "$root$path")

	case $hash in
	"$old") state=old ;;
	"$new") state=new ;;
	*)
		printf 'drift path=%s\n' "$path"
		return "$EXIT_DRIFT"
		;;
	esac

	if [ -e "$root$backup" ]; then
		if [ "$("$hashof" "$root$backup")" != "$old" ]; then
			printf 'drift path=%s\n' "$backup"
			return "$EXIT_BACKUP"
		fi

		saved=old
	fi

	printf 'ok path=%s state=%s backup=%s\n' "$path" "$state" "$saved"
}

# stagecheck checks that the staged file holds the new content. With raw
# hashes it must hold exactly the bytes the broker staged, and openssl
# must read it in its role, so a file that would not load stops the
# patch before anything on the VM changed.
stagecheck() {
	local raw=no

	case $# in
	3) ;;
	5) raw=yes ;;
	*) return "$EXIT_USAGE" ;;
	esac

	valid_task "$1" || return

	local task=$1 staged=$2 new=$3 root status

	valid_hash "$new" || return

	if [ "$raw" = yes ]; then
		valid_hash "$4" || return
		_pt_role "$5" || return
	fi

	_pt_work "$staged" "$task" s || return
	root=$(_cp_root) || return "$EXIT_REFUSED"

	if [ "$(_pt_hash "$root$staged")" != "$new" ] || { [ "$raw" = yes ] && [ "$(_pt_raw "$root$staged")" != "$4" ]; }; then
		printf 'drift path=%s\n' "$staged"
		return "$EXIT_DRIFT"
	fi

	if [ "$raw" = yes ]; then
		_pt_valid "$5" "$root$staged"
		status=$?

		if [ "$status" -ne 0 ]; then
			printf 'drift path=%s\n' "$staged"
			return "$status"
		fi
	fi

	printf 'ok path=%s\n' "$staged"
}

# backup copies the target to a temporary file in the task's b directory
# and links it to the backup path. ln refuses a name that exists, so a
# backup is written once and never changed. With a raw hash, the target,
# the copy, and a backup already made must all have it.
backup() {
	local raw=no

	case $# in
	5) ;;
	6) raw=yes ;;
	*) return "$EXIT_USAGE" ;;
	esac

	valid_task "$1" || return

	local task=$1 jobdir=$2 path=$3 old=$4 backup=$5 root full saved hashof

	valid_hash "$old" || return

	if [ "$raw" = yes ]; then
		valid_hash "$6" || return
		old=$old:$6
	fi

	_pt_pick "$raw"
	valid_pinned "$path" "$jobdir" || return
	_pt_work "$backup" "$task" b || return
	root=$(_cp_root) || return "$EXIT_REFUSED"

	full=$root$path
	saved=$root$backup

	if [ -e "$saved" ]; then
		if [ "$("$hashof" "$saved")" != "$old" ]; then
			printf 'drift path=%s\n' "$backup"
			return "$EXIT_BACKUP"
		fi

		printf 'ok path=%s backup=%s action=exists\n' "$path" "$backup"
		return 0
	fi

	if [ "$("$hashof" "$full")" != "$old" ]; then
		printf 'drift path=%s\n' "$path"
		return "$EXIT_DRIFT"
	fi

	_PT_TMP=$(mktemp -- "${saved%/*}/.tmp.XXXXXXXXXX") || return 1
	cat -- "$full" > "$_PT_TMP" || return 1

	# The job can write the target while it is copied, so the copy is
	# checked, not the target.
	if [ "$("$hashof" "$_PT_TMP")" != "$old" ]; then
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

# replace swaps the staged file in over a target that holds the old
# content. With raw hashes, the target must still hold exactly the bytes
# discovery read, the staged file exactly the bytes the broker staged,
# and the file about to be renamed must pass openssl in its role.
replace() {
	local raw=no

	case $# in
	6) ;;
	9) raw=yes ;;
	*) return "$EXIT_USAGE" ;;
	esac

	valid_task "$1" || return

	local task=$1 jobdir=$2 path=$3 staged=$4 old=$5 new=$6 root full hash hashof status
	local oldraw='' newraw='' role=''

	_pt_hashes "$old" "$new" || return

	if [ "$raw" = yes ]; then
		_pt_hashes "$7" "$8" || return
		_pt_role "$9" || return
		oldraw=$7
		newraw=$8
		role=$9
	fi

	_pt_pick "$raw"
	valid_pinned "$path" "$jobdir" || return
	_pt_work "$staged" "$task" s || return
	root=$(_cp_root) || return "$EXIT_REFUSED"

	full=$root$path
	hash=$("$hashof" "$full")

	if [ "$hash" = "$new${newraw:+:$newraw}" ]; then
		printf 'ok path=%s state=new action=unchanged\n' "$path"
		return 0
	fi

	if [ "$hash" != "$old${oldraw:+:$oldraw}" ]; then
		printf 'drift path=%s\n' "$path"
		return "$EXIT_DRIFT"
	fi

	if [ "$(_pt_hash "$root$staged")" != "$new" ] || { [ "$raw" = yes ] && [ "$(_pt_raw "$root$staged")" != "$newraw" ]; }; then
		printf 'drift path=%s\n' "$staged"
		return "$EXIT_DRIFT"
	fi

	_pt_swap "$task" "$root$staged" "$full" "$new" "$newraw" "$role"
	status=$?

	case $status in
	0) ;;
	"$EXIT_INVALID")
		printf 'drift path=%s\n' "$staged"
		return "$status"
		;;
	*) return 1 ;;
	esac

	printf 'ok path=%s state=new action=replaced\n' "$path"
}

# restore puts the backup's content back over a target that holds the
# new content, and keeps the backup. With raw hashes, the target must
# hold exactly the bytes the replacement wrote and the backup exactly
# the bytes discovery read, so a backup changed by even one byte of
# whitespace is refused.
restore() {
	local raw=no

	case $# in
	6) ;;
	8) raw=yes ;;
	*) return "$EXIT_USAGE" ;;
	esac

	valid_task "$1" || return

	local task=$1 jobdir=$2 path=$3 backup=$4 old=$5 new=$6 root full hash hashof oldraw=''

	_pt_hashes "$old" "$new" || return

	if [ "$raw" = yes ]; then
		_pt_hashes "$7" "$8" || return
		oldraw=$7
		old=$old:$7
		new=$new:$8
	fi

	_pt_pick "$raw"
	valid_pinned "$path" "$jobdir" || return
	_pt_work "$backup" "$task" b || return
	root=$(_cp_root) || return "$EXIT_REFUSED"

	full=$root$path
	hash=$("$hashof" "$full")

	if [ "$hash" = "$old" ]; then
		printf 'ok path=%s state=old action=unchanged\n' "$path"
		return 0
	fi

	if [ "$hash" != "$new" ]; then
		printf 'unrecovered path=%s\n' "$path"
		return "$EXIT_DRIFT"
	fi

	if [ "$("$hashof" "$root$backup")" != "$old" ]; then
		printf 'unrecovered path=%s\n' "$path"
		return "$EXIT_BACKUP"
	fi

	_pt_swap "$task" "$root$backup" "$full" "$5" "$oldraw" || return 1

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
