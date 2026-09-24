package jev

import (
	"encoding/json"
	"testing"
)

const answersPayload = `{
  "model": "jev-latest",
  "answers": {
    "billing": {"type": "noul", "noul": 0.98},
    "tone": {"type": "choice", "choice": "angry", "confidence": 0.9,
             "probabilities": {"angry": 0.8, "calm": 0.2}},
    "urgency": {"type": "score", "score": 1.7, "confidence": 0.9,
                "legend": {"0": "Can wait", "1": "This week", "2": "Today"},
                "probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}},
    "future": {"type": "rank", "rank": 3}
  },
  "usage": {"input_tokens": 120, "output_tokens": 12}
}`

func decodeAnswers(t *testing.T) *SystemOneResponse {
	t.Helper()
	response := &SystemOneResponse{}
	if err := json.Unmarshal([]byte(answersPayload), response); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return response
}

func TestAnswerAccessors(t *testing.T) {
	response := decodeAnswers(t)
	if noul, ok := response.NoulOf("billing"); !ok || noul != 0.98 {
		t.Errorf("NoulOf = %v, %v", noul, ok)
	}
	choice, ok := response.ChoiceOf("tone")
	if !ok || choice.Choice != "angry" || choice.Confidence != 0.9 || choice.Probabilities["calm"] != 0.2 {
		t.Errorf("ChoiceOf = %+v, %v", choice, ok)
	}
	score, ok := response.ScoreOf("urgency")
	if !ok || score.Score != 1.7 {
		t.Errorf("ScoreOf = %+v, %v", score, ok)
	}
	// JSON object keys are strings; the score maps come back keyed by level.
	if score.Legend[2] != "Today" || score.Probabilities[2] != 0.8 {
		t.Errorf("score maps = %v / %v", score.Legend, score.Probabilities)
	}
	if response.Usage.InputTokens != 120 || response.Usage.OutputTokens != 12 {
		t.Errorf("usage = %+v", response.Usage)
	}
}

func TestAccessorsRejectMismatchedType(t *testing.T) {
	response := decodeAnswers(t)
	if _, ok := response.NoulOf("tone"); ok {
		t.Error("a choice answer was read as a noul")
	}
	if _, ok := response.ChoiceOf("missing"); ok {
		t.Error("an absent name returned a choice")
	}
	if _, ok := response.ScoreOf("billing"); ok {
		t.Error("a noul answer was read as a score")
	}
}

func TestUnknownAnswerTypeSurvives(t *testing.T) {
	response := decodeAnswers(t)
	future := response.Answers["future"]
	if future.Type != "rank" {
		t.Errorf("type = %q", future.Type)
	}
	if future.Known() {
		t.Error("an unmodelled type reported itself as known")
	}
	if future.Noul != nil || future.Choice != nil || future.Score != nil {
		t.Error("an unmodelled type populated a typed field")
	}
	var raw map[string]any
	if err := json.Unmarshal(future.Raw, &raw); err != nil || raw["rank"] != float64(3) {
		t.Errorf("raw payload lost: %v, %v", raw, err)
	}
	// The known answers in the same response decode normally.
	if _, ok := response.NoulOf("billing"); !ok {
		t.Error("an unmodelled answer broke the rest of the response")
	}
}

func TestAnswerRejectsNonObject(t *testing.T) {
	var answer Answer
	if err := json.Unmarshal([]byte(`"not an object"`), &answer); err == nil {
		t.Error("a scalar answer was accepted")
	}
}
