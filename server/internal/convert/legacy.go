package convert

import (
	"encoding/json"
	"github.com/google/uuid"
	"io"
)

func GeminiToLegacy(model string, raw []byte) ([]byte, error) {
	converted, err := GeminiToOpenAI(model, raw, "cmpl-"+uuid.NewString())
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err = json.Unmarshal(converted, &payload); err != nil {
		return nil, err
	}
	payload["object"] = "text_completion"
	for _, v := range AsSlice(payload["choices"]) {
		c := AsMap(v)
		msg := AsMap(c["message"])
		c["text"] = msg["content"]
		c["logprobs"] = nil
		delete(c, "message")
	}
	return json.Marshal(payload)
}

type legacySSEWriter struct{ dst io.Writer }

func (w legacySSEWriter) Write(p []byte) (int, error) {
	// WriteOpenAISSE emits one complete event per Write.
	if string(p) == "data: [DONE]\n\n" {
		n, err := w.dst.Write(p)
		return n, err
	}
	data := p
	if len(data) >= 6 && string(data[:6]) == "data: " {
		data = data[6:]
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return 0, err
	}
	payload["object"] = "text_completion"
	for _, v := range AsSlice(payload["choices"]) {
		c := AsMap(v)
		delta := AsMap(c["delta"])
		c["text"] = AsString(delta["content"])
		c["logprobs"] = nil
		delete(c, "delta")
	}
	if err := writeSSE(w.dst, payload); err != nil {
		return 0, err
	}
	return len(p), nil
}
func WriteLegacySSE(dst io.Writer, model string, src io.Reader) (StreamStats, error) {
	return WriteOpenAISSE(legacySSEWriter{dst}, model, src)
}
