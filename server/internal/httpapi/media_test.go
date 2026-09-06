package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wo/antigravity2api/internal/convert"
)

const generatedImagePayload = `{"response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AQI="}}]},"finishReason":"STOP"}]}}`

func mediaMultipart(t *testing.T, fields map[string]string, name, filename, data string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	file, err := writer.CreateFormFile(name, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	return r
}
func TestImageJSONAndMultipartInputsMapToNativeImages(t *testing.T) {
	for _, r := range []*http.Request{
		httptest.NewRequest("POST", "/", strings.NewReader(`{"prompt":"make blue","model":"gemini-3.1-flash-image","image":"data:image/png;base64,AQI=","size":"1024x1024","quality":"high"}`)),
		mediaMultipart(t, map[string]string{"prompt": "make blue", "model": "gemini-3.1-flash-image", "size": "1024x1024", "quality": "high"}, "image", "input.png", "image bytes"),
	} {
		req, err := parseImageAPIRequest(httptest.NewRecorder(), r, true)
		if err != nil {
			t.Fatal(err)
		}
		outer, _, _ := convert.OpenAIToGemini(imageOpenAIRequest(req), "project", "email", "account")
		encoded, _ := json.Marshal(outer)
		if !strings.Contains(string(encoded), `"inlineData"`) || !strings.Contains(string(encoded), `"image/png"`) || !strings.Contains(string(encoded), `"requestType":"image_gen"`) || !strings.Contains(string(encoded), `"aspectRatio":"1:1"`) || !strings.Contains(string(encoded), `"imageSize":"4K"`) {
			t.Fatalf("incorrect image upstream request: %s", encoded)
		}
	}
}
func TestImageInvalidInputDoesNotCallUpstream(t *testing.T) {
	called := false
	up := &fakeUpstream{call: func(context.Context, string, any, bool, bool) (*http.Response, []byte, error) {
		called = true
		return upstreamResponse(200, generatedImagePayload, false)
	}}
	s, _ := newProxyFixture(t, 1, up)
	for _, body := range []string{`{"prompt":"draw","n":2}`, `{"prompt":"edit","image":"http://127.0.0.1/private"}`, `{"prompt":"edit","image":"data:image/png;base64,!!!!"}`, `{"prompt":"draw","stream":true}`, `{"prompt":"draw","mask":"data:image/png;base64,AQI="}`, `{"prompt":"draw","response_format":"file"}`} {
		w := httptest.NewRecorder()
		s.imagesGenerations(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("bad input status %d: %s", w.Code, w.Body)
		}
	}
	if called {
		t.Fatal("invalid media input reached upstream")
	}
}
func TestImagesHandlerReturnsImagesSchema(t *testing.T) {
	var payload string
	up := &fakeUpstream{call: func(_ context.Context, _ string, p any, stream, count bool) (*http.Response, []byte, error) {
		b, _ := json.Marshal(p)
		payload = string(b)
		if stream || count {
			t.Error("image used streaming/count method")
		}
		return upstreamResponse(200, generatedImagePayload, false)
	}}
	s, _ := newProxyFixture(t, 1, up)
	for _, format := range []string{"b64_json", "url"} {
		w := httptest.NewRecorder()
		s.imagesGenerations(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"prompt":"draw","response_format":"`+format+`"}`)))
		var response map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatalf("images handler failed: %d %s", w.Code, w.Body)
		}
		data := convert.AsSlice(response["data"])
		if len(data) != 1 || convert.AsMap(data[0])[format] == nil || response["choices"] != nil {
			t.Fatalf("wrong images schema: %s", w.Body)
		}
		if format == "url" && !strings.HasPrefix(convert.AsString(convert.AsMap(data[0])[format]), "data:image/png;base64,") {
			t.Fatal("image URL does not contain usable inline data")
		}
	}
	if !strings.Contains(payload, `"candidateCount":1`) || !strings.Contains(payload, `"requestType":"image_gen"`) {
		t.Fatalf("bad image request %s", payload)
	}
}
func TestImageResponseRejectsEmptyOrInvalidData(t *testing.T) {
	for _, raw := range []string{successfulGemini, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"!!!!"}}]}}]}`, `{"error":{"code":429,"message":"wait"}}`} {
		if _, err := imagesResponseJSON([]byte(raw), "b64_json"); err == nil {
			t.Fatal("bad image was success")
		}
	}
	_, err := imagesResponseJSON([]byte(`{"error":{"code":429,"message":"wait"}}`), "b64_json")
	var upstream *convert.UpstreamError
	if !errors.As(err, &upstream) || upstream.Code != 429 {
		t.Fatalf("lost embedded upstream status: %v", err)
	}
}
func TestMultipartFileLimit(t *testing.T) {
	r := mediaMultipart(t, map[string]string{"prompt": "edit"}, "image", "image.png", strings.Repeat("x", maxMediaFileBytes+1))
	_, err := parseImageAPIRequest(httptest.NewRecorder(), r, true)
	var limit *http.MaxBytesError
	if !errors.As(err, &limit) {
		t.Fatalf("missing file limit: %v", err)
	}
}
func TestAudioTranscriptionPreservesFormatAndTextResponse(t *testing.T) {
	var payload string
	up := &fakeUpstream{call: func(_ context.Context, _ string, p any, stream, count bool) (*http.Response, []byte, error) {
		b, _ := json.Marshal(p)
		payload = string(b)
		return upstreamResponse(200, successfulGemini, false)
	}}
	s, _ := newProxyFixture(t, 1, up)
	r := mediaMultipart(t, map[string]string{"model": "gemini-2.5-flash", "response_format": "text", "language": "en"}, "file", "speech.webm", "audio bytes")
	w := httptest.NewRecorder()
	s.audioTranscriptions(w, r)
	if w.Code != 200 || w.Body.String() != "hello" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("bad transcript response: %d %s %s", w.Code, w.Header(), w.Body)
	}
	if !strings.Contains(payload, `"mimeType":"audio/webm"`) || !strings.Contains(payload, `"inlineData"`) {
		t.Fatalf("audio MIME lost: %s", payload)
	}
}
func TestAudioUnsupportedOptionsAreRejected(t *testing.T) {
	s := &Server{}
	for _, fields := range []map[string]string{{"response_format": "srt"}, {"timestamp_granularities[]": "word"}, {"temperature": "NaN"}, {"stream": "true"}, {"stream": "1"}, {"stream": "garbage"}} {
		w := httptest.NewRecorder()
		s.audioTranscriptions(w, mediaMultipart(t, fields, "file", "speech.mp3", "audio bytes"))
		if w.Code != 400 {
			t.Fatalf("unsupported transcription option accepted: %d", w.Code)
		}
	}
}
func TestTranscriptSkipsThinkingAndRejectsEmpty(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"reason","thought":true},{"text":"hello "},{"text":"world"}]}}]}`)
	response, err := transcriptionResponse(raw, "json")
	if err != nil || string(response) != `{"text":"hello world"}` {
		t.Fatalf("bad transcript: %s %v", response, err)
	}
	if _, err := transcriptionResponse([]byte(`{"candidates":[]}`), "json"); err == nil {
		t.Fatal("empty transcript accepted")
	}
}
