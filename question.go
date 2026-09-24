package jev

import (
	"encoding/json"
	"fmt"
)

// Questions maps a name you choose to the question asked under it. The same
// names key the answers in the response.
type Questions map[string]Question

// Question is one of [Noul], [Choice] or [Score]. The interface is closed: the
// API defines these three primitives and a client cannot invent a fourth.
type Question interface {
	json.Marshaler
	// questionType returns the wire discriminator.
	questionType() string
	// validate rejects a question the API would reject, before it is sent.
	validate(name string) error
}

// NoulCriteria describes what counts as a yes and what counts as a no. Either
// field may be a string, a map or a slice. Leave a field nil to send nothing.
type NoulCriteria struct {
	True  any `json:"true,omitempty"`
	False any `json:"false,omitempty"`
}

// Noul asks a yes/no question. The answer is the probability that the statement
// is true, from 0 to 1.
//
// See https://docs.typesafe.ai/primitives/noul.
type Noul struct {
	// Instructions is the question or statement to evaluate, as a string, a map
	// or a slice. Optional.
	Instructions any
	// Criteria clarifies the two outcomes. Optional.
	Criteria *NoulCriteria
}

func (Noul) questionType() string  { return "noul" }
func (Noul) validate(string) error { return nil }

// MarshalJSON builds the wire object by hand so an unset field is absent rather
// than null; the API reads an explicit null as a supplied value.
func (q Noul) MarshalJSON() ([]byte, error) {
	body := map[string]any{"type": q.questionType()}
	if q.Instructions != nil {
		body["instructions"] = q.Instructions
	}
	if q.Criteria != nil {
		body["criteria"] = q.Criteria
	}
	return json.Marshal(body)
}

// maxChoices is the API's cardinality limit for a choice question.
const maxChoices = 255

// Choice selects one label. Criteria maps each label to a description, or to
// nil for a label interpreted by its name alone.
//
// See https://docs.typesafe.ai/primitives/choice.
type Choice struct {
	// Instructions is what the model should decide. Optional.
	Instructions any
	// Criteria holds the labels to choose between. Required, at most 255.
	Criteria map[string]any
}

func (Choice) questionType() string { return "choice" }

func (q Choice) validate(name string) error {
	if len(q.Criteria) == 0 {
		return fmt.Errorf("%w: choice question %q has no criteria", ErrInvalidRequest, name)
	}
	if len(q.Criteria) > maxChoices {
		return fmt.Errorf("%w: choice question %q has %d criteria, the limit is %d",
			ErrInvalidRequest, name, len(q.Criteria), maxChoices)
	}
	return nil
}

func (q Choice) MarshalJSON() ([]byte, error) {
	body := map[string]any{"type": q.questionType(), "criteria": q.Criteria}
	if q.Instructions != nil {
		body["instructions"] = q.Instructions
	}
	return json.Marshal(body)
}

// Score rates the state against ordered levels. A level's position is its
// score, counting from zero.
//
// See https://docs.typesafe.ai/primitives/score.
type Score struct {
	// Instructions is what the model should rate. Optional.
	Instructions any
	// Criteria holds the ordered level descriptions. Required, at least one.
	Criteria []any
}

func (Score) questionType() string { return "score" }

func (q Score) validate(name string) error {
	if len(q.Criteria) == 0 {
		return fmt.Errorf("%w: score question %q has no criteria", ErrInvalidRequest, name)
	}
	return nil
}

func (q Score) MarshalJSON() ([]byte, error) {
	body := map[string]any{"type": q.questionType(), "criteria": q.Criteria}
	if q.Instructions != nil {
		body["instructions"] = q.Instructions
	}
	return json.Marshal(body)
}

func (q Questions) validate() error {
	if len(q) == 0 {
		return fmt.Errorf("%w: at least one question is required", ErrInvalidRequest)
	}
	for name, question := range q {
		if question == nil {
			return fmt.Errorf("%w: question %q is nil", ErrInvalidRequest, name)
		}
		if err := question.validate(name); err != nil {
			return err
		}
	}
	return nil
}
