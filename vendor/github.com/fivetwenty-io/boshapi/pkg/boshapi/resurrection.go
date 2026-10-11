package boshapi

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ResurrectionState represents the resurrection state. It encodes and decodes
// the document the director uses for GET /resurrection, so a state that is
// marshalled can be read back.
type ResurrectionState struct {
	Paused bool
}

// MarshalJSON encodes the state as {"resurrection": <bool>}, where true means
// the resurrector is running, which is the shape UnmarshalJSON reads.
func (s ResurrectionState) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(struct {
		Resurrection bool `json:"resurrection"`
	}{Resurrection: !s.Paused})
	if err != nil {
		return nil, fmt.Errorf("encoding resurrection state: %w", err)
	}

	return data, nil
}

// UnmarshalJSON decodes the director's answer to GET /resurrection, which is
// {"resurrection": <bool>} where true means the resurrector is running. It
// sets Paused to the opposite and returns an error when the answer has no
// resurrection field.
func (s *ResurrectionState) UnmarshalJSON(data []byte) error {
	var answer struct {
		Resurrection *bool `json:"resurrection"`
	}

	if err := json.Unmarshal(data, &answer); err != nil {
		return fmt.Errorf("decoding resurrection state: %w", err)
	}

	if answer.Resurrection == nil {
		return ErrResurrectionFieldMissing
	}

	s.Paused = !*answer.Resurrection

	return nil
}

// ErrResurrectionFieldMissing is returned when the director's answer to GET
// /resurrection has no resurrection field.
var ErrResurrectionFieldMissing = errors.New("the director's resurrection answer has no resurrection field")

// ResurrectionRequest represents a request to update resurrection state.
type ResurrectionRequest struct {
	ResurrectionPaused bool `json:"resurrection_paused"`
}
