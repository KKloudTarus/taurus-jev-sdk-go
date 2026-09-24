package jev

import "encoding/json"

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

// ChoiceAnswer is the selected label with the distribution behind it.
type ChoiceAnswer struct {
	// Choice is the label with the highest probability.
	Choice string `json:"choice"`
	// Confidence in the selection, from 0 to 1.
	Confidence float64 `json:"confidence"`
	// Probabilities per label, summing to approximately 1.
	Probabilities map[string]float64 `json:"probabilities"`
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

// Answer holds exactly one of Noul, Choice or Score, selected by Type.
//
// An answer whose type this version does not model leaves all three nil and
// keeps Raw, so a primitive added to the API later does not fail the response
// your service is already handling.
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
}

// Known reports whether this version models the answer's type.
func (a Answer) Known() bool {
	return a.Noul != nil || a.Choice != nil || a.Score != nil
}

func (a *Answer) UnmarshalJSON(data []byte) error {
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return err
	}
	a.Type = discriminator.Type
	a.Raw = append(json.RawMessage(nil), data...)
	switch discriminator.Type {
	case AnswerNoul:
		a.Noul = &NoulAnswer{}
		return json.Unmarshal(data, a.Noul)
	case AnswerChoice:
		a.Choice = &ChoiceAnswer{}
		return json.Unmarshal(data, a.Choice)
	case AnswerScore:
		a.Score = &ScoreAnswer{}
		return json.Unmarshal(data, a.Score)
	default:
		return nil
	}
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

// Model describes one model available to the account.
type Model struct {
	// Name or alias accepted by a request's model field.
	Name string `json:"name"`
	// Description of the model and its capabilities.
	Description string `json:"description"`
	// ReleaseDate formatted as YYYY-MM-DD.
	ReleaseDate string `json:"release_date"`
}
