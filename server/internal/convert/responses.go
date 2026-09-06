package convert

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"
)

// Responses has its own lifecycle and output schema; it is not Chat Completions.
func GeminiToResponses(model string, raw []byte) ([]byte, error) {
	return GeminiToResponsesWithTools(model, raw, nil)
}
func GeminiToResponsesWithTools(model string, raw []byte, customToolNames map[string]bool) ([]byte, error) {
	p, err := decodeGeminiPayload(raw)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	sink := &terminalResponseWriter{}
	if _, err = WriteResponsesSSEWithTools(sink, model, bytes.NewReader(append(append([]byte("data: "), b...), []byte("\n\n")...)), customToolNames); err != nil {
		return nil, err
	}
	if sink.response == nil {
		return nil, streamFailure("empty_response", "missing Responses terminal event")
	}
	return json.Marshal(sink.response)
}

type terminalResponseWriter struct{ response map[string]any }

func (w *terminalResponseWriter) Write(p []byte) (int, error) {
	for _, line := range bytes.Split(p, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &event); err != nil {
			return 0, err
		}
		switch AsString(event["type"]) {
		case "response.completed", "response.incomplete", "response.failed":
			w.response = AsMap(event["response"])
		}
	}
	return len(p), nil
}

func responsesUsage(u any) map[string]any {
	m := AsMap(u)
	s := TokenUsageFromOpenAI(u)
	total := intVal(m["total_tokens"])
	if total == 0 {
		total = s.Input + s.Output
	}
	return map[string]any{"input_tokens": s.Input, "input_tokens_details": map[string]any{"cached_tokens": s.Cache}, "output_tokens": s.Output, "output_tokens_details": map[string]any{"reasoning_tokens": s.Reasoning}, "total_tokens": total}
}
func customToolInput(arguments string) string {
	var value any
	if json.Unmarshal([]byte(arguments), &value) == nil {
		if s, ok := value.(string); ok {
			return s
		}
		m := AsMap(value)
		for _, key := range []string{"input", "patch", "patch_text", "diff", "content"} {
			if v, ok := m[key].(string); ok {
				return v
			}
		}
	}
	return arguments
}

