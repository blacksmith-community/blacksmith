// Command devpush copies a dev build of Blacksmith onto a broker VM and
// restarts it, going through the BOSH director's SSH route instead of the
// bosh CLI.
//
// Usage:
//
//	go run ./hack/devpush -d <deployment> -i <group/id> [-binary blacksmith-linux-amd64]
//
// The director comes from the variables the kit's bosh addon prints,
// BOSH_ENVIRONMENT, BOSH_CA_CERT, BOSH_CLIENT, and BOSH_CLIENT_SECRET. The
// instance is the group and the instance ID, as in redis/<uuid>, and the
// binary must be a linux x86-64 executable, which make linux builds. The tool
// asks the director to create a throwaway SSH user on the instance, copies
// the binary over SSH with that user, restarts blacksmith under monit, and
// then asks the director to remove the user again.
package main

import (
	"context"
	"debug/elf"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/fivetwenty-io/boshapi/pkg/boshclient"
)

const (
	// exitFailure is the exit status of a push that did not finish.
	exitFailure = 1

	// defaultBinary is what make linux builds.
	defaultBinary = "blacksmith-linux-amd64"

	// defaultDirectorPort is the port a director listens on, which the tool
	// adds to a BOSH_ENVIRONMENT that names none.
	defaultDirectorPort = "25555"

	// directorRequestTimeout bounds each attempt at a director request.
	directorRequestTimeout = 60 * time.Second

	// directorRetries is how many times boshapi retries a request that is
	// safe to send again.
	directorRetries = 3

	usageText = "usage: devpush -d <deployment> -i <group/id> [-binary " + defaultBinary + "]"

	// elfMagicLength is how many bytes identify a file's format.
	elfMagicLength = 4
)

// The names of the variables the kit's bosh addon prints.
const (
	envDirector = "BOSH_ENVIRONMENT"
	envCACert   = "BOSH_CA_CERT"
	envClient   = "BOSH_CLIENT"
	envSecret   = "BOSH_CLIENT_SECRET" //nolint:gosec // the name of a variable, not a credential
	envProxy    = "BOSH_ALL_PROXY"
)

var (
	// errUsage means the command line is incomplete or malformed.
	errUsage = errors.New("the command line is not usable")
	// errEnvironment means a BOSH_* variable is missing or unusable.
	errEnvironment = errors.New("the director settings are not usable")
	// errBinary means the file to push is not a linux x86-64 executable.
	errBinary = errors.New("the binary to push is not usable")
	// errInstance means the -i value does not name an instance by ID.
	errInstance = errors.New("the instance is not usable")
)

// instanceIDForm is the canonical form of the instance IDs the director
// assigns.
var instanceIDForm = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// hostNameForm is a DNS name the tool accepts as a director address.
var hostNameForm = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)

