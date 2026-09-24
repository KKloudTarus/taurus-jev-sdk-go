package jev

import (
	"encoding/json"
	"fmt"
)

// Questions maps a name you choose to the question asked under it. The same
// names key the answers in the response.
type Questions map[string]Question

// Question is one of [Noul], [Choice], [Score] or [RawQuestion].
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

// Choice selects one label. Criteria maps each label to a description, or to
// nil for a label interpreted by its name alone.
//
// The number of labels is bounded by the API, not by this client, so a limit
// raised server side needs no SDK upgrade.
//
// See https://docs.typesafe.ai/primitives/choice.
type Choice struct {
	// Instructions is what the model should decide. Optional.
	Instructions any
	// Criteria holds the labels to choose between. Required.
	Criteria map[string]any
}

func (Choice) questionType() string { return "choice" }

func (q Choice) validate(name string) error {
	if len(q.Criteria) == 0 {
		return fmt.Errorf("%w: choice question %q has no criteria", ErrInvalidRequest, name)
	}
	return nil
}

// MarshalJSON builds the wire object by hand so an unset field is absent rather
// than null, while a nil description inside Criteria stays an explicit null.
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

// MarshalJSON builds the wire object by hand so an unset field is absent rather
// than null.
func (q Score) MarshalJSON() ([]byte, error) {
	body := map[string]any{"type": q.questionType(), "criteria": q.Criteria}
	if q.Instructions != nil {
		body["instructions"] = q.Instructions
	}
	return json.Marshal(body)
}

// RawQuestion sends a question shape this version does not model, such as a
// primitive added to the API after this release. Fields are written verbatim
// alongside the type discriminator.
//
// Its answer arrives with Known() false and its payload in Answer.Raw.
type RawQuestion struct {
	// Type is the wire discriminator. Required.
	Type string
	// Fields are the remaining members of the question object. A "type" key
	// here is ignored in favor of Type.
	Fields map[string]any
}

func (q RawQuestion) questionType() string { return q.Type }

func (q RawQuestion) validate(name string) error {
	if q.Type == "" {
		return fmt.Errorf("%w: raw question %q has no type", ErrInvalidRequest, name)
	}
	return nil
}

// MarshalJSON writes Fields verbatim with Type as the discriminator. A "type"
// key in Fields is ignored, so the discriminator cannot be overwritten.
func (q RawQuestion) MarshalJSON() ([]byte, error) {
	body := make(map[string]any, len(q.Fields)+1)
	for key, value := range q.Fields {
		body[key] = value
	}
	body["type"] = q.Type
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
