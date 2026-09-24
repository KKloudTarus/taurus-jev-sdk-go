package jev

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func marshalQuestion(t *testing.T, question Question) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(question)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return decoded
}

func TestQuestionWireShapes(t *testing.T) {
	noul := marshalQuestion(t, Noul{Instructions: "Is this spam?"})
	if !reflect.DeepEqual(noul, map[string]any{"type": "noul", "instructions": "Is this spam?"}) {
		t.Errorf("noul = %v", noul)
	}

	full := marshalQuestion(t, Noul{Criteria: &NoulCriteria{True: "Unsolicited advertising"}})
	criteria, ok := full["criteria"].(map[string]any)
	if !ok || criteria["true"] != "Unsolicited advertising" {
		t.Errorf("noul criteria = %v", full["criteria"])
	}
	// The unset half of the criteria stays off the wire.
	if _, present := criteria["false"]; present {
		t.Error("unset criteria field was serialized")
	}

	choice := marshalQuestion(t, Choice{Criteria: map[string]any{"angry": nil, "calm": "polite"}})
	if choice["type"] != "choice" {
		t.Errorf("choice type = %v", choice["type"])
	}
	// A label with no description is sent as an explicit null, which the API
	// reads as "interpret this label by its name".
	labels := choice["criteria"].(map[string]any)
	value, present := labels["angry"]
	if !present || value != nil {
		t.Errorf("nil label = %v, present = %v", value, present)
	}

	score := marshalQuestion(t, Score{Criteria: []any{"Can wait", "Today"}})
	if score["type"] != "score" || len(score["criteria"].([]any)) != 2 {
		t.Errorf("score = %v", score)
	}
}

func TestUnsetInstructionsAreAbsent(t *testing.T) {
	for _, question := range []Question{
		Noul{},
		Choice{Criteria: map[string]any{"a": nil}},
		Score{Criteria: []any{"a"}},
	} {
		if _, present := marshalQuestion(t, question)["instructions"]; present {
			t.Errorf("%T serialized unset instructions", question)
		}
	}
}

func TestStructuredInstructions(t *testing.T) {
	// Instructions accept a map or a slice, not only a string.
	question := marshalQuestion(t, Noul{Instructions: map[string]any{"task": "Identify advertising"}})
	nested, ok := question["instructions"].(map[string]any)
	if !ok || nested["task"] != "Identify advertising" {
		t.Errorf("instructions = %v", question["instructions"])
	}
}

func TestQuestionsValidate(t *testing.T) {
	cases := []struct {
		name      string
		questions Questions
		wantError bool
	}{
		{"empty", Questions{}, true},
		{"nil map", nil, true},
		{"nil question", Questions{"q": nil}, true},
		{"choice without criteria", Questions{"q": Choice{}}, true},
		{"score without criteria", Questions{"q": Score{}}, true},
		{"bare noul is valid", Questions{"q": Noul{}}, false},
		{"valid mix", Questions{
			"a": Noul{Instructions: "?"},
			"b": Choice{Criteria: map[string]any{"x": nil}},
			"c": Score{Criteria: []any{"low", "high"}},
		}, false},
	}
	for _, testCase := range cases {
		err := testCase.questions.validate()
		if testCase.wantError && err == nil {
			t.Errorf("%s: expected an error", testCase.name)
		}
		if !testCase.wantError && err != nil {
			t.Errorf("%s: unexpected error %v", testCase.name, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: error %v is not ErrInvalidRequest", testCase.name, err)
		}
	}
}

func TestLargeChoiceCriteriaAreSent(t *testing.T) {
	// openapi.json sets no maxProperties on choice criteria, so the client must
	// not impose one of its own. A server-side limit is the server's to report.
	criteria := make(map[string]any, 300)
	for i := 0; i < 300; i++ {
		criteria[fmt.Sprintf("label%d", i)] = nil
	}
	question := Choice{Criteria: criteria}
	if err := question.validate("tone"); err != nil {
		t.Fatalf("validate rejected %d labels: %v", len(criteria), err)
	}
	if got := marshalQuestion(t, question)["criteria"].(map[string]any); len(got) != 300 {
		t.Errorf("serialized %d labels, want 300", len(got))
	}
}

func TestRawQuestion(t *testing.T) {
	question := RawQuestion{Type: "rank", Fields: map[string]any{"instructions": "?", "depth": 3}}
	if err := question.validate("q"); err != nil {
		t.Fatalf("validate: %v", err)
	}
	encoded := marshalQuestion(t, question)
	if encoded["type"] != "rank" || encoded["instructions"] != "?" || encoded["depth"] != float64(3) {
		t.Errorf("encoded = %v", encoded)
	}
	// Type wins over a colliding key, so the discriminator cannot be spoofed.
	collide := marshalQuestion(t, RawQuestion{Type: "rank", Fields: map[string]any{"type": "noul"}})
	if collide["type"] != "rank" {
		t.Errorf("type = %v, want rank", collide["type"])
	}
	if err := (RawQuestion{}).validate("q"); err == nil {
		t.Error("a raw question with no type was accepted")
	}
}
