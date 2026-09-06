package convert

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxSSEEventBytes = 32 << 20
const MaxCollectedResponseBytes = 128 << 20
const maxStreamCandidates = 64
const maxStreamToolCalls = 4096
const maxSSEDataLines = 4096

// UpstreamError preserves an upstream failure even when it arrives inside HTTP 200 SSE.
type UpstreamError struct {
	Code    int
	Message string
	Type    string
}

func (e *UpstreamError) Error() string { return e.Message }
func streamFailure(kind, message string) error {
	return &UpstreamError{Code: 502, Type: kind, Message: message}
}

func payloadError(payload map[string]any) error {
	if v := payload["error"]; v != nil {
		m := AsMap(v)
		code := intVal(m["code"])
		if code < 400 || code > 599 {
			code = 502
		}
		message := firstNonEmpty(AsString(m["message"]), AsString(v), "upstream returned an error")
		return &UpstreamError{Code: code, Type: firstNonEmpty(AsString(m["status"]), AsString(m["type"]), "upstream_error"), Message: message}
	}
	if feedback := AsMap(payload["promptFeedback"]); AsString(feedback["blockReason"]) != "" {
		return &UpstreamError{Code: 400, Type: "content_filter", Message: "upstream blocked prompt: " + AsString(feedback["blockReason"])}
	}
	return nil
}

// geminiDecoder accepts standard multiline SSE and upstream's one-JSON-per-line
// variant. Only complete JSON values are dispatched early; partial data lines are
// retained until the next data line. A single event has a strict memory bound.
type geminiDecoder struct {
	reader *bufio.Reader
	data   []byte
	raw    bool
	eof    bool
	lines  int
}

