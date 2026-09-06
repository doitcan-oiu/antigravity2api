package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wo/antigravity2api/internal/convert"
	"github.com/wo/antigravity2api/internal/models"
)

const maxMediaFileBytes = 16 << 20
const maxInputImages = 8

type imageAPIRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              *int   `json:"n"`
	Size           string `json:"size"`
	Quality        string `json:"quality"`
	ImageSize      string `json:"imageSize"`
	ImageSizeSnake string `json:"image_size"`
	Style          string `json:"style"`
	ResponseFormat string `json:"response_format"`
	Image          any    `json:"image"`
	Mask           any    `json:"mask"`
	Stream         bool   `json:"stream"`
}
type mediaUpload struct {
	Name, Filename, MIME string
	Data                 []byte
}

// MultipartReader bounds each field and the whole request without creating
// temporary files or downloading user-supplied URLs.
func readMediaMultipart(w http.ResponseWriter, r *http.Request) (map[string]string, []mediaUpload, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, nil, fmt.Errorf("multipart/form-data is required")
	}
	fields := map[string]string{}
	var files []mediaUpload
	for count := 0; ; count++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if count >= 32 {
			part.Close()
			return nil, nil, fmt.Errorf("too many multipart fields")
		}
		limit := int64(64 << 10)
		if part.FileName() != "" {
			limit = maxMediaFileBytes
		}
		data, readErr := io.ReadAll(io.LimitReader(part, limit+1))
		part.Close()
		if readErr != nil {
			return nil, nil, readErr
		}
		if int64(len(data)) > limit {
			return nil, nil, &http.MaxBytesError{Limit: limit}
		}
		name := part.FormName()
		if part.FileName() == "" {
			fields[name] = string(data)
			continue
		}
		files = append(files, mediaUpload{Name: name, Filename: part.FileName(), MIME: part.Header.Get("Content-Type"), Data: data})
	}
	return fields, files, nil
}
func imageMIME(mt string) bool {
	switch strings.ToLower(mt) {
	case "image/png", "image/jpeg", "image/webp":
		return true
	}
	return false
}
func uploadMIME(file mediaUpload) string {
	mt, _, _ := mime.ParseMediaType(file.MIME)
	if mt != "" && mt != "application/octet-stream" {
		return strings.ToLower(mt)
	}
	switch strings.ToLower(filepath.Ext(file.Filename)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".webm":
		return "audio/webm"
	}
	return http.DetectContentType(file.Data)
}
func validateInlineImage(source string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(source), "data:") {
		return "", fmt.Errorf("image must be an inline data URL; remote image URLs are not downloaded")
	}
	header, data, ok := strings.Cut(source[5:], ",")
	if !ok {
		return "", fmt.Errorf("invalid image data URL")
	}
	if !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", fmt.Errorf("image data URL must use base64")
	}
	mt, _, err := mime.ParseMediaType(header[:len(header)-7])
	if err != nil || !imageMIME(mt) {
		return "", fmt.Errorf("supported input image types are PNG, JPEG and WebP")
	}
	if base64.StdEncoding.DecodedLen(len(data)) > maxMediaFileBytes+2 {
		return "", &http.MaxBytesError{Limit: maxMediaFileBytes}
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) == 0 {
		return "", fmt.Errorf("image data must contain nonempty valid base64")
	}
	if len(decoded) > maxMediaFileBytes {
		return "", &http.MaxBytesError{Limit: maxMediaFileBytes}
	}
	return "data:" + strings.ToLower(mt) + ";base64," + base64.StdEncoding.EncodeToString(decoded), nil
}
func imageSources(input any) ([]string, error) {
	if input == nil {
		return nil, nil
	}
	values := []any{input}
	if a, ok := input.([]any); ok {
		values = a
	}
	if len(values) > maxInputImages {
		return nil, fmt.Errorf("at most %d input images are supported", maxInputImages)
	}
	var sources []string
	for _, v := range values {
		source := convert.AsString(v)
		if m := convert.AsMap(v); m != nil {
			source = convert.AsString(m["url"])
			if source == "" {
				source = convert.AsString(m["image_url"])
			}
			if source == "" {
				source = convert.AsString(convert.GetPath(m, "image_url", "url"))
			}
		}
		normalized, err := validateInlineImage(source)
		if err != nil {
			return nil, err
		}
		sources = append(sources, normalized)
	}
	return sources, nil
}
func parseImageAPIRequest(w http.ResponseWriter, r *http.Request, edit bool) (imageAPIRequest, error) {
	var req imageAPIRequest
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		fields, files, err := readMediaMultipart(w, r)
		if err != nil {
			return req, err
		}
		req.Model, req.Prompt, req.Size, req.Quality, req.Style, req.ResponseFormat = fields["model"], fields["prompt"], fields["size"], fields["quality"], fields["style"], fields["response_format"]
		req.ImageSize, req.ImageSizeSnake = fields["imageSize"], fields["image_size"]
		if fields["n"] != "" {
			n, err := strconv.Atoi(fields["n"])
			if err != nil {
				return req, fmt.Errorf("n must be 1")
			}
			req.N = &n
		}
		if fields["stream"] != "" {
			value, err := strconv.ParseBool(fields["stream"])
			if err != nil {
				return req, fmt.Errorf("stream must be boolean")
			}
			req.Stream = value
		}
		if fields["mask"] != "" {
			req.Mask = fields["mask"]
		}
		var images []any
		for _, file := range files {
			if file.Name == "mask" {
				return req, fmt.Errorf("mask-based editing is not supported; supply an image and editing instructions")
			}
			if file.Name != "image" && file.Name != "image[]" {
				return req, fmt.Errorf("unsupported file field %q", file.Name)
			}
			mt := uploadMIME(file)
			if !imageMIME(mt) || len(file.Data) == 0 {
				return req, fmt.Errorf("input images must be nonempty PNG, JPEG or WebP files")
			}
			images = append(images, "data:"+mt+";base64,"+base64.StdEncoding.EncodeToString(file.Data))
		}
		if len(images) > 0 {
			req.Image = images
		} else if fields["image"] != "" {
			req.Image = fields["image"]
		}
	} else if err := readJSON(r, &req); err != nil {
		return req, err
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return req, fmt.Errorf("prompt is required")
	}
	if req.N != nil && *req.N != 1 {
		return req, fmt.Errorf("this Images endpoint supports n=1; request additional images separately")
	}
	if req.Stream {
		return req, fmt.Errorf("Images API streaming is not supported")
	}
	if req.Mask != nil {
		return req, fmt.Errorf("mask-based editing is not supported; supply an image and editing instructions")
	}
	if req.ResponseFormat == "" {
		req.ResponseFormat = "b64_json"
	}
	if req.ResponseFormat != "b64_json" && req.ResponseFormat != "url" {
		return req, fmt.Errorf("response_format must be b64_json or url")
	}
	if req.ImageSize == "" {
		req.ImageSize = req.ImageSizeSnake
	}
	req.ImageSize = strings.ToUpper(req.ImageSize)
	switch req.Quality {
	case "", "auto", "standard", "hd", "low", "medium", "high":
	default:
		return req, fmt.Errorf("unsupported image quality")
	}
	if req.ImageSize != "" && req.ImageSize != "1K" && req.ImageSize != "2K" && req.ImageSize != "4K" {
		return req, fmt.Errorf("imageSize must be 1K, 2K or 4K")
	}
	if req.Size != "" && req.Size != "auto" {
		var width, height int
		if _, err := fmt.Sscanf(req.Size, "%dx%d", &width, &height); err != nil || width < 1 || height < 1 || req.Size != fmt.Sprintf("%dx%d", width, height) {
			return req, fmt.Errorf("size must be auto or WIDTHxHEIGHT")
		}
	}
	sources, err := imageSources(req.Image)
	if err != nil {
		return req, err
	}
	if edit && len(sources) == 0 {
		return req, fmt.Errorf("image editing requires at least one input image")
	}
	req.Image = sources
	if req.Model == "" || req.Model == "dall-e-2" || req.Model == "dall-e-3" || req.Model == "gpt-image-1" || req.Model == "gpt-image-1-mini" {
		req.Model = "gemini-3.1-flash-image"
	}
	if !convert.IsImageModel(convert.MapModel(req.Model)) {
		return req, fmt.Errorf("an image generation model is required")
	}
	return req, nil
}
func imageOpenAIRequest(req imageAPIRequest) convert.OpenAIRequest {
	prompt := req.Prompt
	if req.Style != "" {
		prompt += "\nRequested visual style: " + req.Style
	}
	content := []any{map[string]any{"type": "text", "text": prompt}}
	for _, source := range req.Image.([]string) {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": source}})
	}
	one := 1
	return convert.OpenAIRequest{Model: req.Model, Messages: []convert.OpenAIMessage{{Role: "user", Content: content}}, N: &one, Size: req.Size, Quality: req.Quality, ImageSize: req.ImageSize}
}
func (s *Server) imagesGenerations(w http.ResponseWriter, r *http.Request) {
	s.serveImages(w, r, false)
}
func (s *Server) imagesEdits(w http.ResponseWriter, r *http.Request) { s.serveImages(w, r, true) }
func (s *Server) serveImages(w http.ResponseWriter, r *http.Request, edit bool) {
	req, err := parseImageAPIRequest(w, r, edit)
	if err != nil {
		writeRequestError(w, "openai", err)
		return
	}
	openai := imageOpenAIRequest(req)
	s.serveMedia(w, r, openai, "images", "", func(_ string, raw []byte) ([]byte, error) { return imagesResponseJSON(raw, req.ResponseFormat) })
}
func imagesResponseJSON(raw []byte, format string) ([]byte, error) {
	data, err := mediaResponsePayload(raw)
	if err != nil {
		return nil, err
	}
	images := []any{}
	for _, v := range convert.AsSlice(data["candidates"]) {
		c := convert.AsMap(v)
		for _, v := range convert.AsSlice(convert.GetPath(c, "content", "parts")) {
			p := convert.AsMap(v)
			inline := convert.AsMap(p["inlineData"])
			if inline == nil {
				inline = convert.AsMap(p["inline_data"])
			}
			encoded := convert.AsString(inline["data"])
			if encoded == "" {
				continue
			}
			mt := convert.AsString(inline["mimeType"])
			if mt == "" {
				mt = convert.AsString(inline["mime_type"])
			}
			if mt == "" {
				mt = "image/png"
			}
			if !strings.HasPrefix(mt, "image/") {
				continue
			}
			// Validate encoding without decoding another large copy of the image.
			if n, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))); err != nil || n == 0 {
				return nil, fmt.Errorf("upstream returned invalid image data")
			}
			if format == "url" {
				images = append(images, map[string]any{"url": "data:" + mt + ";base64," + encoded})
			} else {
				images = append(images, map[string]any{"b64_json": encoded})
			}
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("upstream returned no generated image")
	}
	// n=1 is the advertised contract even if an upstream candidate includes previews.
	return json.Marshal(map[string]any{"created": time.Now().Unix(), "data": images[:1]})
}
func mediaResponsePayload(raw []byte) (map[string]any, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("empty upstream response")
	}
	if data["error"] != nil {
		return nil, mediaUpstreamError(data["error"])
	}
	if inner := convert.AsMap(data["response"]); inner != nil {
		data = inner
	}
	if data["error"] != nil {
		return nil, mediaUpstreamError(data["error"])
	}
	return data, nil
}
func mediaUpstreamError(value any) error {
	m := convert.AsMap(value)
	code := 502
	switch v := m["code"].(type) {
	case float64:
		code = int(v)
	case string:
		if parsed, err := strconv.Atoi(v); err == nil {
			code = parsed
		}
	}
	if code < 400 || code > 599 {
		code = 502
	}
	message := convert.AsString(m["message"])
	if message == "" {
		message = "upstream media request failed"
	}
	return &convert.UpstreamError{Code: code, Type: "upstream_error", Message: message}
}

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request, req convert.OpenAIRequest, protocol, contentType string, toJSON toJSONFn) {
	original := req.Model
	used, mixed := s.routeModel(original)
	req.Model = used
	target := convert.ResolveModel(used, nil, "").Model
	session := requestSession(r, original, req.Messages)
	s.proxy(w, r, proxyPlan{protocol: protocol, model: original, target: target, session: session, mixed: mixed, direct: true, contentType: contentType,
		build: func(a *models.Account, final string) (any, error) {
			outer, _, _ := convert.OpenAIToGeminiWithModel(req, a.ProjectID, a.Email, a.ID, final)
			return setSession(outer, session), nil
		}, toJSON: toJSON})
}

