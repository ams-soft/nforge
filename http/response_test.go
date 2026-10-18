package nhttp

import (
	"encoding/xml"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/codec"
)

type user struct {
	XMLName xml.Name `json:"-" cbor:"-" xml:"user"`
	ID      int64    `json:"id" xml:"id,attr"`
	Name    string   `json:"name"`
}

func TestSendJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(stdhttp.MethodPost, "/users", nil)
	if err := SendJSON(rec, req, 201, user{ID: 1, Name: "Ana"}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 201 {
		t.Errorf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	if body := rec.Body.String(); body != `{"success":true,"status":201,"data":{"id":1,"name":"Ana"}}` {
		t.Errorf("body %s", body)
	}
}

func TestSendJSONError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(stdhttp.MethodGet, "/users/9?x=1", nil)
	if err := SendJSON(rec, req, 404, "user not found"); err != nil {
		t.Fatal(err)
	}
	want := `{"success":false,"status":404,"error":{"title":"404 ERROR NOT FOUND","detail":"user not found","instance":"/users/9"}}`
	if rec.Code != 404 || rec.Body.String() != want {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestSendNoContent(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := SendJSON(rec, httptest.NewRequest(stdhttp.MethodDelete, "/", nil), 204, user{}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 204 || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Errorf("status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestSendXML(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := SendXML(rec, httptest.NewRequest(stdhttp.MethodGet, "/", nil), 200, user{ID: 1, Name: "Ana"}); err != nil {
		t.Fatal(err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/xml; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	want := xml.Header + `<response><success>true</success><status>200</status><data id="1"><name>Ana</name></data></response>`
	if body := rec.Body.String(); body != want {
		t.Errorf("body %s", body)
	}
}

func TestSendCBOR(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := SendCBOR(rec, httptest.NewRequest(stdhttp.MethodGet, "/", nil), 200, user{ID: 1, Name: "Ana"}); err != nil {
		t.Fatal(err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/cbor" {
		t.Errorf("Content-Type %q", ct)
	}
	var resp struct {
		Success bool `json:"success"`
		Status  int  `json:"status"`
		Data    user `json:"data"`
	}
	req := httptest.NewRequest(stdhttp.MethodPost, "/", rec.Body)
	req.Header.Set("Content-Type", rec.Header().Get("Content-Type"))
	if err := ParseCBOR(req, &resp); err != nil || !resp.Success || resp.Status != 200 || resp.Data.ID != 1 || resp.Data.Name != "Ana" {
		t.Fatalf("ParseCBOR: %v %+v", err, resp)
	}
}

func TestSendEncodeErrorWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	err := SendJSON(rec, httptest.NewRequest(stdhttp.MethodGet, "/", nil), 200, map[string]any{"c": make(chan int)})
	if !errors.Is(err, codec.ErrUnsupportedType) {
		t.Fatalf("expected ErrUnsupportedType, got %v", err)
	}
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Fatal("nothing should have been written")
	}
}

func newRequest(contentType, body string) *stdhttp.Request {
	r := httptest.NewRequest(stdhttp.MethodPost, "/", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

func TestParse(t *testing.T) {
	var u user
	if err := ParseJSON(newRequest("application/json; charset=utf-8", `{"id":7,"name":"Bia"}`), &u); err != nil || u.ID != 7 || u.Name != "Bia" {
		t.Fatalf("ParseJSON: %v %+v", err, u)
	}
	u = user{}
	if err := ParseXML(newRequest("application/xml", `<user id="8"><name>Caio</name></user>`), &u); err != nil || u.ID != 8 || u.Name != "Caio" {
		t.Fatalf("ParseXML: %v %+v", err, u)
	}
	if err := ParseXML(newRequest("application/xml", `<!DOCTYPE user><user/>`), &u); !errors.Is(err, codec.ErrDTDNotAllowed) {
		t.Fatalf("expected ErrDTDNotAllowed, got %v", err)
	}
}

func TestParseContentType(t *testing.T) {
	accepted := map[string][]string{
		codec.JSON: {"", "application/json", "Application/JSON; charset=utf-8", "application/problem+json"},
		codec.XML:  {"", "application/xml", "text/xml; charset=utf-8", "application/soap+xml"},
		codec.CBOR: {"", "application/cbor", "application/senml+cbor"},
	}
	rejected := map[string][]string{
		codec.JSON: {"application/x-www-form-urlencoded", "text/plain", "application/xml", "json"},
		codec.XML:  {"application/x-www-form-urlencoded", "application/json", "multipart/form-data; boundary=x"},
		codec.CBOR: {"application/octet-stream", "application/json"},
	}
	for format, cts := range accepted {
		for _, ct := range cts {
			if err := checkContentType(ct, format); err != nil {
				t.Errorf("%s should accept %q: %v", format, ct, err)
			}
		}
	}
	for format, cts := range rejected {
		for _, ct := range cts {
			err := checkContentType(ct, format)
			var me *UnsupportedMediaTypeError
			if !errors.As(err, &me) || me.Got != ct || ParseStatus(err) != stdhttp.StatusUnsupportedMediaType {
				t.Errorf("%s should reject %q with 415, got %v", format, ct, err)
			}
		}
	}

	// The real case: an XML body sent as a form is never even read.
	body := strings.NewReader("%3Cuser+id%3D%221%22%3E")
	r := httptest.NewRequest(stdhttp.MethodPost, "/", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var u user
	if err := ParseXML(r, &u); ParseStatus(err) != stdhttp.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %v", err)
	}
	if body.Len() == 0 {
		t.Fatal("body should not have been read")
	}
}

type adult struct {
	Age int `json:"age"`
}

func (a *adult) Validate() error {
	if a.Age < 18 {
		return errors.New("underage")
	}
	return nil
}

func TestParseStatus(t *testing.T) {
	big := `{"name":"` + strings.Repeat("x", codec.DefaultMaxPayloadBytes) + `"}`
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 200},
		{"sintaxe", ParseJSON(newRequest("application/json", `{"id":`), &user{}), 400},
		{"unknown field/type", ParseJSON(newRequest("application/json", `{"id":"x"}`), &user{}), 400},
		{"content-type", ParseJSON(newRequest("text/plain", `{}`), &user{}), 415},
		{"payload grande", ParseJSON(newRequest("application/json", big), &user{}), 413},
		{"validation", ParseJSON(newRequest("application/json", `{"age":10}`), &adult{}), 422},
	}
	for _, c := range cases {
		if got := ParseStatus(c.err); got != c.want {
			t.Errorf("%s: ParseStatus(%v) = %d, expected %d", c.name, c.err, got, c.want)
		}
	}

	// A format not enabled in the codec is also 415.
	r, err := codec.New(codec.Config{Formats: []string{"json"}})
	if err != nil {
		t.Fatal(err)
	}
	prev := codec.Default()
	codec.SetDefault(r)
	defer codec.SetDefault(prev)
	if got := ParseStatus(ParseXML(newRequest("application/xml", `<user/>`), &user{})); got != 415 {
		t.Errorf("xml disabled: status %d, expected 415", got)
	}
}
