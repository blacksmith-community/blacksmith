package certscripts

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"blacksmith/internal/ops/scripts"
)

// ErrNoPasswordlessSudo is the error a driver reports, before anything is
// regenerated, when SudoCheckCommand fails.
var ErrNoPasswordlessSudo = errors.New("passwordless sudo is not available for BOSH SSH users")

// ErrPatchUnsafe is the error ParseDiscovery, BuildConsumers, and
// RequireConsumers wrap when discovery shows that patching in place is
// not safe, so the operator should redeploy instead.
var ErrPatchUnsafe = errors.New("certscripts: patch in place is not safe on this VM")

// Match is one file that discovery found. For a whole-file match, Path is
// a pinned target, Hash is the normalised hash of the whole file, and the
// other fields describe it. For an embedded match, Path is the file that
// holds the block and Hash is the block's hash, and the other fields are
// empty.
type Match struct {
	Job  string
	Path string
	Hash string
	// RawHash is the SHA-256 of the file's bytes as discovery read them.
	RawHash string
	// Lead is the blank lines before the PEM, as LF or CRLF bytes, and
	// Tail the spaces, tabs, carriage returns, and newlines after it. A
	// replacement keeps both, as BOSH would render them.
	Lead  string
	Tail  string
	Owner string
	Mode  string
	// RootDirs says that every directory from / down to the file is
	// root's and not writable by group or other, and that the file has
	// one link, so the checks a write makes would pass.
	RootDirs bool
	// Why says which check refused the file when RootDirs is false. It
	// is nil when RootDirs is true.
	Why *Refusal
}

// Refusal is the first check that stopped a patch in place on one file.
type Refusal struct {
	// Token is one of the closed set discover.sh prints.
	Token string
	// At is the path the check failed on, empty for a token with none.
	At string
	// Owner and Mode are the failing directory's owner:group and octal
	// mode, set for the dir-owner and dir-writable tokens only.
	Owner string
	Mode  string
}

// Discovery is what discover.sh found on one VM.
type Discovery struct {
	// VM names the VM the output came from. ParseDiscovery leaves it
	// empty, and the caller sets it so that BuildConsumers can name it.
	VM string
	// JobDirs maps each job to its resolved directory under
	// /var/vcap/data/jobs.
	JobDirs map[string]string
	// Matches are the regular files whose whole content has an old hash.
	Matches []Match
	// Embedded are the files that hold an old hash inside larger content.
	Embedded []Match
	// Processes maps each job to the monit processes its monit file names.
	Processes map[string][]string
}

// Consumer is one job on one VM that holds the certificate file and the
// key file of one variable, and the monit processes that read them.
type Consumer struct {
	Job       string
	Variable  string
	CertPath  string
	KeyPath   string
	Processes []string
}

// The keys of discovery output.
const (
	keyJob  = "job"
	keyPath = "path"
	keyHash = "hash"
)

// discoveryKinds returns the keys each kind of discovery line holds.
func discoveryKinds() map[string]map[string]bool {
	return map[string]map[string]bool{
		"jobdir": {keyJob: true, keyPath: true},
		"match": {
			keyJob: true, keyPath: true, keyHash: true, "raw": true, "lead": true, "tail": true,
			"owner": true, "mode": true, "rootdirs": true,
			"why": true, "at": true, "by": true, "bits": true,
		},
		"embedded": {keyJob: true, keyPath: true, keyHash: true},
		"proc":     {keyJob: true, "name": true},
	}
}

