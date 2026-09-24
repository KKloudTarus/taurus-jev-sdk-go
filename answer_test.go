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

func TestResponseRejectsNonObjectAnswer(t *testing.T) {
	// A null or scalar answer is a broken payload. It must not be mistaken for
	// a forward-compatible answer type, which also reports Known() == false.
	for _, payload := range []string{
		`{"model":"m","usage":{"input_tokens":1,"output_tokens":1},"answers":{"q":null}}`,
		`{"model":"m","usage":{"input_tokens":1,"output_tokens":1},"answers":{"q":"scalar"}}`,
	} {
		var response SystemOneResponse
		err := json.Unmarshal([]byte(payload), &response)
		if err == nil {
			t.Errorf("%s: accepted", payload)
			continue
		}
		var field *fieldError
		if !asFieldError(err, &field) || field.path != "answers.q" {
			t.Errorf("%s: err = %v, want a field error at answers.q", payload, err)
		}
	}
}

func TestResponseRejectsMissingRequiredFields(t *testing.T) {
	usage := `"usage":{"input_tokens":1,"output_tokens":1}`
	cases := []struct {
		payload string
		want    string
	}{
		// A missing noul decodes to 0.0 in a plain float, which reads as a
		// maximally confident no. It has to be an error instead.
		{`{"model":"m",` + usage + `,"answers":{"spam":{"type":"noul"}}}`, "answers.spam.noul"},
		{`{"model":"m",` + usage + `,"answers":{"t":{"type":"choice","confidence":0.9,"probabilities":{}}}}`, "answers.t.choice"},
		{`{"model":"m",` + usage + `,"answers":{"t":{"type":"choice","choice":"a","probabilities":{}}}}`, "answers.t.confidence"},
		{`{"model":"m",` + usage + `,"answers":{"t":{"type":"choice","choice":"a","confidence":0.9}}}`, "answers.t.probabilities"},
		{`{"model":"m",` + usage + `,"answers":{"u":{"type":"score","confidence":0.9,"legend":{},"probabilities":{}}}}`, "answers.u.score"},
		{`{"model":"m",` + usage + `,"answers":{"u":{"type":"score","score":1,"confidence":0.9,"probabilities":{}}}}`, "answers.u.legend"},
		{`{"model":"m",` + usage + `,"answers":{"q":{"noul":0.5}}}`, "answers.q.type"},
		{`{` + usage + `,"answers":{"q":{"type":"noul","noul":0.5}}}`, "model"},
		{`{"model":"m",` + usage + `}`, "answers"},
		{`{"model":"m",` + usage + `,"answers":{}}`, "answers"},
		{`{"model":"m","answers":{"q":{"type":"noul","noul":0.5}}}`, "usage"},
	}
	for _, testCase := range cases {
		var response SystemOneResponse
		err := json.Unmarshal([]byte(testCase.payload), &response)
		var field *fieldError
		if !asFieldError(err, &field) {
			t.Errorf("%s: err = %v, want a field error", testCase.payload, err)
			continue
		}
		if field.path != testCase.want {
			t.Errorf("%s: path = %q, want %q", testCase.payload, field.path, testCase.want)
		}
	}
}

func TestResponseRoundTripsThroughJSON(t *testing.T) {
	first := decodeAnswers(t)
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var second SystemOneResponse
	if err := json.Unmarshal(encoded, &second); err != nil {
		t.Fatalf("re-decoding a marshalled response failed: %v\n%s", err, encoded)
	}
	if noul, ok := second.NoulOf("billing"); !ok || noul != 0.98 {
		t.Errorf("NoulOf after round trip = %v, %v", noul, ok)
	}
	if choice, ok := second.ChoiceOf("tone"); !ok || choice.Choice != "angry" {
		t.Errorf("ChoiceOf after round trip = %+v, %v", choice, ok)
	}
	if second.Answers["future"].Type != "rank" {
		t.Error("the unmodelled answer did not survive the round trip")
	}
}

func TestGroupedAccessors(t *testing.T) {
	response := decodeAnswers(t)
	if len(response.Nouls()) != 1 || response.Nouls()["billing"].Noul != 0.98 {
		t.Errorf("Nouls = %+v", response.Nouls())
	}
	if len(response.Choices()) != 1 || response.Choices()["tone"].Choice != "angry" {
		t.Errorf("Choices = %+v", response.Choices())
	}
	if len(response.Scores()) != 1 || response.Scores()["urgency"].Score != 1.7 {
		t.Errorf("Scores = %+v", response.Scores())
	}
	unknown := response.Unknown()
	if len(unknown) != 1 || unknown["future"].Type != "rank" {
		t.Errorf("Unknown = %+v", unknown)
	}
}
