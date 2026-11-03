package operators

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// UNDEFINED_AZ is a placeholder for undefined availability zones.
const UNDEFINED_AZ = "__UNDEFINED_AZ__"

// staticIPClaimMu serializes the check-then-claim sequence against an
// engine's used-IP tracking. graft.OperatorState's GetUsedIPs/SetUsedIP
// pair is individually thread-safe (each call takes the engine's own
// lock), but reading the map and then writing to it are two separate
// operations. Without an operator-level lock around both, two
// static_ips evaluations racing on the same IP could both observe the
// address as free and both claim it, silently handing the same static
// IP to two BOSH instance groups. Parallel evaluation is becoming the
// default, so this sequence must be atomic regardless of how many
// operator evaluations the scheduler runs concurrently.
var staticIPClaimMu sync.Mutex

// claimStaticIP atomically checks whether ip is already claimed in
// state and, if not, claims it for owner. It returns the name of the
// existing claimant and a non-nil error (matching spruce's wording)
// when the IP was already taken.
func claimStaticIP(state graft.OperatorState, ip, owner string) (string, error) {
	staticIPClaimMu.Lock()
	defer staticIPClaimMu.Unlock()

	if thief, taken := state.GetUsedIPs()[ip]; taken {
		return thief, ansi.Errorf("@R{tried to use IP '}@c{%s}@R{', but that address is already allocated to} @c{%s}", ip, thief)
	}

	state.SetUsedIP(ip, owner)
	return "", nil
}

// StaticIPOperator ...
type StaticIPOperator struct{}

// Setup ...
func (StaticIPOperator) Setup() error {
	return nil
}

// OrderSensitive reports true: which instance group's claim wins a
// duplicate-IP-claim error (see staticIPClaimMu/claimStaticIP) depends on
// relative evaluation order among static_ips calls, so the parallel
// scheduler must not dispatch static_ips concurrently with other
// same-wave static_ips calls.
func (StaticIPOperator) OrderSensitive() bool {
	return true
}

