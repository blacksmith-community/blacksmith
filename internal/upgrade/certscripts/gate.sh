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
