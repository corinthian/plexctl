package jsonx

import "testing"

func TestTruthy(t *testing.T) {
	falsy := []any{nil, false, 0.0, "", []any{}, map[string]any{}, 0, int64(0)}
	for _, v := range falsy {
		if Truthy(v) {
			t.Errorf("Truthy(%#v) = true, want false", v)
		}
	}
	truthy := []any{true, 1.0, "x", []any{1}, map[string]any{"a": 1}, -1.0}
	for _, v := range truthy {
		if !Truthy(v) {
			t.Errorf("Truthy(%#v) = false, want true", v)
		}
	}
}

func TestAsStrIntegralFloat(t *testing.T) {
	// PMS numbers arrive as float64; str(playQueueID) must not grow ".0".
	cases := map[any]string{
		float64(5535): "5535",
		float64(0):    "0",
		"already":     "already",
		float64(1.5):  "1.5",
		int(7):        "7",
	}
	for in, want := range cases {
		if got := AsStr(in); got != want {
			t.Errorf("AsStr(%#v) = %q, want %q", in, got, want)
		}
	}
}

func TestMarshalNoHTMLEscape(t *testing.T) {
	got := Marshal(J{"error": "HTTP 404: <html> & stuff"})
	want := `{"error":"HTTP 404: <html> & stuff"}`
	if got != want {
		t.Errorf("Marshal = %q, want %q", got, want)
	}
}

func TestGetHelpers(t *testing.T) {
	m := J{"MediaContainer": map[string]any{"Metadata": []any{map[string]any{"title": "x"}, "junk"}}}
	mc := GetMap(m, "MediaContainer")
	if len(MapList(mc, "Metadata")) != 1 {
		t.Fatal("MapList should keep only object elements")
	}
	if GetMap(m, "missing") == nil || GetList(m, "missing") != nil {
		t.Fatal("missing-key defaults wrong")
	}
}