// The forms of the values in discovery output.
var (
	jobName     = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	processName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@+-]*$`)
	ownerValue  = regexp.MustCompile(`^[A-Za-z0-9_.-]+:[A-Za-z0-9_.-]+$`)
	modeValue   = regexp.MustCompile(`^[0-7]{4}$`)
	// leadValue and tailValue are the forms discover.sh prints, - for
	// none or one letter a byte, and discover.sh checks the same forms.
	leadValue = regexp.MustCompile(`^(-|(rn|n){1,32})$`)
	tailValue = regexp.MustCompile(`^(-|[nrst]{1,64})$`)
)

// edgeBytes turns a lead or a tail from its letters into its bytes.
func edgeBytes(letters string) string {
	return strings.NewReplacer("-", "", "n", "\n", "r", "\r", "s", " ", "t", "\t").Replace(letters)
}

// The ways discovery output can fail its checks. None repeats the output.
var (
	errMissingField = errors.New("a field is missing")
	errJobName      = errors.New("the job name is malformed")
	errJobDir       = errors.New("the job directory is not the job's resolved directory")
	errJobRepeated  = errors.New("the job directory is reported twice")
	errNoJobDir     = errors.New("the file's job has no job directory")
	errHash         = errors.New("the hash is malformed")
	errOwner        = errors.New("the owner is malformed")
	errMode         = errors.New("the mode is malformed")
	errRootDirs     = errors.New("rootdirs is neither yes nor no")
	errWhy          = errors.New("the reason for rootdirs=no is missing, unknown, or malformed")
	errWhyOnYes     = errors.New("a reason is given for rootdirs=yes")
	errEdges        = errors.New("the whitespace around the file's PEM is malformed")
	errProcess      = errors.New("the process name is malformed")
)

// layoutError is a job laid out on the VM in a way that patching cannot
// follow, as opposed to output that breaks the contract.
type layoutError struct {
	job string
	err error
}

func (e *layoutError) Error() string { return e.err.Error() }

func (e *layoutError) Unwrap() error { return e.err }

// lineError wraps the fault in a line of discovery output. A job's layout
// wraps ErrPatchUnsafe and names the job, so the operator is told to
// redeploy, and every other fault wraps ErrMalformedOutput.
func lineError(line int, err error) error {
	var layout *layoutError
	if errors.As(err, &layout) {
		return fmt.Errorf("%w: the job %s is laid out in a way patching cannot follow (line %d): %w",
			ErrPatchUnsafe, layout.job, line, layout.err)
	}

	return fmt.Errorf("%w: line %d: %w", scripts.ErrMalformedOutput, line, err)
}

// ParseDiscovery reads discover.sh output. It checks every line, and it
// runs ValidPinnedTarget on every match and every embedded file against
// its job's jobdir line. A file outside its job's resolved directory, a
// job directory that is not /var/vcap/data/jobs/<job>/<version>, and a
// path the script printed as ! reject the whole output with an error that
// wraps ErrPatchUnsafe and names the job. Any other fault rejects it with
// ErrMalformedOutput. Matches and embedded files come back sorted by job
// and path.
func ParseDiscovery(out []byte) (*Discovery, error) {
	records, err := scripts.ParseRecords(out, discoveryKinds())
	if err != nil {
		return nil, err
	}

	found := &Discovery{JobDirs: make(map[string]string), Processes: make(map[string][]string)}

	// Job directories come first, so a file's check never depends on the
	// order the script printed its lines in.
	for _, record := range records {
		if record.Kind != "jobdir" {
			continue
		}

		err := found.addJobDir(record.Fields)
		if err != nil {
			return nil, lineError(record.Line, err)
		}
	}

	for _, record := range records {
		err := found.add(record)
		if err != nil {
			return nil, lineError(record.Line, err)
		}
	}

	sortMatches(found.Matches)
	sortMatches(found.Embedded)

	return found, nil
}

// fields returns the values of keys, or an error when any is missing.
func fields(pairs map[string]string, keys ...string) ([]string, error) {
	values := make([]string, 0, len(keys))

	for _, key := range keys {
		value, ok := pairs[key]
		if !ok {
			return nil, errMissingField
		}

		values = append(values, value)
	}

	return values, nil
}

func (d *Discovery) addJobDir(pairs map[string]string) error {
	values, err := fields(pairs, keyJob, keyPath)
	if err != nil {
		return err
	}

	job, path := values[0], values[1]

	if !jobName.MatchString(job) || !cleanSegments("/"+job) {
		return errJobName
	}

	if !shaped(path, jobDirPath) || !strings.HasPrefix(path, "/var/vcap/data/jobs/"+job+"/") {
		return &layoutError{job: job, err: errJobDir}
	}

	if _, seen := d.JobDirs[job]; seen {
		return errJobRepeated
	}

	d.JobDirs[job] = path

	return nil
}

func (d *Discovery) add(record scripts.Record) error {
	switch record.Kind {
	case "match":
		match, err := d.file(record.Fields, true)
		if err != nil {
			return err
		}

		d.Matches = append(d.Matches, match)
	case "embedded":
		match, err := d.file(record.Fields, false)
		if err != nil {
			return err
		}

		d.Embedded = append(d.Embedded, match)
	case "proc":
		values, err := fields(record.Fields, keyJob, "name")
		if err != nil {
			return err
		}

		job, name := values[0], values[1]

		if !jobName.MatchString(job) || !cleanSegments("/"+job) {
			return errJobName
		}

		if !processName.MatchString(name) {
			return errProcess
		}

		d.Processes[job] = append(d.Processes[job], name)
	}

	return nil
}

// file reads a match line, with its raw hash, lead, tail, owner, mode,
// and rootdirs when whole is set, or an embedded line.
func (d *Discovery) file(pairs map[string]string, whole bool) (Match, error) {
	values, err := fields(pairs, keyJob, keyPath, keyHash)
	if err != nil {
		return Match{}, err
	}

	match := Match{Job: values[0], Path: values[1], Hash: values[2]}

	jobDir, ok := d.JobDirs[match.Job]
	if !ok {
		return Match{}, errNoJobDir
	}

	err = ValidPinnedTarget(match.Path, jobDir)
	if err != nil {
		return Match{}, &layoutError{job: match.Job, err: err}
	}

	if scripts.ValidHash(match.Hash) != nil {
		return Match{}, errHash
	}

	if !whole {
		return match, nil
	}

	values, err = fields(pairs, "raw", "lead", "tail", "owner", "mode", "rootdirs")
	if err != nil {
		return Match{}, err
	}

	switch {
	case scripts.ValidHash(values[0]) != nil:
		return Match{}, errHash
	case !leadValue.MatchString(values[1]), !tailValue.MatchString(values[2]):
		return Match{}, errEdges
	}

	match.RawHash = values[0]
	match.Lead, match.Tail = edgeBytes(values[1]), edgeBytes(values[2])
	values = values[3:]
	match.Owner, match.Mode = values[0], values[1]

	switch {
	case !ownerValue.MatchString(match.Owner):
		return Match{}, errOwner
	case !modeValue.MatchString(match.Mode):
		return Match{}, errMode
	}

	switch values[2] {
	case "yes":
		match.RootDirs = true

		for _, key := range []string{keyWhy, keyAt, keyBy, keyBits} {
			if _, ok := pairs[key]; ok {
				return Match{}, errWhyOnYes
			}
		}
	case "no":
		why, err := refusal(pairs)
		if err != nil {
			return Match{}, err
		}

		match.Why = why
	default:
		return Match{}, errRootDirs
	}

	return match, nil
}

// The keys of a reason, and the tokens discover.sh prints for one.
const (
	keyWhy  = "why"
	keyAt   = "at"
	keyBy   = "by"
	keyBits = "bits"
)

// Reason tokens. needsAt says whether a token names a path, and needsMode
// whether it also names the directory's owner and mode.
var reasonTokens = map[string]struct{ needsAt, needsMode bool }{
	"shape":        {needsAt: false},
	"job-link":     {needsAt: true},
	"dir-symlink":  {needsAt: true},
	"dir-missing":  {needsAt: true},
	"dir-owner":    {needsAt: true, needsMode: true},
	"dir-writable": {needsAt: true, needsMode: true},
	"not-regular":  {needsAt: true},
	"many-links":   {needsAt: true},
	"realpath":     {needsAt: true},
	"other":        {needsAt: false},
}

var reasonPath = regexp.MustCompile(`^/[A-Za-z0-9_./:-]*$`)

// refusal reads the reason on a rootdirs=no line. The token must be a
// known one, and each token takes exactly the extra fields it names.
func refusal(pairs map[string]string) (*Refusal, error) {
	token, ok := pairs[keyWhy]
	if !ok {
		return nil, errWhy
	}

	spec, known := reasonTokens[token]
	if !known {
		return nil, errWhy
	}

	why := &Refusal{Token: token, At: pairs[keyAt], Owner: pairs[keyBy], Mode: pairs[keyBits]}

	_, hasAt := pairs[keyAt]
	_, hasBy := pairs[keyBy]
	_, hasBits := pairs[keyBits]

	if hasAt != spec.needsAt || hasBy != spec.needsMode || hasBits != spec.needsMode {
		return nil, errWhy
	}

	if hasAt && !reasonPath.MatchString(why.At) {
		return nil, errWhy
	}

	if spec.needsMode && (!ownerValue.MatchString(why.Owner) || !modeValue.MatchString(why.Mode)) {
		return nil, errWhy
	}

	return why, nil
}

// describe says in a sentence why the file could not be patched in place.
func (r *Refusal) describe() string {
	if r == nil {
		return "a safety check failed that the scripts could not name, so patching in place isn't safe. Redeploy it instead"
	}

	switch r.Token {
	case "job-link":
		return fmt.Sprintf("the link %s is missing or does not point at the job's directory, so patching in place isn't safe. Redeploy it instead", r.At)
	case "dir-symlink":
		return fmt.Sprintf("the directory %s is a symbolic link, so patching in place isn't safe. Redeploy it instead", r.At)
	case "dir-missing":
		return fmt.Sprintf("the directory %s is missing or is not a directory, so patching in place isn't safe. Redeploy it instead", r.At)
	case "dir-owner":
		return fmt.Sprintf("the directory %s is owned by %s rather than root (mode %s), so patching in place isn't safe. Redeploy it instead",
			r.At, r.Owner, r.Mode)
	case "dir-writable":
		return fmt.Sprintf("the directory %s can be changed by %s (owner %s, mode %s), so patching in place isn't safe. Redeploy it instead",
			r.At, r.writers(), r.Owner, r.Mode)
	case "not-regular":
		return fmt.Sprint("that file is not a plain file, or is a symbolic link, so patching in place isn't safe. Redeploy it instead")
	case "many-links":
		return fmt.Sprint("that file has more than one link, so patching in place isn't safe. Redeploy it instead")
	case "realpath":
		return fmt.Sprint("that file resolves to a different real path, so patching in place isn't safe. Redeploy it instead")
	case "shape":
		return "the file or its job directory is not where a job's files belong, so patching in place isn't safe. Redeploy it instead"
	default:
		return "a safety check failed that the scripts could not name, so patching in place isn't safe. Redeploy it instead"
	}
}

// writers names who the mode lets change the directory, from the group
// in owner and the group and other write bits in mode.
func (r *Refusal) writers() string {
	bits, err := strconv.ParseUint(r.Mode, 8, 32)
	if err != nil {
		return "someone other than root"
	}

	_, group, _ := strings.Cut(r.Owner, ":")

	var who []string
	if bits&0o020 != 0 {
		who = append(who, "group "+group)
	}

	if bits&0o002 != 0 {
		who = append(who, "any user")
	}

	return strings.Join(who, " and ")
}

func sortMatches(matches []Match) {
	slices.SortFunc(matches, func(left, right Match) int {
		return cmp.Or(
			strings.Compare(left.Job, right.Job),
			strings.Compare(left.Path, right.Path),
			strings.Compare(left.Hash, right.Hash),
		)
	})
}

// unsafe wraps ErrPatchUnsafe with a sentence about the VM.
func (d *Discovery) unsafe(format string, args ...any) error {
	vm := d.VM
	if vm == "" {
		vm = "(unnamed)"
	}

	return fmt.Errorf("%w: on VM %s, %s", ErrPatchUnsafe, vm, fmt.Sprintf(format, args...))
}

// BuildConsumers turns one VM's discovery into its consumers. The maps go
// from each selected variable's name to the normalised hash of its old
// certificate and of its old key, and both must name the same variables.
// Each job that holds a variable's certificate file and key file is one
// consumer per pair of files. Every copy of a variable's certificate
// holds the same certificate, and every copy of its key the same key, so
// the files pair in path order. It fails, with an error that wraps
// ErrPatchUnsafe and names the VM and the variable, when a job holds a
// variable's certificate file and no key file or the reverse, or a
// different number of each, when a file holds an old certificate or key
// inside larger content, when a matching file fails the checks a write
// would make (rootdirs=no), and when the job of a consumer has no monit
// process to restart. A VM that holds none of a variable's files
// has no consumer of it and nothing to patch for it, because the files
// can live in another instance group, so RequireConsumers checks across
// all the VMs that every variable has a consumer somewhere. Consumers come
// back sorted by job, variable, and paths.
func BuildConsumers(disc *Discovery, oldCertHash, oldKeyHash map[string]string) ([]Consumer, error) {
	variables := make([]string, 0, len(oldCertHash))
	for variable := range oldCertHash {
		variables = append(variables, variable)
	}

	sort.Strings(variables)

	for variable := range oldKeyHash {
		if _, ok := oldCertHash[variable]; !ok {
			return nil, disc.unsafe("the variable %s has an old key hash and no old certificate hash", variable)
		}
	}

	for _, variable := range variables {
		if _, ok := oldKeyHash[variable]; !ok {
			return nil, disc.unsafe("the variable %s has an old certificate hash and no old key hash", variable)
		}
	}

	certOf := invert(oldCertHash)
	keyOf := invert(oldKeyHash)

	// The embedded files are sorted, so the first one names the error.
	if len(disc.Embedded) > 0 {
		first := disc.Embedded[0]

		names := slices.Concat(certOf[first.Hash], keyOf[first.Hash])
		if len(names) == 0 {
			return nil, disc.unsafe("the embedded file %s has a hash that was not asked for", first.Path)
		}

		return nil, disc.unsafe("the variable %s is embedded in larger content in %s, which patching cannot replace",
			strings.Join(names, ", "), first.Path)
	}

	// certs and keys map job, then variable, to the matching paths.
	certs := make(map[string]map[string][]string)
	keys := make(map[string]map[string][]string)

	for _, match := range disc.Matches {
		names := slices.Concat(certOf[match.Hash], keyOf[match.Hash])
		if len(names) == 0 {
			return nil, disc.unsafe("the file %s has a hash that was not asked for", match.Path)
		}

		if !match.RootDirs {
			return nil, disc.unsafe("for the file %s of the variable %s, %s",
				match.Path, strings.Join(names, ", "), match.Why.describe())
		}

		for _, variable := range certOf[match.Hash] {
			addPath(certs, match.Job, variable, match.Path)
		}

		for _, variable := range keyOf[match.Hash] {
			addPath(keys, match.Job, variable, match.Path)
		}
	}

	return disc.pair(variables, certs, keys)
}

// pair matches each job's certificate files to its key files for every
// variable.
func (d *Discovery) pair(variables []string, certs, keys map[string]map[string][]string) ([]Consumer, error) {
	jobs := make([]string, 0, len(certs)+len(keys))
	for job := range certs {
		jobs = append(jobs, job)
	}

	for job := range keys {
		if _, ok := certs[job]; !ok {
			jobs = append(jobs, job)
		}
	}

	sort.Strings(jobs)

	var consumers []Consumer

	for _, job := range jobs {
		for _, variable := range variables {
			certPaths, keyPaths := certs[job][variable], keys[job][variable]

			switch {
			case len(certPaths) == 0 && len(keyPaths) == 0:
				continue
			case len(keyPaths) == 0:
				return nil, d.unsafe("the variable %s has a certificate file and no key file in the job %s", variable, job)
			case len(certPaths) == 0:
				return nil, d.unsafe("the variable %s has a key file and no certificate file in the job %s", variable, job)
			case len(certPaths) != len(keyPaths):
				return nil, d.unsafe("the variable %s has %d certificate files and %d key files in the job %s",
					variable, len(certPaths), len(keyPaths), job)
			}

			// A consumer whose job names no process would have its files
			// replaced and nothing restarted, so the old certificate would
			// keep serving.
			if len(d.Processes[job]) == 0 {
				return nil, d.unsafe("the job %s holds the variable %s and has no monit process to restart", job, variable)
			}

			sort.Strings(certPaths)
			sort.Strings(keyPaths)

			for i := range certPaths {
				consumers = append(consumers, Consumer{
					Job: job, Variable: variable, CertPath: certPaths[i], KeyPath: keyPaths[i],
					Processes: slices.Clone(d.Processes[job]),
				})
			}
		}
	}

	return consumers, nil
}

// RequireConsumers checks, across every VM of an instance, that each
// selected variable has at least one consumer. perVM maps each VM to the
// consumers BuildConsumers returned for it. A variable that no VM holds
// gives patch mode nothing to apply, so RequireConsumers then fails with
// an error that wraps ErrPatchUnsafe and names every such variable, and
// the operator should redeploy instead.
func RequireConsumers(variables []string, perVM map[string][]Consumer) error {
	held := make(map[string]bool)

	for _, consumers := range perVM {
		for _, consumer := range consumers {
			held[consumer.Variable] = true
		}
	}

	var missing []string

	for _, variable := range variables {
		if !held[variable] {
			missing = append(missing, variable)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	sort.Strings(missing)

	return fmt.Errorf("%w: the selected variables %s have no consumer on any VM", ErrPatchUnsafe, strings.Join(missing, ", "))
}

// invert maps each hash to the variables that have it, in name order.
func invert(byVariable map[string]string) map[string][]string {
	out := make(map[string][]string, len(byVariable))

	for variable, hash := range byVariable {
		out[hash] = append(out[hash], variable)
	}

	for _, names := range out {
		sort.Strings(names)
	}

	return out
}

func addPath(into map[string]map[string][]string, job, variable, path string) {
	if into[job] == nil {
		into[job] = make(map[string][]string)
	}

	into[job][variable] = append(into[job][variable], path)
}
