package convert

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

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

func WriteOpenAISSE(dst io.Writer, model string, src io.Reader) (StreamStats, error) {
	var stats StreamStats
	id := "chatcmpl-" + uuid.NewString()
	reader := bufio.NewReader(src)
	var buf bytes.Buffer
	sentRole := false
	toolIndex := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return stats, err
		}
		buf.WriteString(line)
		for {
			chunk, _, ok := takeSSE(&buf)
			if !ok {
				break
			}
			if chunk == "[DONE]" {
				continue
			}
			payload, ok := parseGeminiChunk(chunk)
			if !ok {
				continue
			}
			text, thinking, toolCalls, finish, usage := collectParts(payload)
			stats.note(text, thinking, toolCalls, usage)
			if !sentRole {
				_ = writeSSE(dst, map[string]any{
					"id": id, "object": "chat.completion.chunk", "created": nowUnix(), "model": model,
					"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}},
				})
				sentRole = true
			}
			if thinking != "" {
				_ = writeSSE(dst, map[string]any{
					"id": id, "object": "chat.completion.chunk", "created": nowUnix(), "model": model,
					"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": nil, "reasoning_content": thinking}, "finish_reason": nil}},
				})
			}
			if text != "" {
				_ = writeSSE(dst, map[string]any{
					"id": id, "object": "chat.completion.chunk", "created": nowUnix(), "model": model,
					"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}, "finish_reason": nil}},
				})
			}
			for _, tc := range toolCalls {
				m := AsMap(tc)
				fn := AsMap(m["function"])
				delta := map[string]any{
					"tool_calls": []any{map[string]any{
						"index": toolIndex,
						"id":    AsString(m["id"]),
						"type":  "function",
						"function": map[string]any{
							"name":      AsString(fn["name"]),
							"arguments": AsString(fn["arguments"]),
						},
					}},
				}
				toolIndex++
				_ = writeSSE(dst, map[string]any{
					"id": id, "object": "chat.completion.chunk", "created": nowUnix(), "model": model,
					"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
				})
			}
			if finish != "" {
				out := map[string]any{
					"id": id, "object": "chat.completion.chunk", "created": nowUnix(), "model": model,
					"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": openaiFinish(finish, toolIndex > 0)}},
				}
				if usage != nil {
					out["usage"] = usage
				}
				_ = writeSSE(dst, out)
			}
		}
		if err == io.EOF {
			break
		}
	}
	_, _ = io.WriteString(dst, "data: [DONE]\n\n")
	return stats, nil
}

