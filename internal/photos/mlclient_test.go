package photos

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mlEmbedding is a 512-number embedding as the service sends it: a JSON array in a string.
func mlEmbedding(first ...float32) string {
	v := make([]float32, 512)
	copy(v, first)
	b, _ := json.Marshal(v)
	return string(b)
}

func writeJPEGStub(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "p.jpg")
	if err := os.WriteFile(p, []byte("\xff\xd8\xff\xe0jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMLClientParsesStringEmbedding(t *testing.T) {
	var entries, model string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/predict" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		entries = r.FormValue("entries")
		f, _, err := r.FormFile("image")
		if err != nil {
			t.Error("no image part")
		} else {
			b, _ := io.ReadAll(f)
			if !strings.HasPrefix(string(b), "\xff\xd8") {
				t.Error("image is not the file")
			}
		}
		var e map[string]map[string]map[string]any
		_ = json.Unmarshal([]byte(entries), &e)
		model, _ = e["facial-recognition"]["recognition"]["modelName"].(string)
		_, _ = io.WriteString(w, `{"imageHeight":2048,"imageWidth":1536,"facial-recognition":[`+
			`{"boundingBox":{"x1":422.0,"y1":889.0,"x2":474.0,"y2":948.0},"score":0.87,"embedding":"`+
			strings.ReplaceAll(mlEmbedding(3, 4), `"`, `\"`)+`"}]}`)
	}))
	defer srv.Close()

	c := &MLClient{URL: srv.URL, Model: "buffalo_l", MinScore: 0.7}
	got, err := c.Detect(context.Background(), writeJPEGStub(t))
	if err != nil {
		t.Fatal(err)
	}
	if model != "buffalo_l" || !strings.Contains(entries, `"minScore":0.7`) {
		t.Fatalf("entries %s", entries)
	}
	if len(got) != 1 {
		t.Fatalf("faces %+v", got)
	}
	f := got[0]
	near := func(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-4 }
	if !near(f.X1, 422.0/1536) || !near(f.Y1, 889.0/2048) || !near(f.X2, 474.0/1536) || !near(f.Y2, 948.0/2048) || !near(f.Score, 0.87) {
		t.Fatalf("box %+v", f)
	}
	if len(f.Vec) != 512 || !near(f.Vec[0], 0.6) || !near(f.Vec[1], 0.8) {
		t.Fatalf("vector not normalised: %v", f.Vec[:3])
	}
}

func TestMLClientUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	c := &MLClient{URL: srv.URL, Model: "buffalo_l", MinScore: 0.7}
	if _, err := c.Detect(context.Background(), writeJPEGStub(t)); !errors.Is(err, ErrMLUnavailable) {
		t.Fatalf("500: %v", err)
	}
	srv.Close()
	if _, err := c.Detect(context.Background(), writeJPEGStub(t)); !errors.Is(err, ErrMLUnavailable) {
		t.Fatalf("closed port: %v", err)
	}
	if c.Ping(context.Background()) {
		t.Fatal("ping of a closed port")
	}
}

func TestMLClientBadImageIsNotUnavailable(t *testing.T) {
	// The service answers 422 for a picture it cannot decode: that is the file's fault.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"bad image"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	c := &MLClient{URL: srv.URL, Model: "buffalo_l", MinScore: 0.7}
	_, err := c.Detect(context.Background(), writeJPEGStub(t))
	if err == nil || errors.Is(err, ErrMLUnavailable) {
		t.Fatalf("422: %v", err)
	}
}

func TestMLClientProxyErrorsAreUnavailable(t *testing.T) {
	// A proxy in front of the service refusing (auth, size, rate) is no fault of the file:
	// failing every photo of the library on it would be wrong.
	for _, code := range []int{401, 403, 413, 429} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		c := &MLClient{URL: srv.URL, Model: "buffalo_l", MinScore: 0.7}
		if _, err := c.Detect(context.Background(), writeJPEGStub(t)); !errors.Is(err, ErrMLUnavailable) {
			t.Errorf("%d: %v", code, err)
		}
		srv.Close()
	}
}

func TestMLClientWrongModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"detail":"model 'buffalo_l' is not served here","code":"unknown_model"}`)
	}))
	defer srv.Close()
	c := &MLClient{URL: srv.URL, Model: "buffalo_l", MinScore: 0.7}
	_, err := c.Detect(context.Background(), writeJPEGStub(t))
	if !errors.Is(err, ErrMLWrongModel) || !strings.Contains(err.Error(), "buffalo_l") {
		t.Fatalf("wrong model: %v", err)
	}
}
