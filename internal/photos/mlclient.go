package photos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"discodrive/internal/photos/faces"
)

// ErrMLUnavailable: the face service did not answer (network, timeout, 5xx). It is never
// the file's fault: the asset stays in the queue.
var ErrMLUnavailable = errors.New("photos: face service unavailable")

// ErrMLWrongModel: the service answers but does not serve the configured model
// (PHOTOS_ML_MODEL): a setting to fix, never the photos' fault.
var ErrMLWrongModel = errors.New("photos: face service does not serve the model")

// MLClient talks to a face service with the HTTP API of Immich's machine-learning container
// (POST /predict): DiscoDrive's own ml container, or Immich's.
type MLClient struct {
	URL      string // e.g. http://immich-machine-learning:3003
	Model    string // auraface (ours), buffalo_l (Immich)
	MinScore float32
	HTTP     *http.Client // nil: a client with a 2 minute timeout (a cold model load)
}

// DetectedFace is a face of a picture: the box as fractions of the picture's size, the
// detection score and the normalised embedding.
type DetectedFace struct {
	X1, Y1, X2, Y2, Score float32
	Vec                   []float32
}

func (c *MLClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

// Detect sends the JPEG at path to the service and returns the faces on it.
func (c *MLClient) Detect(ctx context.Context, path string) ([]DetectedFace, error) {
	img, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries, _ := json.Marshal(map[string]any{"facial-recognition": map[string]any{
		"detection":   map[string]any{"modelName": c.Model, "options": map[string]any{"minScore": c.MinScore}},
		"recognition": map[string]any{"modelName": c.Model},
	}})
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("entries", string(entries))
	part, _ := mw.CreateFormFile("image", "image.jpg")
	_, _ = part.Write(img)
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/predict", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrMLUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMLUnavailable, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		var body struct{ Code, Detail string }
		if json.Unmarshal(raw, &body) == nil && body.Code == "unknown_model" {
			return nil, fmt.Errorf("%w %q: %s", ErrMLWrongModel, c.Model, body.Detail)
		}
	}
	switch {
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusNotFound,
		resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden,
		resp.StatusCode == http.StatusRequestEntityTooLarge, resp.StatusCode == http.StatusTooManyRequests:
		// A wrong URL, a proxy in front refusing, the service failing: not the file's
		// fault. (The queue tells a service that is up but fails on one picture apart.)
		return nil, fmt.Errorf("%w: %s", ErrMLUnavailable, resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("face service: %s: %s", resp.Status, truncate(string(raw), 200))
	}
	var out struct {
		W     float32 `json:"imageWidth"`
		H     float32 `json:"imageHeight"`
		Faces []struct {
			Box struct {
				X1, Y1, X2, Y2 float32
			} `json:"boundingBox"`
			Score     float32         `json:"score"`
			Embedding json.RawMessage `json:"embedding"`
		} `json:"facial-recognition"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("face service: bad answer: %v", err)
	}
	if out.W <= 0 || out.H <= 0 {
		return nil, errors.New("face service: no image size in the answer")
	}
	res := make([]DetectedFace, 0, len(out.Faces))
	for _, f := range out.Faces {
		vec, err := parseEmbedding(f.Embedding)
		if err != nil {
			return nil, fmt.Errorf("face service: %v", err)
		}
		faces.Normalize(vec)
		res = append(res, DetectedFace{
			X1: clamp01(f.Box.X1 / out.W), Y1: clamp01(f.Box.Y1 / out.H),
			X2: clamp01(f.Box.X2 / out.W), Y2: clamp01(f.Box.Y2 / out.H),
			Score: f.Score, Vec: vec,
		})
	}
	return res, nil
}

// parseEmbedding reads the embedding: a JSON array of numbers (our service), or that array
// inside a string (Immich's).
func parseEmbedding(raw json.RawMessage) ([]float32, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var v []float32
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("bad embedding: %v", err)
	}
	if len(v) != EmbeddingDim {
		return nil, fmt.Errorf("embedding of %d numbers, want %d", len(v), EmbeddingDim)
	}
	return v, nil
}

// EmbeddingDim is the length of a buffalo_l face embedding.
const EmbeddingDim = 512

func clamp01(x float32) float32 { return min(max(x, 0), 1) }

// Ping reports whether the service answers GET /ping.
func (c *MLClient) Ping(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/ping", nil)
	if err != nil {
		return false
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