func WriteClaudeSSE(dst io.Writer, model string, src io.Reader) (StreamStats, error) {
	var stats StreamStats
	msgID := "msg_" + uuid.NewString()
	reader := bufio.NewReader(src)
	var buf bytes.Buffer
	index := 0
	started := false
	textOpen := false
	thinkOpen := false
	writeEvent := func(event string, payload any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(dst, "event: %s\ndata: %s\n\n", event, b)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return stats, err
		}
		buf.WriteString(line)
		for {
			chunk, _, ok := takeSSE(&buf)
			if !ok {
				break
			}
			if chunk == "[DONE]" {
				continue
			}
			payload, ok := parseGeminiChunk(chunk)
			if !ok {
				continue
			}
			if !started {
				writeEvent("message_start", map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": msgID, "type": "message", "role": "assistant", "model": model,
						"content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
					},
				})
				started = true
			}
			text, thinking, toolCalls, finish, usage := collectParts(payload)
			stats.note(text, thinking, toolCalls, usage)
			if thinking != "" {
				if !thinkOpen {
					writeEvent("content_block_start", map[string]any{
						"type": "content_block_start", "index": index,
						"content_block": map[string]any{"type": "thinking", "thinking": ""},
					})
					thinkOpen = true
				}
				writeEvent("content_block_delta", map[string]any{
					"type": "content_block_delta", "index": index,
					"delta": map[string]any{"type": "thinking_delta", "thinking": thinking},
				})
			}
			if text != "" {
				if thinkOpen {
					writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
					thinkOpen = false
					index++
				}
				if !textOpen {
					writeEvent("content_block_start", map[string]any{
						"type": "content_block_start", "index": index,
						"content_block": map[string]any{"type": "text", "text": ""},
					})
					textOpen = true
				}
				writeEvent("content_block_delta", map[string]any{
					"type": "content_block_delta", "index": index,
					"delta": map[string]any{"type": "text_delta", "text": text},
				})
			}
			for _, tc := range toolCalls {
				if thinkOpen {
					writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
					thinkOpen = false
					index++
				}
				if textOpen {
					writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
					textOpen = false
					index++
				}
				m := AsMap(tc)
				fn := AsMap(m["function"])
				var input any = map[string]any{}
				_ = json.Unmarshal([]byte(AsString(fn["arguments"])), &input)
				writeEvent("content_block_start", map[string]any{
					"type": "content_block_start", "index": index,
					"content_block": map[string]any{"type": "tool_use", "id": AsString(m["id"]), "name": AsString(fn["name"]), "input": map[string]any{}},
				})
				writeEvent("content_block_delta", map[string]any{
					"type": "content_block_delta", "index": index,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": AsString(fn["arguments"])},
				})
				writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
				index++
			}
			if finish != "" {
				if thinkOpen {
					writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
					thinkOpen = false
				}
				if textOpen {
					writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
					textOpen = false
				}
				stop := "end_turn"
				if toolIndexGuess(toolCalls) || strings.ToUpper(finish) == "STOP" && false {
					stop = "end_turn"
				}
				if len(toolCalls) > 0 {
					stop = "tool_use"
				}
				if strings.ToUpper(finish) == "MAX_TOKENS" {
					stop = "max_tokens"
				}
				delta := map[string]any{"stop_reason": stop, "stop_sequence": nil}
				if usage != nil {
					delta["usage"] = claudeUsage(usage)
				}
				writeEvent("message_delta", map[string]any{"type": "message_delta", "delta": delta})
				writeEvent("message_stop", map[string]any{"type": "message_stop"})
			}
		}
		if err == io.EOF {
			break
		}
	}
	if started {
		if thinkOpen {
			fmt.Fprintf(dst, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", index)
		}
		if textOpen {
			fmt.Fprintf(dst, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", index)
		}
		writeEvent := func(event string, payload any) {
			b, _ := json.Marshal(payload)
			fmt.Fprintf(dst, "event: %s\ndata: %s\n\n", event, b)
		}
		writeEvent("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}})
		writeEvent("message_stop", map[string]any{"type": "message_stop"})
	}
	return stats, nil
}

func WriteGeminiSSE(dst io.Writer, model string, src io.Reader) (StreamStats, error) {
	var stats StreamStats
	reader := bufio.NewReader(src)
	var buf bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return stats, err
		}
		buf.WriteString(line)
		for {
			chunk, _, ok := takeSSE(&buf)
			if !ok {
				break
			}
			if chunk == "[DONE]" {
				continue
			}
			payload, ok := parseGeminiChunk(chunk)
			if !ok {
				continue
			}
			text, thinking, toolCalls, _, usage := collectParts(payload)
			stats.note(text, thinking, toolCalls, usage)
			stampGeminiModel(payload, model)
			b, _ := json.Marshal(payload)
			fmt.Fprintf(dst, "data: %s\n\n", b)
		}
		if err == io.EOF {
			break
		}
	}
	return stats, nil
}

func takeSSE(buf *bytes.Buffer) (string, []byte, bool) {
	data := buf.Bytes()
	idx := bytes.Index(data, []byte("\n\n"))
	if idx < 0 {
		idx = bytes.Index(data, []byte("\r\n\r\n"))
		if idx < 0 {
			return "", nil, false
		}
		block := string(data[:idx])
		rest := append([]byte(nil), data[idx+4:]...)
		buf.Reset()
		buf.Write(rest)
		return extractData(block), rest, true
	}
	block := string(data[:idx])
	rest := append([]byte(nil), data[idx+2:]...)
	buf.Reset()
	buf.Write(rest)
	return extractData(block), rest, true
}

func extractData(block string) string {
	var payload strings.Builder
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "data:") {
			if payload.Len() > 0 {
				payload.WriteByte('\n')
			}
			payload.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return payload.String()
}

func parseGeminiChunk(chunk string) (map[string]any, bool) {
	chunk = strings.TrimSpace(chunk)
	if chunk == "" || chunk == "[DONE]" {
		return nil, false
	}
	var envelope map[string]any
	if json.Unmarshal([]byte(chunk), &envelope) != nil {
		return nil, false
	}
	if r := AsMap(envelope["response"]); r != nil {
		return r, true
	}
	return envelope, true
}

func writeSSE(w io.Writer, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

func toolIndexGuess(toolCalls []any) bool {
	return len(toolCalls) > 0
}
