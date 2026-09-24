package jev

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// Answer types the API defines today. An answer carrying any other type is kept
// raw rather than rejected.
const (
	AnswerNoul   = "noul"
	AnswerChoice = "choice"
	AnswerScore  = "score"
)

// NoulAnswer is the probability that a yes/no statement is true.
type NoulAnswer struct {
	// Noul runs from 0 to 1. Values near 0.5 mean the model is undecided.
	Noul float64 `json:"noul"`
}

// UnmarshalJSON rejects a payload missing the required field. Decoding into a
// plain float would turn an absent probability into 0, which reads as a
// maximally confident no.
func (a *NoulAnswer) UnmarshalJSON(data []byte) error {
	var wire struct {
		Noul *float64 `json:"noul"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Noul == nil {
		return missingField("noul")
	}
	a.Noul = *wire.Noul
	return nil
}

// ChoiceAnswer is the selected label with the distribution behind it.
type ChoiceAnswer struct {
	// Choice is the label with the highest probability.
	Choice string `json:"choice"`
	// Confidence in the selection, from 0 to 1.
	Confidence float64 `json:"confidence"`
	// Probabilities per label, summing to approximately 1.
	Probabilities map[string]float64 `json:"probabilities"`
}

func (a *ChoiceAnswer) UnmarshalJSON(data []byte) error {
	var wire struct {
		Choice        *string            `json:"choice"`
		Confidence    *float64           `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	switch {
	case wire.Choice == nil:
		return missingField("choice")
	case wire.Confidence == nil:
		return missingField("confidence")
	case wire.Probabilities == nil:
		return missingField("probabilities")
	}
	a.Choice, a.Confidence, a.Probabilities = *wire.Choice, *wire.Confidence, wire.Probabilities
	return nil
}

// ScoreAnswer is the expected score with the rubric it was scored against.
//
// Legend and Probabilities are keyed by integer level. The wire format sends
// those keys as JSON strings; encoding/json converts them back.
type ScoreAnswer struct {
	// Score is the probability-weighted average level, so it may fall between
	// two integer levels.
	Score float64 `json:"score"`
	// Confidence in the score, from 0 to 1.
	Confidence float64 `json:"confidence"`
	// Legend maps each level to the criterion that defined it.
	Legend map[int]any `json:"legend"`
	// Probabilities per level, summing to approximately 1.
	Probabilities map[int]float64 `json:"probabilities"`
}

func (a *ScoreAnswer) UnmarshalJSON(data []byte) error {
	var wire struct {
		Score         *float64        `json:"score"`
		Confidence    *float64        `json:"confidence"`
		Legend        map[int]any     `json:"legend"`
		Probabilities map[int]float64 `json:"probabilities"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	switch {
	case wire.Score == nil:
		return missingField("score")
	case wire.Confidence == nil:
		return missingField("confidence")
	case wire.Legend == nil:
		return missingField("legend")
	case wire.Probabilities == nil:
		return missingField("probabilities")
	}
	a.Score, a.Confidence = *wire.Score, *wire.Confidence
	a.Legend, a.Probabilities = wire.Legend, wire.Probabilities
	return nil
}

// Answer holds exactly one of Noul, Choice or Score, selected by Type.
//
// An answer whose type this version does not model leaves all three nil and
// keeps Raw, so a primitive added to the API later does not fail the response
// your service is already handling. A malformed answer is rejected instead:
// [SystemOneResponse] reports it as a [ResponseValidationError] naming the
// field, so a broken payload is never mistaken for a future primitive.
type Answer struct {
	// Type is the wire discriminator.
	Type string
	// Noul is set when Type is [AnswerNoul].
	Noul *NoulAnswer
	// Choice is set when Type is [AnswerChoice].
	Choice *ChoiceAnswer
	// Score is set when Type is [AnswerScore].
	Score *ScoreAnswer
	// Raw is the answer object exactly as the server sent it.
	Raw json.RawMessage

	// notObject records a payload that was not a JSON object, and missing
	// records the first required field it omitted. The enclosing response turns
	// either into a ResponseValidationError once the question name is known.
	notObject bool
	missing   string
}

// Known reports whether this version models the answer's type.
func (a Answer) Known() bool {
	return a.Noul != nil || a.Choice != nil || a.Score != nil
}

// MarshalJSON writes the answer exactly as the server sent it, so a decoded
// response round-trips through encoding/json.
func (a Answer) MarshalJSON() ([]byte, error) {
	if len(a.Raw) == 0 {
		return []byte("null"), nil
	}
	return a.Raw, nil
}

func (a *Answer) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		a.notObject = true
		a.Raw = bytes.Clone(trimmed)
		return nil
	}
	var discriminator struct {
		Type *string `json:"type"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return err
	}
	a.Raw = bytes.Clone(data)
	if discriminator.Type == nil {
		a.missing = "type"
		return nil
	}
	a.Type = *discriminator.Type

	var err error
	switch a.Type {
	case AnswerNoul:
		a.Noul = &NoulAnswer{}
		err = json.Unmarshal(data, a.Noul)
	case AnswerChoice:
		a.Choice = &ChoiceAnswer{}
		err = json.Unmarshal(data, a.Choice)
	case AnswerScore:
		a.Score = &ScoreAnswer{}
		err = json.Unmarshal(data, a.Score)
	default:
		// Forward compatibility: an unmodelled type keeps its bytes in Raw.
		return nil
	}
	var field *fieldError
	if asFieldError(err, &field) {
		a.Noul, a.Choice, a.Score = nil, nil, nil
		a.missing = field.path
		return nil
	}
	return err
}

// Usage reports the tokens billed for one evaluation. Output tokens are
// currently free of charge.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// SystemOneResponse holds one answer per question, keyed by the names supplied
// in the request.
type SystemOneResponse struct {
	// Model that answered, which may differ from the alias that was requested.
	Model string `json:"model"`
	// Answers keyed by question name.
	Answers map[string]Answer `json:"answers"`
	// Usage for this evaluation.
	Usage Usage `json:"usage"`

	// RequestID is the x-typesafe-request-id header. Quote it in a support
	// report.
	RequestID string `json:"-"`
	// Status is the HTTP status code the response arrived with.
	Status int `json:"-"`
	// Header is the response header, for values this version does not model.
	Header http.Header `json:"-"`
	// RawBody is the response body exactly as it arrived.
	RawBody json.RawMessage `json:"-"`
}

// UnmarshalJSON enforces the fields the API documents as required, so a
// truncated or degraded body is reported rather than decoded into zero values.
func (r *SystemOneResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Model   *string           `json:"model"`
		Answers map[string]Answer `json:"answers"`
		Usage   *Usage            `json:"usage"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	switch {
	case wire.Model == nil:
		return missingField("model")
	case wire.Answers == nil:
		return missingField("answers")
	case len(wire.Answers) == 0:
		return missingField("answers")
	case wire.Usage == nil:
		return missingField("usage")
	}
	for name, answer := range wire.Answers {
		if answer.notObject {
			return missingField("answers." + name)
		}
		if answer.missing != "" {
			return missingField("answers." + name + "." + answer.missing)
		}
	}
	r.Model, r.Answers, r.Usage = *wire.Model, wire.Answers, *wire.Usage
	return nil
}

// NoulOf returns the probability for a yes/no question. The second result is
// false when the name is absent or answered by another primitive.
func (r *SystemOneResponse) NoulOf(name string) (float64, bool) {
	answer, ok := r.Answers[name]
	if !ok || answer.Noul == nil {
		return 0, false
	}
	return answer.Noul.Noul, true
}

// ChoiceOf returns the choice answer for a question. The second result is false
// when the name is absent or answered by another primitive.
func (r *SystemOneResponse) ChoiceOf(name string) (ChoiceAnswer, bool) {
	answer, ok := r.Answers[name]
	if !ok || answer.Choice == nil {
		return ChoiceAnswer{}, false
	}
	return *answer.Choice, true
}

// ScoreOf returns the score answer for a question. The second result is false
// when the name is absent or answered by another primitive.
func (r *SystemOneResponse) ScoreOf(name string) (ScoreAnswer, bool) {
	answer, ok := r.Answers[name]
	if !ok || answer.Score == nil {
		return ScoreAnswer{}, false
	}
	return *answer.Score, true
}

// Nouls returns every yes/no answer, keyed by question name.
func (r *SystemOneResponse) Nouls() map[string]NoulAnswer {
	result := map[string]NoulAnswer{}
	for name, answer := range r.Answers {
		if answer.Noul != nil {
			result[name] = *answer.Noul
		}
	}
	return result
}

// Choices returns every choice answer, keyed by question name.
func (r *SystemOneResponse) Choices() map[string]ChoiceAnswer {
	result := map[string]ChoiceAnswer{}
	for name, answer := range r.Answers {
		if answer.Choice != nil {
			result[name] = *answer.Choice
		}
	}
	return result
}

// Scores returns every score answer, keyed by question name.
func (r *SystemOneResponse) Scores() map[string]ScoreAnswer {
	result := map[string]ScoreAnswer{}
	for name, answer := range r.Answers {
		if answer.Score != nil {
			result[name] = *answer.Score
		}
	}
	return result
}

// Unknown returns every answer whose type this version does not model, keyed by
// question name. Their payloads are available through Answer.Raw.
func (r *SystemOneResponse) Unknown() map[string]Answer {
	result := map[string]Answer{}
	for name, answer := range r.Answers {
		if !answer.Known() {
			result[name] = answer
		}
	}
	return result
}

// Model describes one model available to the account.
type Model struct {
	// Name or alias accepted by a request's model field.
	Name string `json:"name"`
	// Description of the model and its capabilities.
	Description string `json:"description"`
	// ReleaseDate formatted as YYYY-MM-DD.
	ReleaseDate string `json:"release_date"`
}

// modelList enforces the one field the models endpoint documents as required,
// so a null or empty body is not read as "the account has no models".
type modelList struct {
	Models []Model `json:"models"`
}

func (l *modelList) UnmarshalJSON(data []byte) error {
	var wire struct {
		Models *[]Model `json:"models"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Models == nil {
		return missingField("models")
	}
	l.Models = *wire.Models
	return nil
}
