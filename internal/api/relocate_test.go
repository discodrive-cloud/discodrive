package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"testing"
)

func TestRelocateAPIIsAtomic(t *testing.T) {
	_, do := modTimeServer(t)
	request := func(method, path string, body any, want int) map[string]json.RawMessage {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rec := do(httptest.NewRequest(method, path, bytes.NewReader(data)))
		if rec.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		var out map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	idOf := func(m map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if err := json.Unmarshal(m["id"], &id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	folder := func(name string) string {
		return idOf(request("POST", "/files/folder", map[string]string{"name": name}, 201))
	}
	a, b := folder("a"), folder("b")
	upload := func(parent, name, content string) string {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := mw.WriteField("parent_id", parent); err != nil {
			t.Fatal(err)
		}
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err = mw.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/files/upload", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := do(req)
		if rec.Code != 201 {
			t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Node map[string]json.RawMessage `json:"node"`
		}
		if err = json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return idOf(out.Node)
	}
	assertNodes := func(parent string, want map[string]string) {
		t.Helper()
		rec := do(httptest.NewRequest("GET", "/files?parent_id="+parent, nil))
		if rec.Code != 200 {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var nodes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
			t.Fatal(err)
		}
		if len(nodes) != len(want) {
			t.Fatalf("nodes=%v want=%v", nodes, want)
		}
		for _, n := range nodes {
			if want[n.ID] != n.Name {
				t.Fatalf("node=%v want=%v", n, want)
			}
		}
	}
	src := upload(a, "report.txt", "source")
	occupied := upload(b, "report.txt", "existing")
	// A collision at the intermediate name must not prevent the final move.
	request("PATCH", "/files/"+src+"/relocate", map[string]any{"parent_id": b, "name": "new.txt"}, 200)
	assertNodes(a, map[string]string{})
	assertNodes(b, map[string]string{src: "new.txt", occupied: "report.txt"})
	// A final-name collision must preserve both the source and the target.
	other := upload(a, "other.txt", "other")
	request("PATCH", "/files/"+other+"/relocate", map[string]any{"parent_id": b, "name": "report.txt"}, 409)
	assertNodes(a, map[string]string{other: "other.txt"})
	assertNodes(b, map[string]string{src: "new.txt", occupied: "report.txt"})
	// Invalid names also leave the source untouched.
	request("PATCH", "/files/"+other+"/relocate", map[string]any{"parent_id": b, "name": "../escape"}, 400)
	assertNodes(a, map[string]string{other: "other.txt"})
	// Moving to the root is a supported atomic operation too.
	request("PATCH", "/files/"+other+"/relocate", map[string]any{"parent_id": nil, "name": "root.txt"}, 200)
	assertNodes(a, map[string]string{})
}
