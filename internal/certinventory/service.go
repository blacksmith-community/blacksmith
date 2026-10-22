package certinventory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/credhub"
	"blacksmith/pkg/logger"
)

const (
	directorInfoTTL     = 5 * time.Minute
	directorInfoTimeout = 10 * time.Second
	directorInfoBackoff = 30 * time.Second
	settingsLoadTimeout = 15 * time.Second
	credhubProbeSegment = "blacksmith-credhub-probe"
)

// credhubFinder is the one CredHub call the capability probe makes.
type credhubFinder interface {
	FindByPath(ctx context.Context, path string) ([]string, error)
}

// ServiceDeps is everything the Service is built from.
type ServiceDeps struct {
	// Scanner holds the readers. Its CredHub field stays empty, because
	// NewService fills it from CredHubClient only when that is non-nil.
	Scanner ScannerDeps
	// CredHubClient is nil when the broker has no CredHub block.
	CredHubClient *credhub.Client
	List          InstanceLister
	Broker        func() BrokerSources
	// Settings is optional. When nil, the Service reads and writes settings
	// through Scanner.Vault.
	Settings *SettingsStore

	InventoryEnabled bool
	RotationEnabled  bool
	PatchEnabled     bool

	Log   logger.Logger
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
}

// Service ties the scanner, cache, scheduler, and settings together and
// answers the inventory API from one cached Fleet.
type Service struct {
	deps    ServiceDeps
	scanner *Scanner
	cache   *Cache
	sched   *Scheduler
	store   *SettingsStore
	finder  credhubFinder
	log     logger.Logger

	// aggregate is a seam so a test can count rebuilds.
	aggregate func([]InstanceScan, Settings, Capabilities, time.Time) *Fleet

	mu         sync.Mutex
	fleet      *Fleet
	caps       Capabilities
	builtGen   uint64
	builtSet   Settings
	builtCapV  uint64
	hasBuilt   bool
	capVersion uint64

	probeState  credhub.CapabilityState
	probeReason string

	// infoMu guards the cached director info. It is separate from mu, so a
	// slow director never holds up a caller that needs mu.
	infoMu       sync.Mutex
	info         *bosh.Info
	infoAt       time.Time
	infoFailedAt time.Time
	infoFetching bool

	// sentCA is the services CA name the last broker scan was given, or the
	// name a rescan was started for.
	sentCA       string
	configCAOnce sync.Once
	configCA     string

	// noDirector is true when the broker has no BOSH director.
	noDirector bool
}

// ErrDirectorUnavailable is what every director call answers when the broker
// has no BOSH director.
var ErrDirectorUnavailable = errors.New("BOSH director unavailable")

// missingDirector stands in for a director the broker does not have, so a
// scan fails with a reason instead of dereferencing nil.
type missingDirector struct{}

func (missingDirector) GetInfo(context.Context) (*bosh.Info, error) {
	return nil, ErrDirectorUnavailable
}

func (missingDirector) GetDeployment(context.Context, string) (*bosh.DeploymentDetail, error) {
	return nil, ErrDirectorUnavailable
}

func (missingDirector) GetDeploymentVMs(context.Context, string) ([]bosh.VM, error) {
	return nil, ErrDirectorUnavailable
}

func (missingDirector) ListVariables(context.Context, string) ([]bosh.Variable, error) {
	return nil, ErrDirectorUnavailable
}

// NewService builds a Service. It starts nothing; call Start.
func NewService(deps ServiceDeps) *Service {
	if deps.Now == nil {
		deps.Now = time.Now
	}

	log := deps.Log
	if log == nil {
		log = logger.NewNoOpLogger()
	}

	s := &Service{
		deps:      deps,
		cache:     &Cache{},
		log:       log,
		aggregate: Aggregate,
		store:     deps.Settings,
	}

	if s.store == nil {
		s.store = NewSettingsStore(deps.Scanner.Vault)
	}

	if deps.Scanner.Director == nil {
		deps.Scanner.Director = missingDirector{}
		s.noDirector = true
	}

	// Only a real client goes into the interface, so a missing one stays a
	// true nil and the scanner reports "CredHub not configured".
	if deps.CredHubClient != nil {
		deps.Scanner.CredHub = deps.CredHubClient
		s.finder = deps.CredHubClient
		s.probeState = credhub.StateConfigured
	}

	deps.Scanner.Settings = s.store.Current
	deps.Scanner.Now = deps.Now

	if deps.Scanner.CredHubState == nil {
		deps.Scanner.CredHubState = s.credhubState
	}

	s.deps = deps
	s.scanner = NewScanner(deps.Scanner)
	s.sched = NewScheduler(s.scanner, s.cache, deps.List, s.brokerSources, s.store.Current, deps.Now, deps.After)

	return s
}

