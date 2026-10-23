package certinventory

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

var csvHeader = []string{
	"id", "key", "variable", "kind", "common_name", "issuer", "owner_kind", "owner",
	"service", "plan", "instance_id", "affected_instances", "effective_not_after",
	"days_left", "expiry", "health", "attention", "findings", "coverage",
}

// WriteCSV writes one row per certificate. A text cell that a spreadsheet
// would read as a formula gets a leading apostrophe, so a hostile common
// name cannot run in the viewer's spreadsheet. Numeric and boolean cells
// are written as they are.
func WriteCSV(w io.Writer, certs []Certificate) error {
	cw := csv.NewWriter(w)

	if err := cw.Write(csvHeader); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}

	for i := range certs {
		if err := cw.Write(csvRow(&certs[i])); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}

	cw.Flush()

	if err := cw.Error(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}

	return nil
}

// WriteJSON writes the inventory response as indented JSON.
func WriteJSON(w io.Writer, resp InventoryResponse) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	if err := enc.Encode(resp); err != nil {
		return fmt.Errorf("write json: %w", err)
	}

	return nil
}

// neutralise stops a spreadsheet from reading a text cell as a formula.
func neutralise(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}

	return s
}

func csvRow(c *Certificate) []string {
	var owner, ownerKind, service, plan string

	var instances []string

	for i := range c.Owners {
		o := &c.Owners[i]
		if i == 0 {
			ownerKind = string(o.Kind)
		}

		name := firstNonEmpty(o.InstanceName, o.Deployment, o.InstanceID)
		owner = joinUnique(owner, name)
		service = joinUnique(service, o.ServiceName)
		plan = joinUnique(plan, o.PlanName)

		if o.InstanceID != "" {
			instances = append(instances, o.InstanceID)
		}
	}

	var notAfter, daysLeft string
	if c.EffectiveNotAfter != nil {
		notAfter = c.EffectiveNotAfter.UTC().Format(time.RFC3339)
	}

	if c.DaysLeft != nil {
		daysLeft = strconv.Itoa(*c.DaysLeft)
	}

	codes := make([]string, 0, len(c.Findings))
	for _, f := range c.Findings {
		codes = append(codes, f.Code)
	}

	text := func(s string) string { return neutralise(s) }

	return []string{
		text(c.ID), text(c.Key), text(c.Variable), text(string(c.Kind)),
		text(c.CommonName), text(c.Issuer), text(ownerKind), text(owner),
		text(service), text(plan), text(strings.Join(instances, ";")),
		strconv.Itoa(c.AffectedInstances), notAfter, daysLeft,
		text(string(c.Expiry)), text(string(c.Health)), strconv.FormatBool(c.Attention),
		text(strings.Join(codes, ";")),
		fmt.Sprintf("%d/%d", c.Coverage.ObservedListeners, c.Coverage.ExpectedListeners),
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}

	return ""
}

// joinUnique appends v to a semicolon list unless the list already has it.
func joinUnique(list, v string) string {
	if v == "" {
		return list
	}

	if list == "" {
		return v
	}

	for _, have := range strings.Split(list, ";") {
		if have == v {
			return list
		}
	}

	return list + ";" + v
}
