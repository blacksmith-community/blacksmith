package certinventory

import "time"

const day = 24 * time.Hour

// ExpiryOf classifies a certificate's remaining time against the thresholds.
// The comparison uses durations, so 6 days and 23 hours left is critical
// under a 7-day threshold. Days left is the remaining time in whole days,
// rounded down, and is negative for an expired certificate.
func ExpiryOf(notAfter *time.Time, now time.Time, warningDays, criticalDays int) (ExpiryStatus, *int) {
	if notAfter == nil {
		return ExpiryUnknown, nil
	}

	remaining := notAfter.Sub(now)
	days := int(floorDiv(remaining, day))

	switch {
	case remaining <= 0:
		return ExpiryExpired, &days
	case remaining < time.Duration(criticalDays)*day:
		return ExpiryCritical, &days
	case remaining < time.Duration(warningDays)*day:
		return ExpiryWarning, &days
	default:
		return ExpiryOK, &days
	}
}

func floorDiv(d, unit time.Duration) int64 {
	q := int64(d / unit)
	if d%unit < 0 {
		q--
	}

	return q
}

// HealthOf derives the health axis from findings and coverage.
func HealthOf(findings []Finding, cov Coverage) Health {
	hasWarning := false

	for _, f := range findings {
		switch f.Severity {
		case SeverityError:
			return HealthError
		case SeverityWarning:
			hasWarning = true
		}
	}

	if hasWarning {
		return HealthWarning
	}

	if !cov.Complete {
		return HealthUnknown
	}

	for _, state := range cov.SourceStates {
		if state == "failed" {
			return HealthUnknown
		}
	}

	return HealthOK
}

// NeedsAttention reports whether the navigation badge should count c.
func NeedsAttention(c *Certificate) bool {
	if c.Expiry == ExpiryExpired || c.Expiry == ExpiryCritical || c.Health == HealthError {
		return true
	}

	return c.Divergence != nil &&
		(c.Divergence.State == DivergenceUnconverged || c.Divergence.State == DivergenceReverted)
}

// Summarize counts certificates by status. It uses the Expiry, Health, and
// Divergence values already set on each certificate.
func Summarize(certs []Certificate) Summary {
	s := Summary{Certificates: len(certs)}
	instances := map[string]struct{}{}

	for i := range certs {
		c := &certs[i]

		switch c.Expiry {
		case ExpiryExpired:
			s.Expired++
		case ExpiryCritical:
			s.Critical++
		case ExpiryWarning:
			s.Warning++
		case ExpiryOK:
			s.ExpiryOK++
		default:
			s.ExpiryUnknown++
		}

		if c.Health == HealthError {
			s.HealthErrors++
		}

		if NeedsAttention(c) {
			s.NeedsAttention++
		}

		if c.Divergence != nil && c.Divergence.State == DivergenceUnconverged {
			s.Unconverged++
		}

		if c.Coverage.Stale {
			s.Stale++
		}

		if !c.Coverage.Complete {
			s.IncompleteCoverage++
		}

		for _, o := range c.Owners {
			if o.InstanceID != "" {
				instances[o.InstanceID] = struct{}{}
			}
		}
	}

	s.AffectedInstances = len(instances)

	return s
}

// EffectiveNotAfter follows the effective-expiry rule. A CredHub certificate
// starts from its deployed version. Probe observations can move that earlier,
// and move it later only when coverage is complete and every probe agrees on
// the same later date. Any other certificate uses its earliest observation.
func EffectiveNotAfter(c *Certificate) *time.Time {
	if c.Versions != nil && c.Versions.DeployedNotAfter != nil {
		eff := *c.Versions.DeployedNotAfter

		var probes []time.Time

		for _, o := range c.Observations {
			if o.Source == SourceProbe && o.NotAfter != nil {
				probes = append(probes, *o.NotAfter)
			}
		}

		for _, p := range probes {
			if p.Before(eff) {
				eff = p
			}
		}

		if c.Coverage.Complete && len(probes) > 0 {
			same := true

			for _, p := range probes {
				if !p.Equal(probes[0]) {
					same = false

					break
				}
			}

			if same && probes[0].After(eff) {
				eff = probes[0]
			}
		}

		return &eff
	}

	var earliest *time.Time

	for _, o := range c.Observations {
		if o.NotAfter == nil {
			continue
		}

		if earliest == nil || o.NotAfter.Before(*earliest) {
			t := *o.NotAfter
			earliest = &t
		}
	}

	return earliest
}