// Phase ...
func (StaticIPOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies ...
func (StaticIPOperator) Dependencies(ev *Evaluator, _ []*Expr, _ []*tree.Cursor, _ []*tree.Cursor) []*tree.Cursor {
	l := []*tree.Cursor{}

	track := func(path string) {
		c, err := tree.ParseCursor(path)
		if err != nil {
			return
		}
		keys, err := c.Glob(ev.Tree)
		if err != nil {
			return
		}
		l = append(l, keys...)
	}

	// top level stuff
	track("networks")
	track("networks.*")
	track("jobs")
	track("jobs.*")
	track("instance_groups")
	track("instance_groups.*")

	// need all the network name decls
	track("networks.*.name")
	track("networks.*.subnets")

	// need all the static range decls
	track("networks.*.subnets.*.static")

	// need all the az decls
	track("networks.*.subnets.*.az")
	track("networks.*.subnets.*.azs")
	track("networks.*.subnets.*.static.*")

	// need all the job instance count decls
	track("jobs.*.instances")
	track("instance_groups.*.instances")

	// need all the job network name decls
	track("jobs.*.networks.*.name")
	track("instance_groups.*.networks.*.name")

	// need all the instance_group azs decls
	track("instance_groups.*.azs")
	track("instance_groups.*.azs.*")

	return l
}

func currentJob(ev *Evaluator) (*tree.Cursor, error) {
	c := ev.Here.Copy()
	for c.Depth() > 0 && c.Parent() != "jobs" && c.Parent() != "instance_groups" {
		c.Pop()
	}

	if c.Depth() == 0 {
		return nil, fmt.Errorf("not currently inside of a job definition block")
	}
	return c, nil
}

func instances(ev *Evaluator, job *tree.Cursor) (int, error) {
	c := job.Copy()
	c.Push("instances")
	inst, err := c.ResolveString(ev.Tree)
	if err != nil {
		return 0, err
	}

	i, err := strconv.ParseInt(inst, 10, 0)
	if err != nil {
		return 0, ansi.Errorf("@R{the `}@c{instances:}@R{` for the current job is not numeric}")
	}
	if i < 0 {
		return 0, ansi.Errorf("@R{negative number found in `}@c{instances:}@R{` for the current job}")
	}
	return int(i), nil
}

//nolint:gocyclo // statics parses BOSH network configuration with AZs and IP ranges
func statics(ev *Evaluator) (map[string][]string, []string, error) {
	addrs := map[string][]string{}
	azs := []string{}

	c := ev.Here.Copy()
	c.Pop()
	c.Push("name")
	name, err := c.ResolveString(ev.Tree)
	if err != nil {
		return addrs, azs, err
	}

	// Build the cursor from nodes rather than parsing a dotted string,
	// so a network name that contains dots (as every Genesis 3 cloud
	// config network name does) stays one node and is matched whole.
	c = &tree.Cursor{Nodes: []string{"networks", name, "subnets", "*"}}
	keys, err := c.Glob(ev.Tree)
	if err != nil {
		return addrs, azs, err
	}

	for _, key := range keys {
		r, err := key.Canonical(ev.Tree)
		if err != nil {
			return addrs, azs, err
		}

		// list of azs associated with this specific subnet
		// do not confuse with `azs`, which is a list of
		// all `azs` for the network.
		subnet_zones := []string{}

		// look for az definition in the `az` key
		azCursor := r.Copy()
		azCursor.Push("az")
		z, resolveErr := azCursor.ResolveString(ev.Tree)
		if resolveErr == nil && z != "" {
			azs = append(azs, z) // to preserve subnet ordering
			subnet_zones = append(subnet_zones, z)
		}

		// look for az definitions in the `azs` key
		c = r.Copy()
		c.Push("azs")
		os, err := c.Resolve(ev.Tree)
		if err == nil {
			if zs, ok := os.([]interface{}); ok {
				for _, o := range zs {
					if z, ok := o.(string); ok && z != "" {
						azs = append(azs, z)
						subnet_zones = append(subnet_zones, z)
					}
				}
			}
		}

		// add a default zone for azs + subnet zones, if
		// this network has no zones specified
		if len(subnet_zones) == 0 {
			azs = append(azs, "z1")
			subnet_zones = append(subnet_zones, "z1")
		}

		// a subnet with no static key is valid in BOSH: it only hands out
		// dynamic IPs. Count it as an empty static pool for its zones.
		c = r.Copy()
		c.Push("static")
		if _, err := c.Resolve(ev.Tree); errors.Is(err, tree.ErrNotFound) {
			for _, az := range subnet_zones {
				if _, ok := addrs[az]; !ok {
					addrs[az] = []string{}
				}
			}
			continue
		}

		c = r.Copy()
		c.Push("static")
		c.Push("*")
		keys, err := c.Glob(ev.Tree)
		if err != nil {
			return addrs, azs, err
		}

		for _, key := range keys {
			r, err := key.Resolve(ev.Tree)
			if err != nil {
				return addrs, azs, err
			}

			rStr, ok := r.(string)
			if !ok {
				return addrs, azs, ansi.Errorf("@c{%s} @R{is not a well-formed BOSH network}", name)
			}

			segments := strings.Split(rStr, "-")
			for i, s := range segments {
				segments[i] = strings.TrimSpace(s)
			}

			start := net.ParseIP(segments[0])
			if start == nil {
				return nil, azs, ansi.Errorf("@c{%s}@R{: not a valid IP address}", segments[0])
			}

			for _, az := range subnet_zones {
				addrs[az] = append(addrs[az], start.String())
				if len(segments) == 1 {
					continue
				}
			}

			if len(segments) == 2 {
				end := net.ParseIP(segments[1])
				if end == nil {
					return nil, azs, ansi.Errorf("@c{%s}@R{: not a valid IP address}", segments[1])
				}

				if binary.BigEndian.Uint32(start.To4()) > binary.BigEndian.Uint32(end.To4()) {
					return nil, azs, ansi.Errorf("@R{Static IP pool }@c{[%s - %s]} @R{ends before it starts}", start, end)
				}

				for !start.Equal(end) {
					incrementIP(start, len(start)-1)
					for _, az := range subnet_zones {
						addrs[az] = append(addrs[az], start.String())
					}
				}
			}
		}
	}
	return addrs, azs, nil
}

func allIPs(pools map[string][]string, azs []string) []string {
	var ips []string
	seen := map[string]bool{}

	for _, az := range azs {
		pool, ok := pools[az]
		if !ok {
			continue
		}
		for _, ip := range pool {
			if !seen[ip] {
				ips = append(ips, ip)
				seen[ip] = true
			}
		}
	}
	return ips
}

//nolint:unparam // return value used for recursive calls; modifies ip in-place
func incrementIP(ip net.IP, i int) net.IP {
	if ip[i] == 255 {
		ip[i] = 0

		// check next octet
		if ip[i-1] == 255 {
			incrementIP(ip, i-1)
		} else {
			ip[i-1]++
		}
	} else {
		ip[i]++
	}
	return ip
}

// Run ...
//
//nolint:gocyclo // static_ips operator handles complex IP pool calculations
func (s StaticIPOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( static_ips ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( static_ips ... )) operation at $%s\n", ev.Here)

	// Pre-allocate with expected capacity (at least len(args) IPs expected)
	ips := make([]interface{}, 0, len(args))

	// detect what job we are in
	DEBUG("  determining what job context (( static_ips ... )) was called in")
	job, err := currentJob(ev)
	if err != nil {
		DEBUG("  failed: %s\n", err)
		return nil, err
	}
	DEBUG("  got it.  $.%s\n", job)

	job.Push("name")
	DEBUG("  extracting job name from $.%s", job)
	jobname, err := job.Resolve(ev.Tree)
	if err != nil {
		DEBUG("  job has no name.  this could be problematic.\n")
		return nil, err
	}
	job.Pop()
	DEBUG("  got it.  job is %s\n", jobname)

	job.Push("azs")
	DEBUG("  extracting azs from $.%s", job)
	var azs []string
	if zs, azsErr := job.Resolve(ev.Tree); azsErr == nil {
		if zsList, ok := zs.([]interface{}); ok {
			for _, z := range zsList {
				zStr, ok := z.(string)
				if !ok {
					DEBUG("  azs %v: '%v' is not a string literal\n", zs, z)
					return nil, ansi.Errorf("@R{azs} @c{%#v} @R{must be a list of strings}", zs)
				}
				azs = append(azs, zStr)
			}
		} else {
			DEBUG("  azs must be a list of strings\n")
			return nil, ansi.Errorf("@R{azs} @c{%#v} @R{must be a list of strings}", zs)
		}
	}
	job.Pop()
	DEBUG("  got it.  azs are %v\n", azs)

	// determine if we have any instances
	DEBUG("  determining how many instances of job %s there are", jobname)
	inst, err := instances(ev, job)
	if err != nil {
		DEBUG("  failed: %s\n", err)
		return nil, err
	}
	if inst == 0 {
		DEBUG("  no instances for this job.  skipping static IP address calculations...\n")
		return &Response{
			Type:  Replace,
			Value: ips,
		}, nil
	}
	DEBUG("  got it.  there are %d instances of %s\n", inst, jobname)

	// check to make sure instances matches requested number of static ips
	DEBUG("  checking to see if the caller asked for enough static_ips to provision all job instances (need at least %d)", inst)
	if len(args) < inst {
		DEBUG("  oops.  you asked for %d IPs for a job with %d instances\n", len(args), inst)
		return nil, ansi.Errorf("@R{not enough static IPs requested for} @c{job of %d instances} @R{(only asked for} @c{%d}@R{)}", inst, len(args))
	}
	DEBUG("  looks good.  asking for %d IPs for a job with %d instances\n", len(args), inst)

	// find our network
	DEBUG("  determining the pool of static IPs from which to provision")
	pools, poolAZs, err := statics(ev)
	DEBUG("  static IP pools: %v", pools)
	DEBUG("  static IP pool AZs: %v", poolAZs)
	if err != nil {
		DEBUG("  failed: %s\n", err)
		return nil, err
	}
	count := 0
	for _, pool := range pools {
		count += len(pool)
	}
	DEBUG("  found %d addresses in the pool\n", count)

	// verify that pools contain all specified AZs, just like BOSH
	for _, az := range azs {
		if _, ok := pools[az]; !ok {
			DEBUG("  could not find AZ %s in network AZS: %v\n", az, azs)
			return nil, ansi.Errorf("@R{could not find AZ} @c{%s} (@R{in network AZS} @c{%v})", az, azs)
		}
	}

	// if no AZs are specified on instance_groups, then just use whatever is in networks / pools
	if len(azs) == 0 {
		azs = append(azs, poolAZs...)
	}

	ord := func(n int64) string {
		switch {
		case n%100 >= 11 && n%100 <= 13:
			return "th"
		case n%10 == 1:
			return "st"
		case n%10 == 2:
			return "nd"
		case n%10 == 3:
			return "rd"
		}
		return "th"
	}

	// build the list of ips, based on offsets
	for i, arg := range args {
		if i >= inst {
			break
		}

		// Use ResolveOperatorArgument to support nested expressions
		val, err := ResolveOperatorArgument(ev, arg)
		if err != nil {
			DEBUG("  arg[%d]: failed to resolve expression to a concrete value", i)
			DEBUG("     [%d]: error was: %s", i, err)
			return nil, err
		}

		current := fmt.Sprintf("%s/%d", jobname, i)

		// parse argument, could be in form of <az>:<number>, or just <number>
		var offset int64
		az := UNDEFINED_AZ

		switch v := val.(type) {
		case string:
			if strings.Contains(v, ":") {
				// must be of format <az>:<number>
				params := strings.SplitN(v, ":", 2)
				az = params[0]
				offset, err = strconv.ParseInt(params[1], 10, 64)
				if err != nil {
					DEBUG("  arg[%d]: '%v' is not a number literal\n", i, v)
					return nil, fmt.Errorf("static_ips operator arguments must have format <az>:<number> or <number>")
				}
			} else {
				offset, err = strconv.ParseInt(v, 10, 64)
				if err != nil {
					DEBUG("  arg[%d]: '%v' is not a number literal\n", i, v)
					return nil, fmt.Errorf("static_ips operator arguments must have format <az>:<number> or <number>")
				}
			}
		case int:
			offset = int64(v)
		case int64:
			offset = v
		case float64:
			offset = int64(v)
		default:
			DEBUG("  arg[%d]: '%v' is not a valid static_ips argument\n", i, val)
			return nil, fmt.Errorf("static_ips operator arguments must have format <az>:<number> or <number>")
		}

		// get IPs to use
		pool := allIPs(pools, azs)
		if az != UNDEFINED_AZ {
			// check if az is actually in instance_groups azs
			var found bool
			for _, z := range azs {
				if az == z {
					found = true
					break
				}
			}
			if !found {
				DEBUG("  specified az %s is not in instance_groups azs %v\n", az, azs)
				return nil, ansi.Errorf("@R{could not find AZ} @c{%s} @R{in instance_groups AZS} @c{%v}", az, azs)
			}

			var ok bool
			pool, ok = pools[az]
			if !ok {
				DEBUG("  could not find pool: %s\n", az)
				return nil, ansi.Errorf("@R{could not find AZ} @c{%s} @R{in IP pool}", az)
			}
		}

		if offset < 0 {
			DEBUG("  arg[%d]: '%d' is not a positive number\n", i, offset)
			return nil, fmt.Errorf("static_ips operator only accepts literal non-negative numbers for arguments")
		}

		DEBUG("  arg[%d]: asking for the %d%s IP from the static address pool", i, offset, ord(offset))
		if offset >= int64(len(pool)) {
			DEBUG("     [%d]: pool only has %d addresses; offset %d is out of bounds\n", i, len(pool), offset)
			return nil, ansi.Errorf("@R{request for} @c{static_ip(%d)} @R{in a pool of only} @c{%d (zero-indexed)} @R{static addresses}", offset, len(pool))
		}

		// check to see if the address is already claimed, and claim it
		// atomically so concurrent evaluations cannot both succeed for
		// the same IP (see staticIPClaimMu / claimStaticIP)
		ip := pool[offset]
		DEBUG("     [%d]: checking to see if %s is already claimed", i, ip)

		engine := graft.GetEngine(ev)
		if thief, claimErr := claimStaticIP(engine.GetOperatorState(), ip, current); claimErr != nil {
			DEBUG("     [%d]: %s is in use by %s\n", i, ip, thief)
			return nil, claimErr
		}

		DEBUG("     [%d]: claimed %s for job %s", i, ip, current)
		ips = append(ips, ip)

		DEBUG("")
	}

	return &Response{
		Type:  Replace,
		Value: ips,
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("static_ips", StaticIPOperator{})
}