func audioUploadFormat(mt string) (string, bool) {
	switch mt {
	case "audio/wav", "audio/x-wav":
		return "wav", true
	case "audio/mpeg", "audio/mp3":
		return "mp3", true
	case "audio/mp4", "audio/x-m4a", "video/mp4":
		return "m4a", true
	case "audio/ogg", "application/ogg":
		return "ogg", true
	case "audio/flac", "audio/x-flac":
		return "flac", true
	case "audio/webm", "video/webm":
		return "audio/webm", true
	}
	return "", false
}
func (s *Server) audioTranscriptions(w http.ResponseWriter, r *http.Request) {
	fields, files, err := readMediaMultipart(w, r)
	if err != nil {
		writeRequestError(w, "openai", err)
		return
	}
	if len(files) != 1 || files[0].Name != "file" || len(files[0].Data) == 0 {
		writeProxyError(w, "openai", 400, "one nonempty audio file is required")
		return
	}
	format, ok := audioUploadFormat(uploadMIME(files[0]))
	if !ok {
		writeProxyError(w, "openai", 400, "supported audio formats are WAV, MP3, M4A/MP4, OGG, FLAC and WebM")
		return
	}
	responseFormat := fields["response_format"]
	if responseFormat == "" {
		responseFormat = "json"
	}
	if responseFormat != "json" && responseFormat != "text" {
		writeProxyError(w, "openai", 400, "transcriptions support json or text; timestamped formats are not available")
		return
	}
	if fields["timestamp_granularities[]"] != "" || fields["timestamp_granularities"] != "" {
		writeProxyError(w, "openai", 400, "timestamped or streaming transcriptions are not supported")
		return
	}
	if value := fields["stream"]; value != "" {
		stream, err := strconv.ParseBool(value)
		if err != nil || stream {
			writeProxyError(w, "openai", 400, "streaming transcriptions are not supported")
			return
		}
	}
	model := fields["model"]
	switch model {
	case "", "whisper-1", "gpt-4o-transcribe", "gpt-4o-mini-transcribe":
		model = "gemini-2.5-flash"
	}
	if convert.IsImageModel(convert.MapModel(model)) {
		writeProxyError(w, "openai", 400, "an audio-capable text model is required")
		return
	}
	prompt := "Transcribe the speech in the supplied audio accurately in its original language. Return only the transcript, without commentary or invented timestamps."
	if fields["language"] != "" {
		prompt += "\nExpected language: " + fields["language"]
	}
	if fields["prompt"] != "" {
		prompt += "\nContext and spelling hints: " + fields["prompt"]
	}
	content := []any{map[string]any{"type": "text", "text": prompt}, map[string]any{"type": "input_audio", "input_audio": map[string]any{"format": format, "data": base64.StdEncoding.EncodeToString(files[0].Data)}}}
	req := convert.OpenAIRequest{Model: model, Messages: []convert.OpenAIMessage{{Role: "user", Content: content}}}
	if fields["temperature"] != "" {
		temperature, err := strconv.ParseFloat(fields["temperature"], 64)
		if err != nil || math.IsNaN(temperature) || math.IsInf(temperature, 0) || temperature < 0 || temperature > 1 {
			writeProxyError(w, "openai", 400, "temperature must be between 0 and 1")
			return
		}
		req.Temperature = &temperature
	}
	contentType := ""
	if responseFormat == "text" {
		contentType = "text/plain; charset=utf-8"
	}
	s.serveMedia(w, r, req, "audio", contentType, func(_ string, raw []byte) ([]byte, error) { return transcriptionResponse(raw, responseFormat) })
}
func transcriptionResponse(raw []byte, format string) ([]byte, error) {
	data, err := mediaResponsePayload(raw)
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	candidates := convert.AsSlice(data["candidates"])
	if len(candidates) > 0 {
		for _, v := range convert.AsSlice(convert.GetPath(convert.AsMap(candidates[0]), "content", "parts")) {
			part := convert.AsMap(v)
			if thought, ok := part["thought"].(bool); ok && thought {
				continue
			}
			text.WriteString(convert.AsString(part["text"]))
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return nil, fmt.Errorf("upstream returned no transcript")
	}
	if format == "text" {
		return []byte(text.String()), nil
	}
	return json.Marshal(map[string]any{"text": text.String()})
}