func newGeminiDecoder(src io.Reader) *geminiDecoder {
	if src == nil {
		src = strings.NewReader("")
	}
	return &geminiDecoder{reader: bufio.NewReaderSize(src, 32<<10)}
}
func (d *geminiDecoder) line() ([]byte, error) {
	var line []byte
	for {
		piece, err := d.reader.ReadSlice('\n')
		if len(line)+len(piece) > MaxSSEEventBytes {
			return nil, streamFailure("event_too_large", "upstream SSE event exceeds size limit")
		}
		line = append(line, piece...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}
func decodeGeminiPayload(data []byte) (map[string]any, error) {
	if !utf8.Valid(data) {
		return nil, streamFailure("invalid_response", "upstream SSE contains invalid UTF-8")
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return nil, streamFailure("invalid_response", "invalid upstream JSON event")
	}
	if err := payloadError(m); err != nil {
		return nil, err
	}
	if inner := AsMap(m["response"]); inner != nil {
		m = inner
	}
	if err := payloadError(m); err != nil {
		return nil, err
	}
	return m, nil
}
func (d *geminiDecoder) next() (map[string]any, error) {
	for {
		if d.eof {
			return nil, io.EOF
		}
		line, err := d.line()
		if err != nil && err != io.EOF {
			return nil, err
		}
		if err == io.EOF {
			d.eof = true
		}
		line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		switch {
		case bytes.HasPrefix(line, []byte("data:")):
			d.lines++
			if d.lines > maxSSEDataLines {
				return nil, streamFailure("event_too_large", "upstream SSE event has too many data lines")
			}
			value := bytes.TrimPrefix(line, []byte("data:"))
			value = bytes.TrimPrefix(value, []byte(" "))
			if len(d.data) > 0 {
				d.data = append(d.data, '\n')
			}
			if len(d.data)+len(value) > MaxSSEEventBytes {
				return nil, streamFailure("event_too_large", "upstream SSE event exceeds size limit")
			}
			d.data = append(d.data, value...)
		case len(bytes.TrimSpace(line)) == 0:
			if len(bytes.TrimSpace(d.data)) > 0 && !json.Valid(d.data) && !bytes.Equal(bytes.TrimSpace(d.data), []byte("[DONE]")) {
				return nil, streamFailure("invalid_response", "invalid or truncated upstream SSE event")
			}
		case bytes.HasPrefix(line, []byte(":")), bytes.HasPrefix(line, []byte("event:")), bytes.HasPrefix(line, []byte("id:")), bytes.HasPrefix(line, []byte("retry:")):
			// SSE control fields and heartbeats do not contain model output.
		default:
			if d.raw || (len(d.data) == 0 && bytes.HasPrefix(bytes.TrimSpace(line), []byte("{"))) {
				d.raw = true
				if len(d.data)+len(line)+1 > MaxSSEEventBytes {
					return nil, streamFailure("event_too_large", "upstream JSON exceeds size limit")
				}
				d.data = append(d.data, line...)
				d.data = append(d.data, '\n')
			} else {
				return nil, streamFailure("invalid_response", "unexpected upstream stream data")
			}
		}
		data := bytes.TrimSpace(d.data)
		if bytes.Equal(data, []byte("[DONE]")) {
			d.data = nil
			d.lines = 0
			continue
		}
		if len(data) > 0 && json.Valid(data) {
			m, parseErr := decodeGeminiPayload(data)
			d.data = nil
			d.lines = 0
			d.raw = false
			return m, parseErr
		}
		if d.eof {
			if len(data) > 0 {
				return nil, streamFailure("invalid_response", "truncated upstream SSE event")
			}
			return nil, io.EOF
		}
	}
}

func candidateIndex(c map[string]any, fallback int) int {
	if c["index"] != nil {
		return intVal(c["index"])
	}
	return fallback
}
func candidateHasOutput(c map[string]any) bool {
	for _, v := range AsSlice(AsMap(c["content"])["parts"]) {
		p := AsMap(v)
		if AsString(p["text"]) != "" || AsMap(p["functionCall"]) != nil {
			return true
		}
		for _, key := range []string{"inlineData", "inline_data"} {
			if AsString(AsMap(p[key])["data"]) != "" {
				return true
			}
		}
		f := AsMap(p["fileData"])
		if firstNonEmpty(AsString(f["fileUri"]), AsString(f["file_uri"])) != "" {
			return true
		}
	}
	return false
}

// PrepareGeminiStream validates the first meaningful frame before HTTP headers
// are committed, then replays the consumed frames and buffered source bytes.
func PrepareGeminiStream(src io.Reader) (io.Reader, error) {
	d := newGeminiDecoder(src)
	var prefix bytes.Buffer
	for {
		payload, err := d.next()
		if err == io.EOF {
			return nil, streamFailure("empty_response", "empty upstream stream")
		}
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(payload)
		if prefix.Len()+len(b)+8 > MaxSSEEventBytes {
			return nil, streamFailure("event_too_large", "upstream prelude exceeds size limit")
		}
		prefix.WriteString("data: ")
		prefix.Write(b)
		prefix.WriteString("\n\n")
		for _, c := range AsSlice(payload["candidates"]) {
			cand := AsMap(c)
			if !candidateHasOutput(cand) && strings.EqualFold(AsString(cand["finishReason"]), "STOP") {
				return nil, streamFailure("empty_response", "upstream finished without output")
			}
			if candidateHasOutput(cand) || AsString(cand["finishReason"]) != "" {
				return io.MultiReader(bytes.NewReader(prefix.Bytes()), d.reader), nil
			}
		}
	}
}

type StreamStats struct {
	FirstTokenAt time.Time
	Usage        TokenUsage
}

func (s *StreamStats) note(text, thinking string, toolCalls []any, usage any) {
	if s == nil {
		return
	}
	if s.FirstTokenAt.IsZero() && (text != "" || thinking != "" || len(toolCalls) > 0) {
		s.FirstTokenAt = time.Now()
	}
	if u := TokenUsageFromOpenAI(usage); !u.Empty() {
		s.Usage = u
	}
}

type streamCandidate struct {
	finish  string
	content bool
}

func walkGeminiStream(src io.Reader, visit func(map[string]any) error) (StreamStats, error) {
	var stats StreamStats
	d := newGeminiDecoder(src)
	seen := map[int]*streamCandidate{}
	for {
		p, err := d.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stats, err
		}
		usage := geminiUsageToOpenAI(p["usageMetadata"])
		stats.note("", "", nil, usage)
		for i, v := range AsSlice(p["candidates"]) {
			c := AsMap(v)
			idx := candidateIndex(c, i)
			if c == nil || idx < 0 || idx >= maxStreamCandidates {
				return stats, streamFailure("invalid_response", "upstream returned invalid candidate index")
			}
			state := seen[idx]
			if state == nil {
				if len(seen) >= maxStreamCandidates {
					return stats, streamFailure("invalid_response", "upstream returned too many candidates")
				}
				state = &streamCandidate{}
				seen[idx] = state
			}
			if candidateHasOutput(c) {
				state.content = true
				if stats.FirstTokenAt.IsZero() {
					stats.FirstTokenAt = time.Now()
				}
			}
			if f := AsString(c["finishReason"]); f != "" {
				state.finish = f
			}
		}
		if err := visit(p); err != nil {
			return stats, err
		}
	}
	if len(seen) == 0 {
		return stats, streamFailure("empty_response", "upstream stream ended without candidates")
	}
	for _, state := range seen {
		if state.finish == "" {
			return stats, streamFailure("upstream_interrupted", "upstream stream ended without finishReason")
		}
		if !state.content && strings.EqualFold(state.finish, "STOP") {
			return stats, streamFailure("empty_response", "upstream stream ended without output")
		}
	}
	return stats, nil
}

func writeSSE(w io.Writer, payload any) error { return writeEvent(w, "", payload) }
func writeEvent(w io.Writer, event string, payload any) error {
	if w == nil {
		return io.ErrClosedPipe
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var frame []byte
	if event != "" {
		frame = append(frame, []byte("event: "+event+"\n")...)
	}
	frame = append(frame, []byte("data: ")...)
	frame = append(frame, b...)
	frame = append(frame, '\n', '\n')
	n, err := w.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	return err
}
func writeStreamError(dst io.Writer, protocol string, cause error) error {
	kind, message, code := "stream_error", cause.Error(), 502
	var upstream *UpstreamError
	if errors.As(cause, &upstream) {
		kind, message, code = upstream.Type, upstream.Message, upstream.Code
	}
	payload := map[string]any{"error": map[string]any{"type": kind, "message": message, "code": code}}
	event := ""
	if protocol == "claude" {
		event = "error"
		payload["type"] = "error"
	}
	if err := writeEvent(dst, event, payload); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

type streamToolIdentity struct {
	digest [32]byte
	id     string
	part   map[string]any
}
type streamToolRegistry struct {
	seen        map[string]streamToolIdentity
	usedIDs     map[string]bool
	count       int
	retainParts bool
}

func (r *streamToolRegistry) prepare(part map[string]any, tc map[string]any, index int) (map[string]any, bool, error) {
	if tc == nil {
		return nil, false, streamFailure("invalid_tool_call", "upstream returned an invalid function call")
	}
	if r.seen == nil {
		r.seen = map[string]streamToolIdentity{}
		r.usedIDs = map[string]bool{}
	}
	fc := AsMap(part["functionCall"])
	if strings.TrimSpace(AsString(fc["name"])) == "" {
		return nil, false, streamFailure("invalid_tool_call", "upstream returned a function call without a name")
	}
	var identityKey string
	var identityDigest [32]byte
	id := firstNonEmpty(AsString(fc["call_id"]), AsString(fc["id"]))
	if len(id) > 512 {
		return nil, false, streamFailure("invalid_tool_call", "upstream tool call ID exceeds length limit")
	}
	if id != "" {
		args := fc["args"]
		if args == nil {
			args = map[string]any{}
		}
		raw, _ := json.Marshal(map[string]any{"name": fc["name"], "args": args})
		digest := sha256.Sum256(raw)
		key := fmt.Sprintf("%d:%s", index, id)
		if previous, ok := r.seen[key]; ok {
			if previous.digest == digest {
				tc["id"] = previous.id
				if sig := partSignature(part); sig != "" && previous.part != nil {
					previous.part["thoughtSignature"] = sig
				}
				return tc, false, nil
			}
			return nil, false, streamFailure("invalid_tool_call", "upstream reused a tool call ID with different arguments")
		}
		identityKey, identityDigest = key, digest
	}
	if r.count >= maxStreamToolCalls {
		return nil, false, streamFailure("response_too_large", "upstream returned too many tool calls")
	}
	r.count++
	// Content equality is not identity: two calls without an upstream ID can be
	// intentional, even if their names and arguments are identical.
	if id == "" || r.usedIDs[id] {
		id = "call_" + uuid.NewString()
	}
	r.usedIDs[id] = true
	if identityKey != "" {
		identity := streamToolIdentity{digest: identityDigest, id: id}
		if r.retainParts {
			identity.part = part
		}
		r.seen[identityKey] = identity
	}
	tc["id"] = id
	return tc, true, nil
}

func partSignature(p map[string]any) string {
	return thoughtSignature(p)
}
func singlePartTool(part map[string]any) map[string]any {
	_, _, t, _, _ := collectParts(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{part}}}}})
	if len(t) == 0 {
		return nil
	}
	return AsMap(t[0])
}

