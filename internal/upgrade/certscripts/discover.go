package certscripts

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// SudoCheckCommand is the command a driver runs on a VM before discovery.
// Every privileged step runs with sudo -n, so a VM whose BOSH SSH user
// needs a password fails this at once instead of hanging a later step.
const SudoCheckCommand = "sudo -n true"

// ErrNoPasswordlessSudo is the error a driver reports, before anything is
// regenerated, when SudoCheckCommand fails.
var ErrNoPasswordlessSudo = errors.New("passwordless sudo is not available for BOSH SSH users")

// ErrPatchUnsafe is the error BuildConsumers wraps when discovery shows
// that patching this VM in place is not safe, so the operator should
// redeploy instead.
var ErrPatchUnsafe = errors.New("certscripts: patch in place is not safe on this VM")

// Match is one file that discovery found. For a whole-file match, Path is
// a pinned target, Hash is the normalised hash of the whole file, and
// Owner, Mode, and RootDirs describe it. For an embedded match, Path is
// the file that holds the block and Hash is the block's hash, and the
// other fields are empty.
type Match struct {
	Job   string
	Path  string
	Hash  string
	Owner string
	Mode  string
	// RootDirs says that every directory from / down to the file is
	// root's and not writable by group or other, and that the file has
	// one link, so the checks a write makes would pass.
	RootDirs bool
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
		"jobdir":   {keyJob: true, keyPath: true},
		"match":    {keyJob: true, keyPath: true, keyHash: true, "owner": true, "mode": true, "rootdirs": true},
		"embedded": {keyJob: true, keyPath: true, keyHash: true},
		"proc":     {keyJob: true, "name": true},
	}
}

// The forms of the values in discovery output.
var (
	jobName     = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	processName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@+-]*$`)
	hashValue   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ownerValue  = regexp.MustCompile(`^[A-Za-z0-9_.-]+:[A-Za-z0-9_.-]+$`)
	modeValue   = regexp.MustCompile(`^[0-7]{4}$`)
)

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
	errProcess      = errors.New("the process name is malformed")
)

// ParseDiscovery reads discover.sh output. It checks every line, and it
// runs ValidPinnedTarget on every match and every embedded file against
// its job's jobdir line, so a file outside its job's resolved directory,
// a job directory that is not /var/vcap/data/jobs/<job>/<version>, and a
// value the script printed as ! all reject the whole output. Matches and
// embedded files come back sorted by job and path.
func ParseDiscovery(out []byte) (*Discovery, error) {
	records, err := ParseRecords(out, discoveryKinds())
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
			return nil, fmt.Errorf("%w: line %d: %w", ErrMalformedOutput, record.Line, err)
		}
	}

	for _, record := range records {
		err := found.add(record)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrMalformedOutput, record.Line, err)
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
		return errJobDir
	}

	if _, seen := d.JobDirs[job]; seen {
		return errJobRepeated
	}

	d.JobDirs[job] = path

	return nil
}

func (d *Discovery) add(record Record) error {
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

// file reads a match line, with its owner, mode, and rootdirs when whole
// is set, or an embedded line.
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
		return Match{}, err
	}

	if !hashValue.MatchString(match.Hash) {
		return Match{}, errHash
	}

	if !whole {
		return match, nil
	}

	values, err = fields(pairs, "owner", "mode", "rootdirs")
	if err != nil {
		return Match{}, err
	}

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
	case "no":
	default:
		return Match{}, errRootDirs
	}

	return match, nil
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
// would make (rootdirs=no), and when a selected variable has no consumer
// on this VM. Consumers come back sorted by job, variable, and paths.
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
			return nil, disc.unsafe("the file %s of the variable %s is on a path that someone other than root can change, or has a second link",
				match.Path, strings.Join(names, ", "))
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
// variable, and checks that every variable has a consumer.
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

	found := make(map[string]bool)

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

			sort.Strings(certPaths)
			sort.Strings(keyPaths)

			for i := range certPaths {
				consumers = append(consumers, Consumer{
					Job: job, Variable: variable, CertPath: certPaths[i], KeyPath: keyPaths[i],
					Processes: slices.Clone(d.Processes[job]),
				})
			}

			found[variable] = true
		}
	}

	for _, variable := range variables {
		if !found[variable] {
			return nil, d.unsafe("the variable %s has no consumer", variable)
		}
	}

	return consumers, nil
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
