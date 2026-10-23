# discover.sh finds the files on this VM that hold an old certificate or
# an old key, and the monit processes of every job. It only reads.
#
#   discover <old-hash>...
#
# Each hash is the normalised hash of an old certificate or an old key.
# The script prints these lines, which ParseDiscovery reads.
#
#   jobdir job=<job> path=<resolved job directory>
#   match job=<job> path=<pinned target> hash=<h> owner=<user:group> mode=<octal> rootdirs=<yes|no>
#   embedded job=<job> path=<file> hash=<h>
#   proc job=<job> name=<process>
#
# A match is a regular file, not a symlink, under the job's resolved
# directory whose whole normalised content has one of the hashes.
# rootdirs=yes says that valid_pinned accepts the file, so every
# directory from / down to it is root's and not writable by group or
# other, and the file has a single link. An embedded line is a file that
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
	local root=$1 job=$2 jobdir=$3 file=$4 path size hash info owner mode rootdirs=no

	path=${file#"$root"}
	_dc_regular "$file" || return 1
	size=$(stat -c %s -- "$file") || return 1

	if [ "$size" -le "$DC_MAX_SIZE" ] && _dc_regular "$file" && hash=$(norm_hash "$file") && [ -n "${wanted[$hash]:-}" ]; then
		info=$(stat -c '%U:%G %a' -- "$file") || return 1
		owner=$(_dc_word "${info% *}" "$DC_OWNER_RE")
		mode=$(printf '%04o' "$((8#${info#* }))")

		if valid_pinned "$path" "$jobdir"; then
			rootdirs=yes
		fi

		printf 'match job=%s path=%s hash=%s owner=%s mode=%s rootdirs=%s\n' \
			"$job" "$(_dc_path "$path")" "$hash" "$owner" "$mode" "$rootdirs"

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