func WriteOpenAISSE(dst io.Writer, model string, src io.Reader) (StreamStats, error) {
	id := "chatcmpl-" + uuid.NewString()
	created := nowUnix()
	type candidateState struct {
		role      bool
		toolIndex int
		finish    string
	}
	states := map[int]*candidateState{}
	var registry streamToolRegistry
	var finalUsage any
	emit := func(index int, delta any, finish any) error {
		return writeSSE(dst, map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": index, "delta": delta, "finish_reason": finish}}})
	}
	stats, err := walkGeminiStream(src, func(p map[string]any) error {
		if u := p["usageMetadata"]; u != nil {
			finalUsage = geminiUsageToOpenAI(u)
		}
		for i, v := range AsSlice(p["candidates"]) {
			c := AsMap(v)
			idx := candidateIndex(c, i)
			s := states[idx]
			if s == nil {
				s = &candidateState{}
				states[idx] = s
			}
			if finish := AsString(c["finishReason"]); finish != "" {
				s.finish = finish
			}
			if !s.role {
				if e := emit(idx, map[string]any{"role": "assistant"}, nil); e != nil {
					return e
				}
				s.role = true
			}
			for _, v := range AsSlice(AsMap(c["content"])["parts"]) {
				part := AsMap(v)
				if AsMap(part["functionCall"]) != nil {
					tc, ok, e := registry.prepare(part, singlePartTool(part), idx)
					if e != nil {
						return e
					}
					RememberToolSignature(model, AsString(tc["id"]), partSignature(part))
					if !ok {
						continue
					}
					tc["index"] = s.toolIndex
					s.toolIndex++
					if e := emit(idx, map[string]any{"tool_calls": []any{tc}}, nil); e != nil {
						return e
					}
					continue
				}
				text, thinking, _, _, _ := collectParts(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{part}}}}})
				if thinking != "" {

					if e := emit(idx, map[string]any{"reasoning_content": thinking}, nil); e != nil {
						return e
					}
				}
				if text != "" {
					if e := emit(idx, map[string]any{"content": text}, nil); e != nil {
						return e
					}
				}
				if sig := partSignature(part); sig != "" {
					if e := emit(idx, map[string]any{"reasoning_signature": sig}, nil); e != nil {
						return e
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return stats, writeStreamError(dst, "openai", err)
	}
	indices := make([]int, 0, len(states))
	for idx := range states {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		s := states[idx]

		if e := emit(idx, map[string]any{}, openaiFinish(s.finish, s.toolIndex > 0)); e != nil {
			return stats, e
		}
	}
	if finalUsage != nil {
		if e := writeSSE(dst, map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{}, "usage": finalUsage}); e != nil {
			return stats, e
		}
	}
	n, err := io.WriteString(dst, "data: [DONE]\n\n")
	if err == nil && n != len("data: [DONE]\n\n") {
		err = io.ErrShortWrite
	}
	return stats, err
}

func WriteClaudeSSE(dst io.Writer, model string, src io.Reader) (StreamStats, error) {
	msgID := "msg_" + uuid.NewString()
	started := false
	index := 0
	block := ""
	pendingSig := ""
	usedTool := false
	finish := ""
	var usage any
	var registry streamToolRegistry
	emit := func(event string, p map[string]any) error { p["type"] = event; return writeEvent(dst, event, p) }
	delta := func(kind string, value map[string]any) error {
		value["type"] = kind
		return emit("content_block_delta", map[string]any{"index": index, "delta": value})
	}
	closeBlock := func() error {
		if block == "" {
			return nil
		}
		if block == "thinking" && pendingSig != "" {
			if e := delta("signature_delta", map[string]any{"signature": pendingSig}); e != nil {
				return e
			}
			pendingSig = ""
		}
		if e := emit("content_block_stop", map[string]any{"index": index}); e != nil {
			return e
		}
		index++
		block = ""
		return nil
	}
	startBlock := func(kind string, content map[string]any) error {
		if e := closeBlock(); e != nil {
			return e
		}
		content["type"] = kind
		if e := emit("content_block_start", map[string]any{"index": index, "content_block": content}); e != nil {
			return e
		}
		block = kind
		return nil
	}
	stats, err := walkGeminiStream(src, func(p map[string]any) error {
		if u := p["usageMetadata"]; u != nil {
			usage = geminiUsageToOpenAI(u)
		}
		cands := AsSlice(p["candidates"])
		if len(cands) == 0 {
			return nil
		}
		c := AsMap(cands[0])
		if !started {
			if e := emit("message_start", map[string]any{"message": map[string]any{"id": msgID, "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": claudeUsage(usage)}}); e != nil {
				return e
			}
			started = true
		}
		if f := AsString(c["finishReason"]); f != "" {
			finish = f
		}
		for _, v := range AsSlice(AsMap(c["content"])["parts"]) {
			part := AsMap(v)
			sig := partSignature(part)
			if AsMap(part["functionCall"]) != nil {
				tc, ok, e := registry.prepare(part, singlePartTool(part), 0)
				if e != nil {
					return e
				}
				RememberToolSignature(model, AsString(tc["id"]), sig)
				if !ok {
					continue
				}
				fn := AsMap(tc["function"])
				content := map[string]any{"id": tc["id"], "name": fn["name"], "input": map[string]any{}}
				if sig != "" {
					content["signature"] = sig
				}
				if e := startBlock("tool_use", content); e != nil {
					return e
				}
				if e := delta("input_json_delta", map[string]any{"partial_json": fn["arguments"]}); e != nil {
					return e
				}
				if e := closeBlock(); e != nil {
					return e
				}
				usedTool = true
				continue
			}
			text, thought, _, _, _ := collectParts(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{part}}}}})
			if partIsThought(part) {
				if thought == "" && sig == "" {
					continue
				}
				if block != "thinking" {
					if e := startBlock("thinking", map[string]any{"thinking": ""}); e != nil {
						return e
					}
				}

				if thought != "" {
					if e := delta("thinking_delta", map[string]any{"thinking": thought}); e != nil {
						return e
					}
				}
				if sig != "" {
					pendingSig = sig
				}
				continue
			}
			if text != "" {
				if block != "text" {
					if e := startBlock("text", map[string]any{"text": ""}); e != nil {
						return e
					}
				}
				if e := delta("text_delta", map[string]any{"text": text}); e != nil {
					return e
				}
			} else if sig != "" && block == "thinking" {
				pendingSig = sig
			}
		}
		return nil
	})
	if err != nil {
		return stats, writeStreamError(dst, "claude", err)
	}

	if e := closeBlock(); e != nil {
		return stats, e
	}
	stop := "end_turn"
	if usedTool {
		stop = "tool_use"
	} else if strings.EqualFold(finish, "MAX_TOKENS") {
		stop = "max_tokens"
	}
	if e := emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": claudeUsage(usage)}); e != nil {
		return stats, e
	}
	return stats, emit("message_stop", map[string]any{})
}

