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