// CredHubState reports whether CredHub is usable now, with a fixed reason
// when it is not.
func (s *Service) CredHubState() (credhub.CapabilityState, string) {
	return s.credhubState()
}

func (s *Service) credhubState() (credhub.CapabilityState, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.credhubStateLocked()
}

func (s *Service) credhubStateLocked() (credhub.CapabilityState, string) {
	if s.deps.CredHubClient == nil {
		return credhub.StateNotConfigured, ""
	}

	return s.probeState, s.probeReason
}

// SetCredHubProbe records the outcome of a CredHub probe. A nil error means
// CredHub answered. The reason is a fixed sentence, never the error text.
func (s *Service) SetCredHubProbe(err error) {
	if s.deps.CredHubClient == nil {
		return
	}

	state := credhub.ClassifyProbe(err)

	reason := ""

	switch state {
	case credhub.StateUnreachable:
		reason = "CredHub could not be reached."
	case credhub.StateRefused:
		reason = "CredHub refused the broker's credentials."
	case credhub.StateConfigured, credhub.StateNotConfigured:
	}

	s.mu.Lock()
	s.probeState, s.probeReason = state, reason
	s.capVersion++
	s.mu.Unlock()
}

// ProbeCredHub lists a path that never exists and records the outcome. It
// does nothing when the broker has no CredHub client.
func (s *Service) ProbeCredHub(ctx context.Context) {
	if s.finder == nil {
		return
	}

	director := s.deps.Scanner.DirectorName
	if director == "" {
		director = s.directorName()
	}

	ctx, cancel := context.WithTimeout(ctx, credhub.ProbeTimeout)
	defer cancel()

	_, err := s.finder.FindByPath(ctx, "/"+director+"/"+credhubProbeSegment+"/")
	s.SetCredHubProbe(err)
}

// Enabled reports whether the inventory switch is on.
func (s *Service) Enabled() bool { return s.deps.InventoryEnabled }

// Start loads the saved settings and, when the inventory is on, starts the
// scan scheduler. A failed load keeps the defaults.
func (s *Service) Start(ctx context.Context) {
	if !s.deps.InventoryEnabled {
		return
	}

	if s.deps.Scanner.Vault != nil {
		loadCtx, cancel := context.WithTimeout(ctx, settingsLoadTimeout)
		_, err := s.store.Load(loadCtx)

		cancel()

		if err != nil {
			s.log.Errorf("certificate settings could not be loaded, so the defaults stay in effect: %v", err)
		}
	}

	s.sched.Start(ctx)
}

// Stop stops the scheduler and waits for running scans to end.
func (s *Service) Stop() { s.sched.Stop() }

// RequestScan starts a fleet scan, or a scan of ids, and returns its ID.
func (s *Service) RequestScan(ids []string) (string, bool) { return s.sched.RequestScan(ids) }

// ScanInstance scans one instance now.
func (s *Service) ScanInstance(ctx context.Context, id string) (InstanceScan, error) {
	return s.sched.ScanInstanceNow(ctx, id)
}

// Settings returns the current settings.
func (s *Service) Settings() Settings { return s.store.Current() }

// SaveSettings validates and stores new settings.
func (s *Service) SaveSettings(ctx context.Context, st Settings) error {
	return s.store.Save(ctx, st)
}

// Response answers the inventory for filter from the cached fleet.
func (s *Service) Response(_ context.Context, filter Filter) InventoryResponse {
	fleet, set, caps := s.current()

	return BuildResponse(fleet, filter, set, caps, s.sched.State(), s.deps.Now())
}

// Item returns one certificate with its public PEM.
func (s *Service) Item(id string) (ItemDetail, bool) {
	fleet, set, _ := s.current()

	detail, ok := fleet.Item(id)
	if !ok {
		return ItemDetail{}, false
	}

	applyExpiry(&detail.Certificate, set, s.deps.Now())
	detail.Attention = NeedsAttention(&detail.Certificate)

	return detail, true
}

