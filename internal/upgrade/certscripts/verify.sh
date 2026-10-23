# verify.sh reads the leaf certificate that a listener on this VM serves,
# for when the broker cannot reach the listener itself. It only reads.
#
#   verify <port> <server name>
#
# It runs openssl s_client against 127.0.0.1:<port>, sending the server
# name for SNI unless it is empty, and prints one line
#
#   fingerprint=<the lowercase hex SHA-256 of the leaf's DER>
#
# and nothing else from openssl. A listener that serves no certificate
# exits 1, and a port or name of the wrong shape exits 2. A name starts
# with a letter or digit, so openssl never reads it as an option.

readonly VF_PORT_RE='^[1-9][0-9]{0,4}$'
readonly VF_NAME_RE='^[A-Za-z0-9][A-Za-z0-9_.-]{0,252}$'
readonly VF_BEGIN='-----BEGIN CERTIFICATE-----'
readonly VF_END='-----END CERTIFICATE-----'

# _vf_leaf prints the first certificate block in the text on stdin, with
# carriage returns removed, or nothing when no block is complete.
_vf_leaf() {
	local line inside=0 block=''

	while IFS= read -r line || [ -n "$line" ]; do
		line=${line%$'\r'}

		if [ "$inside" -eq 0 ]; then
			[ "$line" = "$VF_BEGIN" ] && inside=1
			continue
		fi

		if [ "$line" = "$VF_END" ]; then
			[ -n "$block" ] || return 1
			printf '%s' "$block"
			return 0
		fi

		block+=$line$'\n'
	done

	return 1
}

verify() {
	[ "$#" -eq 2 ] || return "$EXIT_USAGE"

	local port=$1 name=$2 root openssl out body fingerprint
	local -a args

	[[ $port =~ $VF_PORT_RE ]] && [ "$port" -le 65535 ] || return "$EXIT_USAGE"

	if [ -n "$name" ]; then
		[[ $name =~ $VF_NAME_RE ]] || return "$EXIT_USAGE"
	fi

	root=$(_cp_root) || return "$EXIT_REFUSED"
	openssl=$root/usr/bin/openssl
	[ -f "$openssl" ] && [ -x "$openssl" ] || return 1

	args=(s_client -connect "127.0.0.1:$port")
	[ -z "$name" ] || args+=(-servername "$name")

	# s_client prints the leaf it was handed even when the handshake then
	# fails, so its exit status says nothing about the certificate.
	out=$(timeout 10 "$openssl" "${args[@]}" < /dev/null 2> /dev/null) || true

	body=$(_vf_leaf <<< "$out") || return 1
	fingerprint=$(printf '%s' "$body" | base64 -d 2> /dev/null | sha256sum | cut -d ' ' -f 1) || return 1
	[[ $fingerprint =~ $CP_HASH_RE ]] || return 1

	# An empty body decodes to nothing, and the hash of nothing is no
	# certificate's fingerprint.
	[ "$fingerprint" != e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 ] || return 1

	printf 'fingerprint=%s\n' "$fingerprint"
}

case ${1:-} in
verify)
	shift
	verify "$@"
	;;
*)
	exit "$EXIT_USAGE"
	;;
esac
