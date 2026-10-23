package field

import (
	"encoding/json"
	"strings"
	"testing"
)

// Invisible characters are built from their code points: the compiler refuses a literal byte order mark.
var (
	nbsp = string(rune(0xA0))
	bom  = string(rune(0xFEFF))
	zwsp = string(rune(0x200B))
	lrm  = string(rune(0x200E))
)

type body struct {
	Name     String `json:"name"`
	Password Secret `json:"password"`
	Memory   Int    `json:"memory"`
	Public   Bool   `json:"public"`
}

func decode(t *testing.T, doc string) body {
	t.Helper()
	var b body
	if err := json.Unmarshal([]byte(doc), &b); err != nil {
		t.Fatalf("decoding %s failed: %v", doc, err)
	}
	return b
}

func TestAbsentIsTheZeroValue(t *testing.T) {
	b := decode(t, `{}`)
	if b.Name.State != Absent || b.Password.State != Absent || b.Memory.State != Absent || b.Public.State != Absent {
		t.Errorf("a missing key must be Absent: %+v", b)
	}
	if Absent.Sent() || !Null.Sent() || !Valid.Sent() || !Invalid.Sent() {
		t.Error("Sent must be false only for Absent")
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		doc   string
		state State
		value string
	}{
		{`{"name":"Ada"}`, Valid, "Ada"},
		{`{"name":"  Ada \t"}`, Valid, "Ada"},
		{`{"name":"` + nbsp + `Ada` + nbsp + `"}`, Valid, "Ada"},
		{`{"name":"` + bom + `Ada` + zwsp + lrm + `"}`, Valid, "Ada"},
		{`{"name":"A  d a"}`, Valid, "A  d a"},
		{`{"name":"Zoë"}`, Valid, "Zoë"},
		{`{"name":null}`, Null, ""},
		{`{"name":""}`, Null, ""},
		{`{"name":"   "}`, Null, ""},
		{`{"name":"` + zwsp + `"}`, Null, ""},
		{`{"name":5}`, Invalid, ""},
		{`{"name":true}`, Invalid, ""},
		{`{"name":["a"]}`, Invalid, ""},
		{`{"name":{"a":1}}`, Invalid, ""},
	}
	for _, tt := range tests {
		t.Run(tt.doc, func(t *testing.T) {
			got := decode(t, tt.doc).Name
			if got.State != tt.state || got.V != tt.value {
				t.Errorf("got %+v, want state %d value %q", got, tt.state, tt.value)
			}
		})
	}
}

func TestSecretKeepsWhitespace(t *testing.T) {
	tests := []struct {
		doc   string
		state State
		value string
	}{
		{`{"password":" pass word "}`, Valid, " pass word "},
		{`{"password":"   "}`, Valid, "   "},
		{`{"password":""}`, Null, ""},
		{`{"password":null}`, Null, ""},
		{`{"password":12345678}`, Invalid, ""},
	}
	for _, tt := range tests {
		t.Run(tt.doc, func(t *testing.T) {
			got := decode(t, tt.doc).Password
			if got.State != tt.state || got.V != tt.value {
				t.Errorf("got %+v, want state %d value %q", got, tt.state, tt.value)
			}
		})
	}
}

func TestInt(t *testing.T) {
	tests := []struct {
		doc   string
		state State
		value int64
	}{
		{`{"memory":1024}`, Valid, 1024},
		{`{"memory":0}`, Valid, 0},
		{`{"memory":-1}`, Valid, -1},
		{`{"memory":"1024"}`, Valid, 1024},
		{`{"memory":" 1024 "}`, Valid, 1024},
		{`{"memory":"+7"}`, Valid, 7},
		{`{"memory":"-1"}`, Valid, -1},
		{`{"memory":"007"}`, Valid, 7},
		{`{"memory":2048.0}`, Valid, 2048},
		{`{"memory":1e3}`, Valid, 1000},
		{`{"memory":9007199254740992}`, Valid, 9007199254740992},
		{`{"memory":9007199254740992.0}`, Valid, 9007199254740992},
		{`{"memory":-9007199254740992.0}`, Valid, -9007199254740992},
		{`{"memory":9007199254740994.0}`, Invalid, 0},
		{`{"memory":null}`, Null, 0},
		{`{"memory":""}`, Null, 0},
		{`{"memory":"  "}`, Null, 0},

		{`{"memory":1.5}`, Invalid, 0},
		{`{"memory":"1.5"}`, Invalid, 0},
		{`{"memory":"1e3"}`, Invalid, 0},
		{`{"memory":"0x10"}`, Invalid, 0},
		{`{"memory":"12abc"}`, Invalid, 0},
		{`{"memory":"+"}`, Invalid, 0},
		{`{"memory":"-"}`, Invalid, 0},
		{`{"memory":"+-5"}`, Invalid, 0},
		{`{"memory":"--5"}`, Invalid, 0},
		{`{"memory":"٣"}`, Invalid, 0},
		{`{"memory":"99999999999999999999"}`, Invalid, 0},
		{`{"memory":1e400}`, Invalid, 0},
		{`{"memory":18446744073709551615}`, Invalid, 0},
		{`{"memory":1e19}`, Invalid, 0},
		{`{"memory":true}`, Invalid, 0},
		{`{"memory":[1]}`, Invalid, 0},
		{`{"memory":{}}`, Invalid, 0},
	}
	for _, tt := range tests {
		t.Run(tt.doc, func(t *testing.T) {
			got := decode(t, tt.doc).Memory
			if got.State != tt.state || got.V != tt.value {
				t.Errorf("got %+v, want state %d value %d", got, tt.state, tt.value)
			}
		})
	}
}