// oneWordForm is a host name of a single label, such as a bosh CLI alias.
var oneWordForm = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$`)

// options are the command-line flags.
type options struct {
	deployment string
	instance   string
	binary     string
}

// environment is the director connection the BOSH_* variables describe.
type environment struct {
	// director is the director's URL with a scheme and a port.
	director string
	// caCert is the director's CA as PEM text or a path, as boshapi reads it.
	caCert string
	client string
	secret string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)

	stop()

	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "devpush: %v\n", err)

		os.Exit(exitFailure)
	}
}

// run checks everything it can without the network, then pushes. Each check
// that fails stops the tool before it sends the director a request.
func run(ctx context.Context, args []string, getenv func(string) string, out, errOut io.Writer) error {
	opts, err := parseOptions(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintln(errOut, usageText)

			return nil
		}

		return err
	}

	target, err := parseInstance(opts.instance)
	if err != nil {
		return err
	}

	env, err := readEnvironment(getenv)
	if err != nil {
		return err
	}

	err = checkBinary(opts.binary)
	if err != nil {
		return err
	}

	client, err := newClient(ctx, env)
	if err != nil {
		return err
	}

	return newPusher(client, opts.deployment, target, opts.binary, out).push(ctx)
}

// parseOptions reads the flags and refuses a command line that leaves out
// the deployment or the instance.
func parseOptions(args []string) (options, error) {
	var opts options

	flags := flag.NewFlagSet("devpush", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.deployment, "d", "", "the BOSH deployment that holds the broker")
	flags.StringVar(&opts.instance, "i", "", "the broker instance, as <group>/<instance ID>")
	flags.StringVar(&opts.binary, "binary", defaultBinary, "the linux x86-64 blacksmith binary to push")

	err := flags.Parse(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, err //nolint:wrapcheck // run recognises flag.ErrHelp itself
		}

		return options{}, fmt.Errorf("%w: %w. %s", errUsage, err, usageText)
	}

	if flags.NArg() > 0 {
		return options{}, fmt.Errorf("%w: unexpected argument %q. %s", errUsage, flags.Arg(0), usageText)
	}

	switch {
	case opts.deployment == "":
		return options{}, fmt.Errorf("%w: -d names no deployment. %s", errUsage, usageText)
	case opts.instance == "":
		return options{}, fmt.Errorf("%w: -i names no instance. %s", errUsage, usageText)
	case opts.binary == "":
		return options{}, fmt.Errorf("%w: -binary is empty. %s", errUsage, usageText)
	default:
		return opts, nil
	}
}

// parseInstance turns group/id into the SSH target that reaches the
// instance by its ID. A numeric index is refused, because it would become an
// ID that the director never matches.
func parseInstance(value string) (boshapi.SSHTarget, error) {
	group, instanceID, found := strings.Cut(value, "/")
	if !found || group == "" || instanceID == "" || strings.Contains(instanceID, "/") {
		return boshapi.SSHTarget{}, fmt.Errorf("%w: -i is %q, but it must be <group>/<instance ID>, such as blacksmith/%s",
			errInstance, value, "5d1b8f5c-2c43-4a4e-9a57-2f8a3e0d6c11")
	}

	if !instanceIDForm.MatchString(instanceID) {
		return boshapi.SSHTarget{}, fmt.Errorf("%w: the instance %q in -i is not an instance ID (a UUID). "+
			"Use the instance ID from /b/deployments/<name>/instances, because the director matches an instance by its ID and would never match an index",
			errInstance, instanceID)
	}

	return boshapi.SSHTarget{Job: group, IDs: []string{instanceID}}, nil
}

// readEnvironment reads the director settings from the variables the kit's
// bosh addon prints. It refuses a BOSH_ALL_PROXY, because the tool dials the
// VM directly, and it names every variable that is missing.
func readEnvironment(getenv func(string) string) (environment, error) {
	if getenv(envProxy) != "" {
		return environment{}, fmt.Errorf("%w: %s is set, but devpush supports only direct access to the VMs. "+
			"Unset %s and run devpush from a host that can reach the instance's SSH port directly",
			errEnvironment, envProxy, envProxy)
	}

	var missing []string

	values := make(map[string]string, 4) //nolint:mnd // one entry per variable below

	for _, name := range []string{envDirector, envCACert, envClient, envSecret} {
		values[name] = getenv(name)
		if values[name] == "" {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		return environment{}, fmt.Errorf("%w: %s not set. Set them from the output of the kit's bosh addon, "+
			"which prints BOSH_ENVIRONMENT, BOSH_CA_CERT, BOSH_CLIENT, and BOSH_CLIENT_SECRET",
			errEnvironment, variableList(missing))
	}

	director, err := directorURL(values[envDirector])
	if err != nil {
		return environment{}, err
	}

	return environment{
		director: director,
		caCert:   values[envCACert],
		client:   values[envClient],
		secret:   values[envSecret],
	}, nil
}

// variableList names the variables, with "is" or "are" after them.
func variableList(names []string) string {
	if len(names) == 1 {
		return names[0] + " is"
	}

	return strings.Join(names, ", ") + " are"
}

// directorURL gives a BOSH_ENVIRONMENT the scheme and the port a director
// URL needs. A value that is not an address, such as a bosh CLI alias, is
// refused. A single word with no dot is taken for an alias, because a
// director is reached by an IP address or a DNS name that has a domain.
func directorURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}

	parsed, err := url.Parse(value)
	if err == nil && oneWordHost(parsed) {
		return "", fmt.Errorf("%w: %s is %q, but it must be the director's address. The tool reads a one-word host such as %q "+
			"as a bosh CLI environment alias, so give the director's IP address or a fully qualified name, "+
			"such as 127.0.0.1 or bosh.example.com", errEnvironment, envDirector, raw, parsed.Hostname())
	}

	if err != nil || !usableDirectorURL(parsed) {
		return "", fmt.Errorf("%w: %s is %q, but it must be the director's address, such as https://10.0.0.6:%s, "+
			"and not a bosh CLI environment alias", errEnvironment, envDirector, raw, defaultDirectorPort)
	}

	port := parsed.Port()
	if port == "" {
		port = defaultDirectorPort
	}

	return parsed.Scheme + "://" + net.JoinHostPort(parsed.Hostname(), port), nil
}

// oneWordHost reports whether parsed is an http or https URL whose host is a
// single word with no dot, which the tool takes for a bosh CLI alias.
func oneWordHost(parsed *url.URL) bool {
	return (parsed.Scheme == "https" || parsed.Scheme == "http") && oneWordForm.MatchString(parsed.Hostname())
}

// usableDirectorURL reports whether parsed is an http or https URL whose
// host is an IP address or a DNS name with a domain.
func usableDirectorURL(parsed *url.URL) bool {
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return false
	}

	host := parsed.Hostname()

	return net.ParseIP(host) != nil || hostNameForm.MatchString(host)
}

// newClient builds the boshapi client in discover mode, so the director says
// whether it uses UAA or HTTP Basic. It sends no request.
func newClient(ctx context.Context, env environment) (boshapi.Client, error) {
	client, err := boshclient.New(ctx, &boshapi.Config{
		DirectorURL:  env.director,
		ClientID:     env.client,
		ClientSecret: env.secret,
		Auth:         boshapi.AuthDiscover,
		CACert:       env.caCert,
		HTTPTimeout:  directorRequestTimeout,
		RetryMax:     directorRetries,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: building the director client for %s failed: %w. "+
			"Check that %s holds the director's CA certificate as PEM text or as the path of a readable file",
			errEnvironment, env.director, err, envCACert)
	}

	return client, nil
}

// checkBinary refuses a file that is not a linux x86-64 executable, so a
// binary built for the developer's own machine never reaches the VM.
func checkBinary(path string) error {
	found, err := describeBinary(path)
	if err != nil {
		return err
	}

	if found != "" {
		return fmt.Errorf("%w: %s is %s, not a linux x86-64 executable. Build the right one with make linux, "+
			"which writes %s", errBinary, path, found, defaultBinary)
	}

	return nil
}

// describeBinary returns what the file at path is, or an empty string when
// it is a linux x86-64 ELF executable.
func describeBinary(path string) (string, error) {
	file, err := os.Open(path) //nolint:gosec // the path is the developer's own -binary flag
	if err != nil {
		return "", fmt.Errorf("%w: open %s: %w. Build the binary with make linux, which writes %s",
			errBinary, path, err, defaultBinary)
	}

	defer func() { _ = file.Close() }()

	magic := make([]byte, elfMagicLength)

	read, err := io.ReadFull(file, magic)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: read %s: %w", errBinary, path, err)
	}

	if string(magic[:read]) != elf.ELFMAG {
		return describeForeignFile(magic[:read]), nil
	}

	parsed, err := elf.NewFile(file)
	if err != nil {
		return fmt.Sprintf("an ELF file that does not parse (%v)", err), nil
	}

	defer func() { _ = parsed.Close() }()

	return describeELF(parsed), nil
}

// describeELF returns what an ELF file is, or an empty string when it is a
// 64-bit little-endian x86-64 executable built for linux.
func describeELF(parsed *elf.File) string {
	linux := parsed.OSABI == elf.ELFOSABI_NONE || parsed.OSABI == elf.ELFOSABI_LINUX
	executable := parsed.Type == elf.ET_EXEC || parsed.Type == elf.ET_DYN

	if linux && executable && parsed.Machine == elf.EM_X86_64 && parsed.Class == elf.ELFCLASS64 && parsed.Data == elf.ELFDATA2LSB {
		return ""
	}

	system := "linux"
	if !linux {
		system = strings.ToLower(strings.TrimPrefix(parsed.OSABI.String(), "ELFOSABI_"))
	}

	machine := machineName(parsed.Machine)

	kind := "executable"
	if !executable {
		kind = strings.ToLower(strings.TrimPrefix(parsed.Type.String(), "ET_")) + " file"
	}

	return fmt.Sprintf("a %s %s ELF %s", system, machine, kind)
}

// describeForeignFile names the kind of file whose first bytes are magic,
// when it is not an ELF file.
func describeForeignFile(magic []byte) string {
	machOMagics := [][]byte{
		{0xfe, 0xed, 0xfa, 0xce}, {0xfe, 0xed, 0xfa, 0xcf}, {0xce, 0xfa, 0xed, 0xfe}, {0xcf, 0xfa, 0xed, 0xfe}, {0xca, 0xfe, 0xba, 0xbe},
	}

	for _, machO := range machOMagics {
		if string(magic) == string(machO) {
			return "a Mach-O executable, which is what go build makes on a Mac"
		}
	}

	switch {
	case len(magic) >= 2 && string(magic[:2]) == "MZ": //nolint:mnd // the two-byte DOS header signature
		return "a Windows executable"
	case len(magic) >= 2 && string(magic[:2]) == "#!": //nolint:mnd // the two-byte script signature
		return "a script"
	case len(magic) < elfMagicLength:
		return fmt.Sprintf("a file of only %d bytes", len(magic))
	default:
		return "a file that is not an ELF executable"
	}
}

// machineName names a machine the way a developer says it, and falls back to
// the ELF constant for a rare one.
func machineName(machine elf.Machine) string {
	switch machine { //nolint:exhaustive // only the machines a developer builds for have names of their own
	case elf.EM_X86_64:
		return "x86-64"
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_386:
		return "x86"
	case elf.EM_ARM:
		return "arm"
	default:
		return strings.ToLower(strings.TrimPrefix(machine.String(), "EM_"))
	}
}