// current returns the fleet, rebuilding it only when the cache, the
// settings, or the capabilities changed since the last build. The director
// is asked outside every lock.
func (s *Service) current() (*Fleet, Settings, Capabilities) {
	set := s.store.Current()
	gen := s.cache.Generation()
	info := s.directorInfo()

	s.mu.Lock()
	defer s.mu.Unlock()

	caps := s.capabilitiesLocked(set, info)

	if s.hasBuilt && s.builtGen == gen && s.builtSet == set && s.builtCapV == s.capVersion && s.caps == caps {
		return s.fleet, set, s.caps
	}

	s.fleet = s.aggregate(s.cache.Snapshot(), set, caps, s.deps.Now())
	s.caps = caps
	s.builtGen, s.builtSet, s.builtCapV, s.hasBuilt = gen, set, s.capVersion, true

	s.rescanBrokerLocked()

	return s.fleet, set, caps
}

func (s *Service) capabilitiesLocked(set Settings, info *bosh.Info) Capabilities {
	state, reason := s.credhubStateLocked()

	caps := Capabilities{
		CredHub:             state,
		CredHubReason:       reason,
		DirectorName:        s.deps.Scanner.DirectorName,
		DirectorUnavailable: s.noDirector,
		RotationEnabled:     s.deps.RotationEnabled,
		PatchEnabled:        s.deps.PatchEnabled,
		ProbeEnabled:        set.ProbeEnabled,
	}

	switch {
	case s.noDirector:
		caps.ForceLatestReason = ErrDirectorUnavailable.Error()
	case info != nil:
		caps.DirectorVersion = info.Version
		caps.ForceLatest, caps.ForceLatestReason = bosh.ForceLatestVariablesSupported(info.Version)

		if caps.DirectorName == "" {
			caps.DirectorName = info.Name
		}
	default:
		caps.ForceLatestReason = "the director did not report a version"
	}

	return caps
}

// directorInfo returns the director's info. A success is cached for five
// minutes and a failure for 30 seconds. One caller at a time asks the
// director, and any other caller gets the cached value at once instead of
// waiting. No lock of the service is held during the call.
func (s *Service) directorInfo() *bosh.Info {
	if s.noDirector {
		return nil
	}

	now := s.deps.Now()

	s.infoMu.Lock()

	fresh := s.info != nil && now.Sub(s.infoAt) < directorInfoTTL
	backingOff := !s.infoFailedAt.IsZero() && now.Sub(s.infoFailedAt) < directorInfoBackoff

	if fresh || backingOff || s.infoFetching {
		info := s.info
		s.infoMu.Unlock()

		return info
	}

	s.infoFetching = true
	s.infoMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), directorInfoTimeout)
	defer cancel()

	info, err := s.deps.Scanner.Director.GetInfo(ctx)

	s.infoMu.Lock()
	defer s.infoMu.Unlock()

	s.infoFetching = false

	if err != nil || info == nil {
		s.infoFailedAt = now

		return s.info
	}

	s.info, s.infoAt, s.infoFailedAt = info, now, time.Time{}

	return info
}

func (s *Service) directorName() string {
	if info := s.directorInfo(); info != nil {
		return info.Name
	}

	return ""
}

// brokerSources adds the services CA name, read from the cached scans, to
// the sources the broker was configured with.
func (s *Service) brokerSources() BrokerSources {
	var src BrokerSources
	if s.deps.Broker != nil {
		src = s.deps.Broker()
	}

	if src.ServicesCAName != "" {
		return src
	}

	if name := s.servicesCAFromCache(); name != "" {
		src.ServicesCAName = name

		s.mu.Lock()
		s.sentCA = name
		s.mu.Unlock()
	}

	return src
}

// servicesCAFromCache returns the services CA name any cached instance scan
// names, or an empty string.
func (s *Service) servicesCAFromCache() string {
	for _, scan := range s.cache.Snapshot() {
		for name := range scan.Material.SigningCAs {
			if name[strings.LastIndex(name, "/")+1:] == servicesCAVariable {
				return name
			}
		}
	}

	return ""
}

// rescanBrokerLocked starts one broker scan the first time the services CA
// name becomes known after the last broker scan ran without it, so drift
// detection does not wait a whole interval.
func (s *Service) rescanBrokerLocked() {
	if !s.deps.InventoryEnabled || s.deps.Broker == nil || s.sentCA != "" {
		return
	}

	name := s.servicesCAFromCache()
	if name == "" {
		return
	}

	s.configCAOnce.Do(func() { s.configCA = s.deps.Broker().ServicesCAName })

	if s.configCA != "" {
		return
	}

	s.sentCA = name

	go func() { _, _ = s.sched.ScanInstanceNow(context.Background(), BrokerScanID) }()
}