func WriteResponsesSSE(dst io.Writer, model string, src io.Reader) (StreamStats, error) {
	return WriteResponsesSSEWithTools(dst, model, src, nil)
}
func WriteResponsesSSEWithTools(dst io.Writer, model string, src io.Reader, customToolNames map[string]bool) (StreamStats, error) {
	id := "resp_" + uuid.NewString()
	created := nowUnix()
	seq := 0
	emit := func(kind string, event map[string]any) error {
		event["type"] = kind
		event["sequence_number"] = seq
		seq++
		return writeEvent(dst, kind, event)
	}
	response := map[string]any{"id": id, "object": "response", "created_at": created, "status": "in_progress", "model": model, "output": []any{}, "error": nil, "incomplete_details": nil, "usage": nil}
	if err := emit("response.created", map[string]any{"response": response}); err != nil {
		return StreamStats{}, err
	}
	if err := emit("response.in_progress", map[string]any{"response": response}); err != nil {
		return StreamStats{}, err
	}
	outputs := []any{}
	active := -1
	activeCandidate := -1
	activeType := ""
	var text strings.Builder
	var usage any
	finish := ""
	actionable := false
	var registry streamToolRegistry
	collectedBytes := 0
	finalItemIncomplete := false
	closeItem := func() error {
		if active < 0 {
			return nil
		}
		item := AsMap(outputs[active])
		kind := AsString(item["type"])
		if kind == "message" {
			content := map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}
			item["content"] = []any{content}
			if e := emit("response.output_text.done", map[string]any{"item_id": item["id"], "output_index": active, "content_index": 0, "text": text.String()}); e != nil {
				return e
			}
			if e := emit("response.content_part.done", map[string]any{"item_id": item["id"], "output_index": active, "content_index": 0, "part": content}); e != nil {
				return e
			}
		} else if kind == "reasoning" {
			summary := map[string]any{"type": "summary_text", "text": text.String()}
			item["summary"] = []any{summary}
			if e := emit("response.reasoning_summary_text.done", map[string]any{"item_id": item["id"], "output_index": active, "summary_index": 0, "text": text.String()}); e != nil {
				return e
			}
			if e := emit("response.reasoning_summary_part.done", map[string]any{"item_id": item["id"], "output_index": active, "summary_index": 0, "part": summary}); e != nil {
				return e
			}
		}
		item["status"] = "completed"
		if finalItemIncomplete {
			item["status"] = "incomplete"
		}
		if e := emit("response.output_item.done", map[string]any{"output_index": active, "item": item}); e != nil {
			return e
		}
		active = -1
		activeType = ""
		text.Reset()
		return nil
	}
	startText := func(kind string, candidate int) error {
		if active >= 0 && activeType == kind && activeCandidate == candidate {
			return nil
		}
		if e := closeItem(); e != nil {
			return e
		}
		active = len(outputs)
		activeCandidate = candidate
		activeType = kind
		item := map[string]any{"id": "msg_" + uuid.NewString(), "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
		if kind == "reasoning" {
			item = map[string]any{"id": "rs_" + uuid.NewString(), "type": "reasoning", "status": "in_progress", "summary": []any{}}
		}
		outputs = append(outputs, item)
		if e := emit("response.output_item.added", map[string]any{"output_index": active, "item": item}); e != nil {
			return e
		}
		if kind == "reasoning" {
			return emit("response.reasoning_summary_part.added", map[string]any{"item_id": item["id"], "output_index": active, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
		}
		return emit("response.content_part.added", map[string]any{"item_id": item["id"], "output_index": active, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
	}
	stats, streamErr := walkGeminiStream(src, func(p map[string]any) error {
		encoded, _ := json.Marshal(p)
		collectedBytes += len(encoded)
		if collectedBytes > MaxCollectedResponseBytes {
			return streamFailure("response_too_large", "Responses output exceeds collection limit")
		}
		if u := p["usageMetadata"]; u != nil {
			usage = geminiUsageToOpenAI(u)
		}
		for i, v := range AsSlice(p["candidates"]) {
			c := AsMap(v)
			candidate := candidateIndex(c, i)
			if f := AsString(c["finishReason"]); f != "" && (finish == "" || finish == "STOP") {
				finish = f
			}
			for _, v := range AsSlice(AsMap(c["content"])["parts"]) {
				part := AsMap(v)
				if AsMap(part["functionCall"]) != nil {
					tc, ok, e := registry.prepare(part, singlePartTool(part), candidate)
					if e != nil {
						return e
					}
					RememberToolSignature(model, AsString(tc["id"]), partSignature(part))
					if !ok {
						continue
					}
					if e := closeItem(); e != nil {
						return e
					}
					fn := AsMap(tc["function"])
					name := AsString(fn["name"])
					arguments := AsString(fn["arguments"])
					custom := customToolNames[name]
					idx := len(outputs)
					item := map[string]any{"id": "fc_" + uuid.NewString(), "type": "function_call", "status": "in_progress", "call_id": tc["id"], "name": name, "arguments": ""}
					deltaEvent, doneEvent, field := "response.function_call_arguments.delta", "response.function_call_arguments.done", "arguments"
					if custom {
						item["type"] = "custom_tool_call"
						delete(item, "arguments")
						item["input"] = ""
						arguments = customToolInput(arguments)
						deltaEvent, doneEvent, field = "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done", "input"
					}
					if sig := partSignature(part); sig != "" {
						item["signature"] = sig
					}
					outputs = append(outputs, item)
					if e := emit("response.output_item.added", map[string]any{"output_index": idx, "item": item}); e != nil {
						return e
					}
					if e := emit(deltaEvent, map[string]any{"item_id": item["id"], "output_index": idx, "delta": arguments}); e != nil {
						return e
					}
					if e := emit(doneEvent, map[string]any{"item_id": item["id"], "output_index": idx, field: arguments}); e != nil {
						return e
					}
					item[field] = arguments
					item["status"] = "completed"
					if e := emit("response.output_item.done", map[string]any{"output_index": idx, "item": item}); e != nil {
						return e
					}
					actionable = true
					continue
				}
				content, thought, _, _, _ := collectParts(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{part}}}}})
				kind, chunk := "message", content
				if partIsThought(part) {
					kind, chunk = "reasoning", thought
				}
				sig := partSignature(part)
				if chunk == "" {
					if sig != "" && active >= 0 {
						AsMap(outputs[active])["encrypted_content"] = sig
					}
					continue
				}
				if e := startText(kind, candidate); e != nil {
					return e
				}
				text.WriteString(chunk)
				item := AsMap(outputs[active])
				if sig != "" {
					item["encrypted_content"] = sig
				}
				if kind == "reasoning" {
					if e := emit("response.reasoning_summary_text.delta", map[string]any{"item_id": item["id"], "output_index": active, "summary_index": 0, "delta": chunk}); e != nil {
						return e
					}
				} else {
					actionable = true
					if e := emit("response.output_text.delta", map[string]any{"item_id": item["id"], "output_index": active, "content_index": 0, "delta": chunk}); e != nil {
						return e
					}
				}
			}
		}
		return nil
	})
	// Transport and protocol failures produce an explicit terminal event with the
	// partial output. A downstream write failure is returned without claiming success.

	status, reason := "completed", ""
	switch strings.ToUpper(finish) {
	case "MAX_TOKENS":
		status, reason = "incomplete", "max_output_tokens"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT":
		status, reason = "incomplete", "content_filter"
	}
	if streamErr != nil {
		status, reason = "incomplete", "interrupted"
	}
	if !actionable && reason == "" {
		status, reason = "incomplete", "interrupted"
		if streamErr == nil {
			streamErr = streamFailure("empty_response", "upstream returned no assistant answer or tool call")
		}
	}
	finalItemIncomplete = status != "completed"
	if e := closeItem(); e != nil {
		return stats, errors.Join(streamErr, e)
	}
	response["status"] = status
	response["output"] = outputs
	response["usage"] = responsesUsage(usage)
	if status == "completed" {
		response["completed_at"] = nowUnix()
	} else {
		response["incomplete_details"] = map[string]any{"reason": reason}
	}
	if streamErr != nil {
		response["error"] = map[string]any{"code": "upstream_interrupted", "message": streamErr.Error()}
		var upstream *UpstreamError
		if errors.As(streamErr, &upstream) {
			AsMap(response["error"])["code"] = upstream.Type
		}
	}
	if e := emit("response."+status, map[string]any{"response": response}); e != nil {
		return stats, errors.Join(streamErr, e)
	}
	return stats, streamErr
}
