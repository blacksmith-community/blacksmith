package bosh

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// BoshCloudConfigName is the name Blacksmith gives the cloud config it writes.
// The director merges named cloud configs alongside its default one, so the
// broker's config adds to the director's and never replaces it.
const BoshCloudConfigName = "blacksmith"

// CloudConfigAction is what the boot cloud config write should do.
type CloudConfigAction int

const (
	// CloudConfigWrite means nothing overlaps, so the config is written.
	CloudConfigWrite CloudConfigAction = iota
	// CloudConfigSkipIdentical means another cloud config already holds the
	// same content, so no write is needed.
	CloudConfigSkipIdentical
	// CloudConfigRefuse means the content shares names with another cloud
	// config, or both define compilation, so a write could break the
	// director's deploys.
	CloudConfigRefuse
	// CloudConfigSkipUnreadable means the broker could not understand its
	// own content or one of the director's cloud configs, so it cannot show
	// that a write is safe.
	CloudConfigSkipUnreadable
)

// cloudConfigType is the director config type of a cloud config.
const cloudConfigType = "cloud"

// cloudConfigNamedKeys are the cloud config sections whose entries the
// director identifies by name, and so sees twice when two configs define the
// same name.
//
//nolint:gochecknoglobals // read-only table
var cloudConfigNamedKeys = []string{"azs", "vm_types", "disk_types", "networks", "vm_extensions"}

var errCloudConfigNotAMap = errors.New("is not a YAML map")

// compilationKey is the cloud config section the director refuses to merge
// from two configs.
const compilationKey = "compilation"

// CloudConfigOverlap is one thing the broker's content shares with another
// cloud config.
type CloudConfigOverlap struct {
	// Config is the name of the other cloud config. The default cloud
	// config has an empty name.
	Config string
	// Key is the cloud config section, such as vm_types.
	Key string
	// Name is the shared entry name. It is empty for compilation, which
	// has no name.
	Name string
}

// CloudConfigPlan is the decision PlanCloudConfigWrite makes.
type CloudConfigPlan struct {
	Action CloudConfigAction
	// Holder is the name of the cloud config that already holds the same
	// content, for CloudConfigSkipIdentical.
	Holder string
	// Overlaps lists every shared key and name, for CloudConfigRefuse.
	Overlaps []CloudConfigOverlap
	// Reason says what could not be understood, for
	// CloudConfigSkipUnreadable.
	Reason string
}

// IsEmptyCloudConfig parses content as YAML and reports whether it holds
// nothing, meaning it is blank, null, an empty map, or an empty list. It
// returns the YAML error when the content does not parse.
func IsEmptyCloudConfig(content string) (bool, error) {
	// YAML rejects a tab where it expects indentation, so content that is
	// only whitespace is empty before the parser sees it.
	if strings.TrimSpace(content) == "" {
		return true, nil
	}

	var value interface{}

	err := yaml.Unmarshal([]byte(content), &value)
	if err != nil {
		return false, fmt.Errorf("the cloud config is not valid YAML: %w", err)
	}

	switch typed := value.(type) {
	case nil:
		return true, nil
	case map[string]interface{}:
		return len(typed) == 0, nil
	case []interface{}:
		return len(typed) == 0, nil
	default:
		return false, nil
	}
}

// PlanCloudConfigWrite decides what the boot cloud config write should do,
// given the cloud configs the director holds and the broker's content. It
// does no I/O. Any config not named blacksmith counts as another config, the
// director's default one included. The safe answer on any doubt is to write
// nothing.
func PlanCloudConfigWrite(existing []CloudConfigEntry, content string) CloudConfigPlan {
	mine, err := parseCloudConfigMap(content)
	if err != nil {
		return CloudConfigPlan{
			Action: CloudConfigSkipUnreadable,
			Reason: "the broker's cloud config " + err.Error(),
		}
	}

	others := make([]parsedCloudConfig, 0, len(existing))

	for _, entry := range existing {
		if entry.Name == BoshCloudConfigName {
			continue
		}

		parsed, parseErr := parseCloudConfigMap(entry.Content)
		if parseErr != nil {
			return CloudConfigPlan{
				Action: CloudConfigSkipUnreadable,
				Reason: fmt.Sprintf("the director's cloud config %q %v", entry.Name, parseErr),
			}
		}

		others = append(others, parsedCloudConfig{name: entry.Name, value: parsed})
	}

	for _, other := range others {
		if reflect.DeepEqual(other.value, mine) {
			return CloudConfigPlan{Action: CloudConfigSkipIdentical, Holder: other.name}
		}
	}

	var overlaps []CloudConfigOverlap

	for _, other := range others {
		overlaps = append(overlaps, findOverlaps(other, mine)...)
	}

	if len(overlaps) > 0 {
		return CloudConfigPlan{Action: CloudConfigRefuse, Overlaps: overlaps}
	}

	return CloudConfigPlan{Action: CloudConfigWrite}
}

type parsedCloudConfig struct {
	name  string
	value map[string]interface{}
}

// parseCloudConfigMap parses content as a YAML map. Blank content is an empty
// map, and any other shape is an error, because a cloud config is a map.
func parseCloudConfigMap(content string) (map[string]interface{}, error) {
	if strings.TrimSpace(content) == "" {
		return map[string]interface{}{}, nil
	}

	var value interface{}

	err := yaml.Unmarshal([]byte(content), &value)
	if err != nil {
		return nil, fmt.Errorf("is not valid YAML: %w", err)
	}

	switch typed := value.(type) {
	case nil:
		return map[string]interface{}{}, nil
	case map[string]interface{}:
		return typed, nil
	default:
		return nil, fmt.Errorf("%w (it is %T)", errCloudConfigNotAMap, value)
	}
}

func findOverlaps(other parsedCloudConfig, mine map[string]interface{}) []CloudConfigOverlap {
	var overlaps []CloudConfigOverlap

	for _, key := range cloudConfigNamedKeys {
		theirs := entryNames(other.value[key])
		if len(theirs) == 0 {
			continue
		}

		var shared []string

		for name := range entryNames(mine[key]) {
			if theirs[name] {
				shared = append(shared, name)
			}
		}

		sort.Strings(shared)

		for _, name := range shared {
			overlaps = append(overlaps, CloudConfigOverlap{Config: other.name, Key: key, Name: name})
		}
	}

	_, theirsHas := other.value[compilationKey]
	_, mineHas := mine[compilationKey]

	if theirsHas && mineHas {
		overlaps = append(overlaps, CloudConfigOverlap{Config: other.name, Key: compilationKey})
	}

	return overlaps
}

// entryNames returns the set of names in a list of named entries.
func entryNames(value interface{}) map[string]bool {
	list, isList := value.([]interface{})
	if !isList {
		return nil
	}

	names := make(map[string]bool, len(list))

	for _, item := range list {
		entry, isMap := item.(map[string]interface{})
		if !isMap {
			continue
		}

		if name, hasName := entry["name"]; hasName && name != nil {
			names[fmt.Sprint(name)] = true
		}
	}

	return names
}