func TestIntKeepsLargeWholeNumbersExact(t *testing.T) {
	got := decode(t, `{"memory":"9223372036854775807"}`).Memory
	if got.State != Valid || got.V != 1<<63-1 {
		t.Errorf("got %+v, want the largest int64 untouched", got)
	}
}

func TestBool(t *testing.T) {
	tests := []struct {
		doc   string
		state State
		value bool
	}{
		{`{"public":true}`, Valid, true},
		{`{"public":false}`, Valid, false},
		{`{"public":1}`, Valid, true},
		{`{"public":0}`, Valid, false},
		{`{"public":"1"}`, Valid, true},
		{`{"public":"0"}`, Valid, false},
		{`{"public":" 1 "}`, Valid, true},
		{`{"public":null}`, Null, false},
		{`{"public":""}`, Null, false},

		{`{"public":"true"}`, Invalid, false},
		{`{"public":"false"}`, Invalid, false},
		{`{"public":"yes"}`, Invalid, false},
		{`{"public":2}`, Invalid, false},
		{`{"public":-1}`, Invalid, false},
		{`{"public":1.0}`, Invalid, false},
		{`{"public":"01"}`, Invalid, false},
		{`{"public":[true]}`, Invalid, false},
	}
	for _, tt := range tests {
		t.Run(tt.doc, func(t *testing.T) {
			got := decode(t, tt.doc).Public
			if got.State != tt.state || got.V != tt.value {
				t.Errorf("got %+v, want state %d value %v", got, tt.state, tt.value)
			}
		})
	}
}

func TestNeverFailsWhileDecoding(t *testing.T) {
	var b body
	doc := `{"name":[1],"password":{},"memory":"x","public":"maybe"}`
	if err := json.Unmarshal([]byte(doc), &b); err != nil {
		t.Fatalf("a wrong type must be marked, not fail the whole body: %v", err)
	}
	for _, s := range []State{b.Name.State, b.Password.State, b.Memory.State, b.Public.State} {
		if s != Invalid {
			t.Errorf("state = %d, want Invalid", s)
		}
	}
}

func TestLongTextIsKept(t *testing.T) {
	long := strings.Repeat("é", 5000)
	got := decode(t, `{"name":"`+long+`"}`).Name
	if got.State != Valid || got.V != long {
		t.Error("a long value must be passed on whole: length limits belong to the validator")
	}
}

func TestText(t *testing.T) {
	tests := []struct {
		doc   string
		state State
		value string
	}{
		{`{"v":"paper.jar"}`, Valid, "paper.jar"},
		{`{"v":"  padded "}`, Valid, "padded"},
		{`{"v":25565}`, Valid, "25565"},
		{`{"v":-1}`, Valid, "-1"},
		{`{"v":0}`, Valid, "0"},
		{`{"v":1.5}`, Valid, "1.5"},
		{`{"v":2048.0}`, Valid, "2048"},
		{`{"v":1e3}`, Valid, "1000"},
		{`{"v":12345678901234567890}`, Valid, "12345678901234567000"},
		{`{"v":9223372036854775807}`, Valid, "9223372036854775807"},
		{`{"v":null}`, Null, ""},
		{`{"v":""}`, Null, ""},
		{`{"v":"   "}`, Null, ""},
		{`{"v":true}`, Invalid, ""},
		{`{"v":false}`, Invalid, ""},
		{`{"v":["a"]}`, Invalid, ""},
		{`{"v":{"a":1}}`, Invalid, ""},
		{`{"v":1e999}`, Invalid, ""},
	}
	for _, tt := range tests {
		t.Run(tt.doc, func(t *testing.T) {
			var b struct{ V Text }
			if err := json.Unmarshal([]byte(tt.doc), &b); err != nil {
				t.Fatalf("decoding failed: %v", err)
			}
			if b.V.State != tt.state || b.V.V != tt.value {
				t.Errorf("got %+v, want state %d value %q", b.V, tt.state, tt.value)
			}
		})
	}
}

func TestTextAbsent(t *testing.T) {
	var b struct{ V Text }
	if err := json.Unmarshal([]byte(`{}`), &b); err != nil || b.V.State != Absent {
		t.Errorf("got %+v, %v", b.V, err)
	}
}