func WriteGeminiSSE(dst io.Writer, model string, src io.Reader) (StreamStats, error) {
	stats, err := walkGeminiStream(src, func(p map[string]any) error { stampGeminiModel(p, model); return writeSSE(dst, p) })
	if err != nil {
		return stats, writeStreamError(dst, "gemini", err)
	}
	return stats, nil
}

// CollectGeminiJSON preserves all candidates, signatures, media and metadata.
func CollectGeminiJSON(src io.Reader) ([]byte, error) {
	out := map[string]any{}
	candidates := map[int]map[string]any{}
	total := 0
	toolRegistry := streamToolRegistry{retainParts: true}
	_, err := walkGeminiStream(src, func(p map[string]any) error {
		b, _ := json.Marshal(p)
		total += len(b)
		if total > MaxCollectedResponseBytes {
			return streamFailure("response_too_large", "upstream response exceeds collection limit")
		}
		for k, v := range p {
			if k != "candidates" {
				out[k] = v
			}
		}
		for i, v := range AsSlice(p["candidates"]) {
			c := AsMap(v)
			idx := candidateIndex(c, i)
			saved := candidates[idx]
			if saved == nil {
				saved = map[string]any{"index": idx}
				candidates[idx] = saved
			}
			for k, v := range c {
				if k != "content" {
					saved[k] = v
				}
			}
			if content := AsMap(c["content"]); content != nil {
				target := AsMap(saved["content"])
				if target == nil {
					target = map[string]any{"role": "model", "parts": []any{}}
					saved["content"] = target
				}
				for k, v := range content {
					if k != "parts" {
						target[k] = v
					}
				}
				parts := AsSlice(target["parts"])
				for _, v := range AsSlice(content["parts"]) {
					part := AsMap(v)
					if fc := AsMap(part["functionCall"]); fc != nil {
						tc, ok, err := toolRegistry.prepare(part, singlePartTool(part), idx)
						if err != nil {
							return err
						}
						if !ok {
							continue
						}
						// Stabilize IDs before non-streaming response conversion.
						fc["id"] = tc["id"]
						if fc["call_id"] != nil {
							fc["call_id"] = tc["id"]
						}
					}
					text := AsString(part["text"])
					if text != "" && len(parts) > 0 && len(part) <= 2 && partSignature(part) == "" {
						last := AsMap(parts[len(parts)-1])
						if AsString(last["text"]) != "" && len(last) <= 2 && partSignature(last) == "" && partIsThought(last) == partIsThought(part) {
							last["text"] = AsString(last["text"]) + text
							continue
						}
					}
					parts = append(parts, part)
				}
				target["parts"] = parts
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	indices := make([]int, 0, len(candidates))
	for i := range candidates {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	all := make([]any, 0, len(indices))
	for _, i := range indices {
		c := candidates[i]
		all = append(all, c)
	}
	out["candidates"] = all
	return json.Marshal(out)
}
