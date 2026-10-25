# certlib.sh holds the functions that only the certificate patch helpers
# use. The broker sends it after the base library, ops/scripts/lib.sh,
# and before one script, so it relies on the base library's prelude, exit
# constants, and _cp_* helpers, and nothing here runs on its own.

# The certificate path patterns, which are the ones in paths.go. The
# checks of the VM's files then add what only the VM can see.
readonly CP_LOGICAL_RE='^/var/vcap/jobs/[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+$'
readonly CP_PINNED_RE='^/var/vcap/data/jobs/[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+$'
readonly CP_JOBDIR_RE='^/var/vcap/data/jobs/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'
readonly CP_WORK_TAIL_RE='^[sb]/(0|[1-9][0-9]{0,8})$'
readonly CP_WORK_ROOT=/var/vcap/data/certpatch

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
