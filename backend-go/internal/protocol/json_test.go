package protocol

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestStrictDecode(t *testing.T) {
	for _, s := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `[{"x":1,"\u0078":2}]`, `{} {}`, `{"x":"\ud800"}`, `{"x":"\udc00"}`, `{"x":"\ud800\u0000"}`, `{"x":NaN}`, `[1,]`, string([]byte{'"', 0xff, '"'}), strings.Repeat("[", 66) + strings.Repeat("]", 66)} {
		t.Run(s, func(t *testing.T) {
			if _, e := Decode([]byte(s), 65536); e == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	for _, s := range []string{`{"x":"\ud83d\ude00"}`, `{"x":"\\ud800"}`, `{"a":9007199254740993}`, ` [null,true,false,"text",1e0] `} {
		if _, e := Decode([]byte(s), 65536); e != nil {
			t.Fatalf("valid input rejected: %s", s)
		}
	}
	v, e := Decode([]byte(`9007199254740993`), 16)
	if e != nil || v != json.Number("9007199254740993") {
		t.Fatal("number lost precision")
	}
	if _, e = Decode([]byte(`{}`), 1); e == nil {
		t.Fatal("size limit ignored")
	}
	if _, e = Decode([]byte(`{}`), 0); e == nil {
		t.Fatal("unbounded size accepted")
	}
}
func TestExactObject(t *testing.T) {
	for _, s := range []string{`{"ID":1}`, `{"id":1,"extra":2}`, `null`, `[]`} {
		v, _ := Decode([]byte(s), 100)
		if _, e := ExactObject(v, []string{"id"}, nil); e == nil {
			t.Fatal("case/extra/type accepted")
		}
	}
	v, _ := Decode([]byte(`{"id":1}`), 100)
	if _, e := ExactObject(v, []string{"id"}, nil); e != nil {
		t.Fatal(e)
	}
}
func TestSafeInteger(t *testing.T) {
	for _, s := range []string{"1", "1.0", "1e0", "9007199254740991", "90071992547409910e-1"} {
		if _, e := SafeInteger(json.Number(s), 1); e != nil {
			t.Fatalf("valid %s", s)
		}
	}
	for _, s := range []string{"0", "-1", "1.1", "9007199254740992", "9007199254740991.1", "1e999999999", "1e-999999999", "1/1", "01", "+1"} {
		if _, e := SafeInteger(json.Number(s), 1); e == nil {
			t.Fatalf("invalid %s", s)
		}
	}
}
func TestEnvelopeFixtures(t *testing.T) {
	b, e := os.ReadFile("../../testdata/contracts/envelope-cases.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name  string `json:"name"`
		Valid bool   `json:"valid"`
		JSON  string `json:"json"`
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			_, e := ValidateEnvelope([]byte(c.JSON), 65536)
			if (e == nil) != c.Valid {
				t.Fatalf("valid=%t error=%v", c.Valid, e)
			}
		})
	}
}
func TestBodyIsNotSchemaValidated(t *testing.T) {
	b, e := os.ReadFile("../../testdata/contracts/command-envelope.json")
	if e != nil {
		t.Fatal(e)
	}
	v, e := Decode(b, 65536)
	if e != nil {
		t.Fatal(e)
	}
	m := v.(map[string]any)
	m["body"] = map[string]any{"notASchemaField": true}
	b, e = json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	got, e := ValidateEnvelope(b, 65536)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ExactObject(got["body"], []string{"commandId"}, nil); e == nil {
		t.Fatal("caller schema gate must reject")
	}
}
