package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func TestImageMediaTypesAcceptedAcrossProviders(t *testing.T) {
	cases := []struct {
		name      string
		mediaType string
		supported bool
	}{
		{"png standard", "image/png", true},
		{"jpeg standard", "image/jpeg", true},
		{"gif standard", "image/gif", true},
		{"webp standard", "image/webp", true},
		{"png uppercase", "IMAGE/PNG", true},
		{"jpeg with whitespace", "  image/jpeg  ", true},
		{"svg unsupported", "image/svg+xml", false},
		{"bmp unsupported", "image/bmp", false},
		{"tiff unsupported", "image/tiff", false},
		{"pdf unsupported", "application/pdf", false},
		{"plain text unsupported", "text/plain", false},
		{"empty string unsupported", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SupportedImageMediaType(tc.mediaType)
			if got != tc.supported {
				t.Errorf("SupportedImageMediaType(%q) = %v, want %v", tc.mediaType, got, tc.supported)
			}

			// Validation of a message carrying this media type must reject unsupported types.
			msg := Message{
				Role:    RoleUser,
				Content: []Block{ImageBlock(tc.mediaType, []byte("fake-bytes"))},
			}
			err := ValidateMessageImages(msg)
			if tc.supported && err != nil {
				t.Errorf("ValidateMessageImages(%q) unexpected error: %v", tc.mediaType, err)
			}
			if !tc.supported {
				if err == nil {
					t.Fatalf("ValidateMessageImages(%q) expected error, got nil", tc.mediaType)
				}
				if !strings.Contains(err.Error(), tc.mediaType) {
					t.Errorf("error %q does not name the refused media type %q", err.Error(), tc.mediaType)
				}
			}
		})
	}
}

func TestBedrockImageFormatMapping(t *testing.T) {
	cases := []struct {
		name      string
		mediaType string
		want      types.ImageFormat
		wantErr   bool
	}{
		{"png", "image/png", types.ImageFormatPng, false},
		{"jpeg", "image/jpeg", types.ImageFormatJpeg, false},
		{"jpg alias", "image/jpg", types.ImageFormatJpeg, false},
		{"gif", "image/gif", types.ImageFormatGif, false},
		{"webp", "image/webp", types.ImageFormatWebp, false},
		{"uppercase png", "IMAGE/PNG", types.ImageFormatPng, false},
		{"svg has no bedrock format", "image/svg+xml", "", true},
		{"bmp has no bedrock format", "image/bmp", "", true},
		{"pdf has no bedrock format", "application/pdf", "", true},
		{"empty has no bedrock format", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bedrockImageFormat(tc.mediaType)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("bedrockImageFormat(%q) expected error, got %v", tc.mediaType, got)
				}
				if !strings.Contains(err.Error(), tc.mediaType) {
					t.Errorf("error %q does not name media type %q", err.Error(), tc.mediaType)
				}
				return
			}
			if err != nil {
				t.Fatalf("bedrockImageFormat(%q) unexpected error: %v", tc.mediaType, err)
			}
			if got != tc.want {
				t.Errorf("bedrockImageFormat(%q) = %q, want %q", tc.mediaType, got, tc.want)
			}
		})
	}
}

func TestMessageOverTenMegabytesIsRefused(t *testing.T) {
	// A single image exceeding 10MB.
	singleBig := Message{
		Role: RoleUser,
		Content: []Block{
			TextBlock("prompt"),
			ImageBlock("image/png", make([]byte, MaxImageBytesPerMessage+1)),
		},
	}
	err := ValidateMessageImages(singleBig)
	if err == nil {
		t.Fatal("ValidateMessageImages expected error for single image > 10MB, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "10MB") {
		t.Errorf("error %q does not name the 10MB limit", errMsg)
	}
	if !strings.Contains(errMsg, fmt.Sprintf("%d", MaxImageBytesPerMessage+1)) {
		t.Errorf("error %q does not name the carried size (%d bytes)", errMsg, MaxImageBytesPerMessage+1)
	}

	// Multiple images that individually are under 10MB, but together exceed 10MB.
	twoImages := Message{
		Role: RoleUser,
		Content: []Block{
			ImageBlock("image/png", make([]byte, 6*1024*1024)),
			ImageBlock("image/jpeg", make([]byte, 5*1024*1024)),
		},
	}
	err = ValidateMessageImages(twoImages)
	if err == nil {
		t.Fatal("ValidateMessageImages expected error for multiple images summing > 10MB, got nil")
	}
	errMsg = err.Error()
	expectedCarried := 11 * 1024 * 1024
	if !strings.Contains(errMsg, fmt.Sprintf("%d", expectedCarried)) {
		t.Errorf("error %q does not name total carried size (%d bytes)", errMsg, expectedCarried)
	}

	// Adapters refuse it via Chat before making network calls.
	req := ChatRequest{
		Model:    "test-model",
		Messages: []Message{singleBig},
	}
	anth := NewAnthropicDirect("key")
	if _, err := anth.Chat(context.Background(), req); err == nil {
		t.Error("AnthropicDirect.Chat accepted request over 10MB")
	}
	oa := NewOpenAICompat("http://127.0.0.1:9999", "key")
	if _, err := oa.Chat(context.Background(), req); err == nil {
		t.Error("OpenAICompat.Chat accepted request over 10MB")
	}
	bed := NewBedrock("us-east-1", "")
	if _, err := bed.Chat(context.Background(), req); err == nil {
		t.Error("Bedrock.Chat accepted request over 10MB")
	}
}

func TestMessageUnderTenMegabytesIsAccepted(t *testing.T) {
	msg := Message{
		Role: RoleUser,
		Content: []Block{
			TextBlock("two screenshots"),
			ImageBlock("image/png", make([]byte, 2*1024*1024)),
			ImageBlock("image/webp", make([]byte, 3*1024*1024)),
		},
	}
	if err := ValidateMessageImages(msg); err != nil {
		t.Fatalf("ValidateMessageImages refused message under 10MB: %v", err)
	}

	req := ChatRequest{
		Model:    "test-model",
		Messages: []Message{msg},
	}
	if err := ValidateRequestImages(req); err != nil {
		t.Fatalf("ValidateRequestImages refused request under 10MB: %v", err)
	}
}

func TestAnthropicAdapterBuildsImageBlock(t *testing.T) {
	rawBytes := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	msgs := []Message{
		{
			Role: RoleUser,
			Content: []Block{
				TextBlock("inspect this image"),
				ImageBlock("image/png", rawBytes),
			},
		},
	}

	anthMsgs := toAnthropicMessages(msgs)
	if len(anthMsgs) != 1 {
		t.Fatalf("toAnthropicMessages returned %d messages, want 1", len(anthMsgs))
	}
	if anthMsgs[0].Role != "user" {
		t.Errorf("role = %q, want user", anthMsgs[0].Role)
	}
	if len(anthMsgs[0].Content) != 2 {
		t.Fatalf("content blocks count = %d, want 2", len(anthMsgs[0].Content))
	}

	// First block is text.
	if anthMsgs[0].Content[0].Type != "text" || anthMsgs[0].Content[0].Text != "inspect this image" {
		t.Errorf("block 0 = %+v, want text block", anthMsgs[0].Content[0])
	}

	// Second block is image.
	imgBlock := anthMsgs[0].Content[1]
	if imgBlock.Type != "image" {
		t.Fatalf("block 1 type = %q, want image", imgBlock.Type)
	}
	if imgBlock.Source == nil {
		t.Fatal("block 1 Source is nil")
	}
	if imgBlock.Source.Type != "base64" {
		t.Errorf("source type = %q, want base64", imgBlock.Source.Type)
	}
	if imgBlock.Source.MediaType != "image/png" {
		t.Errorf("source media_type = %q, want image/png", imgBlock.Source.MediaType)
	}
	expectedB64 := base64.StdEncoding.EncodeToString(rawBytes)
	if imgBlock.Source.Data != expectedB64 {
		t.Errorf("source data = %q, want %q", imgBlock.Source.Data, expectedB64)
	}

	// Assert built wire request JSON serialization.
	wireBytes, err := json.Marshal(anthMsgs[0])
	if err != nil {
		t.Fatalf("marshal anthropic message: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(wireBytes, &decoded); err != nil {
		t.Fatalf("unmarshal wire message: %v", err)
	}
	contentArr, ok := decoded["content"].([]any)
	if !ok || len(contentArr) != 2 {
		t.Fatalf("wire content = %+v, want 2 items", decoded["content"])
	}
	imgMap, ok := contentArr[1].(map[string]any)
	if !ok || imgMap["type"] != "image" {
		t.Fatalf("wire content[1] = %+v, want type: image", contentArr[1])
	}
	srcMap, ok := imgMap["source"].(map[string]any)
	if !ok {
		t.Fatalf("wire content[1].source = %+v, want object", imgMap["source"])
	}
	if srcMap["type"] != "base64" || srcMap["media_type"] != "image/png" || srcMap["data"] != expectedB64 {
		t.Errorf("wire source = %+v, want base64 image/png data", srcMap)
	}
}

func TestOpenAIAdapterBuildsImageURLBlock(t *testing.T) {
	rawBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	msgs := []Message{
		{
			Role: RoleUser,
			Content: []Block{
				TextBlock("look at this photo"),
				ImageBlock("image/jpeg", rawBytes),
			},
		},
	}

	oaMsgs := toOpenAIMessages("", msgs)
	if len(oaMsgs) != 1 {
		t.Fatalf("toOpenAIMessages returned %d messages, want 1", len(oaMsgs))
	}
	if oaMsgs[0].Role != "user" {
		t.Errorf("role = %q, want user", oaMsgs[0].Role)
	}
	if len(oaMsgs[0].MultiContent) != 2 {
		t.Fatalf("MultiContent count = %d, want 2", len(oaMsgs[0].MultiContent))
	}

	expectedB64 := base64.StdEncoding.EncodeToString(rawBytes)
	expectedDataURI := "data:image/jpeg;base64," + expectedB64

	// Part 0 is text.
	if oaMsgs[0].MultiContent[0].Type != "text" || oaMsgs[0].MultiContent[0].Text != "look at this photo" {
		t.Errorf("part 0 = %+v, want text part", oaMsgs[0].MultiContent[0])
	}

	// Part 1 is image_url.
	imgPart := oaMsgs[0].MultiContent[1]
	if imgPart.Type != "image_url" {
		t.Fatalf("part 1 type = %q, want image_url", imgPart.Type)
	}
	if imgPart.ImageURL == nil || imgPart.ImageURL.URL != expectedDataURI {
		t.Errorf("part 1 image_url = %+v, want URL %q", imgPart.ImageURL, expectedDataURI)
	}

	// Assert built wire request JSON serialization.
	body := oaRequest{
		Model:    "gpt-4o",
		Messages: oaMsgs,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal oaRequest: %v", err)
	}

	var wireMap struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text,omitempty"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url,omitempty"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(payload, &wireMap); err != nil {
		t.Fatalf("unmarshal built wire request: %v", err)
	}
	if len(wireMap.Messages) != 1 {
		t.Fatalf("wire messages count = %d, want 1", len(wireMap.Messages))
	}
	wireMsg := wireMap.Messages[0]
	if len(wireMsg.Content) != 2 {
		t.Fatalf("wire content parts = %d, want 2", len(wireMsg.Content))
	}
	if wireMsg.Content[0].Type != "text" || wireMsg.Content[0].Text != "look at this photo" {
		t.Errorf("wire part 0 = %+v, want text", wireMsg.Content[0])
	}
	if wireMsg.Content[1].Type != "image_url" || wireMsg.Content[1].ImageURL == nil || wireMsg.Content[1].ImageURL.URL != expectedDataURI {
		t.Errorf("wire part 1 = %+v, want image_url with %q", wireMsg.Content[1], expectedDataURI)
	}
}

func TestBedrockAdapterBuildsImageBlock(t *testing.T) {
	rawBytes := []byte("RIFFxxxxWEBPVP8 ")
	msgs := []Message{
		{
			Role: RoleUser,
			Content: []Block{
				TextBlock("describe this"),
				ImageBlock("image/webp", rawBytes),
			},
		},
	}

	bedrockMsgs, err := toBedrockMessages(msgs)
	if err != nil {
		t.Fatalf("toBedrockMessages: %v", err)
	}
	if len(bedrockMsgs) != 1 {
		t.Fatalf("toBedrockMessages returned %d messages, want 1", len(bedrockMsgs))
	}
	if bedrockMsgs[0].Role != types.ConversationRoleUser {
		t.Errorf("role = %v, want %v", bedrockMsgs[0].Role, types.ConversationRoleUser)
	}
	if len(bedrockMsgs[0].Content) != 2 {
		t.Fatalf("content blocks count = %d, want 2", len(bedrockMsgs[0].Content))
	}

	// Block 0: text block.
	tb, ok := bedrockMsgs[0].Content[0].(*types.ContentBlockMemberText)
	if !ok || tb.Value != "describe this" {
		t.Errorf("block 0 = %T (%+v), want TextBlock 'describe this'", bedrockMsgs[0].Content[0], bedrockMsgs[0].Content[0])
	}

	// Block 1: image block.
	ib, ok := bedrockMsgs[0].Content[1].(*types.ContentBlockMemberImage)
	if !ok {
		t.Fatalf("block 1 = %T, want *types.ContentBlockMemberImage", bedrockMsgs[0].Content[1])
	}
	if ib.Value.Format != types.ImageFormatWebp {
		t.Errorf("image format = %q, want %q", ib.Value.Format, types.ImageFormatWebp)
	}
	src, ok := ib.Value.Source.(*types.ImageSourceMemberBytes)
	if !ok {
		t.Fatalf("image source = %T, want *types.ImageSourceMemberBytes", ib.Value.Source)
	}
	if string(src.Value) != string(rawBytes) {
		t.Errorf("image bytes = %v, want %v", src.Value, rawBytes)
	}
}

func TestVisionRefusalClassifierDistinguishesRefusals(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		isRefusal bool
	}{
		// Positive cases: model/endpoint refuses image/vision.
		{"bedrock does not support image input", errors.New("ValidationException: The model does not support image input"), true},
		{"bedrock converse model unsupported", errors.New("ValidationException: Bedrock Converse does not support image input for model anthropic.claude-v2"), true},
		{"bedrock content block image unsupported", errors.New("ValidationException: ContentBlock: image is not supported for this model"), true},
		{"bedrock image input not supported", errors.New("ValidationException: Image input is not supported for this model"), true},
		{"anthropic image input unsupported", errors.New("invalid_request_error: messages.0.content.1.image: Image input is only supported for Claude 3 and later models"), true},
		{"anthropic model no image", errors.New("invalid_request_error: This model does not support image input"), true},
		{"openai image_url unsupported", errors.New("400 BadRequestError: Invalid content type. image_url is only supported by certain models."), true},
		{"openai gpt-3.5 does not support image", errors.New("400 BadRequestError: Model 'gpt-3.5-turbo' does not support image inputs"), true},
		{"openai vision not supported", errors.New("400 Bad Request: Model does not support vision"), true},
		{"openai vision disabled", errors.New("400 Bad Request: vision is not enabled on this model"), true},
		{"local server expects string content", errors.New("Invalid value for 'content': expected a string, got an array of objects"), true},
		{"local server content must be a string", errors.New("400 Bad Request: content must be a string"), true},
		{"local server content should be string", errors.New("'content' should be a string"), true},
		{"ollama model does not support multimodal", errors.New("model does not support multimodal inputs"), true},
		{"vllm multimodal input not supported", errors.New("ValueError: Multimodal input is not supported for model llama-2"), true},

		// Negative cases: MUST NOT be classified as vision refusals.
		{"nil error", nil, false},
		{"rate limit exceeded", errors.New("429 Too Many Requests: Rate limit exceeded"), false},
		{"rate limit error code", errors.New("RateLimitError: rate_limit reached"), false},
		{"bedrock throttling", errors.New("ThrottlingException: Rate exceeded"), false},
		{"context length tokens exceeded", errors.New("400 BadRequestError: This model's maximum context length is 8192 tokens, however you requested 9000 tokens"), false},
		{"context length exceeded code", errors.New("context_length_exceeded: prompt exceeds model context window"), false},
		{"token limit exceeded", errors.New("ValidationException: token limit exceeded for this model"), false},
		{"context canceled", context.Canceled, false},
		{"context deadline exceeded", context.DeadlineExceeded, false},
		{"aws request canceled", &aws.RequestCanceledError{Err: context.Canceled}, false},
		{"wrapped cancellation", fmt.Errorf("do request: %w", context.Canceled), false},
		{"unauthorized", errors.New("401 Unauthorized: Invalid API key"), false},
		{"access denied", errors.New("AccessDeniedException: You don't have access to the model"), false},
		{"expired token", errors.New("ExpiredTokenException: The security token included in the request is expired"), false},
		{"invalid model identifier", errors.New("ValidationException: The provided model identifier is invalid"), false},
		{"internal server error", errors.New("500 Internal Server Error"), false},
		{"network reset", errors.New("connection reset by peer"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isVisionRefusal(tc.err)
			if got != tc.isRefusal {
				t.Errorf("isVisionRefusal(%v) = %v, want %v", tc.err, got, tc.isRefusal)
			}

			wrapped := wrapVisionRefusal(tc.err, "my-model")
			if tc.isRefusal {
				if wrapped == nil {
					t.Fatal("wrapVisionRefusal returned nil for refusal")
				}
				s := wrapped.Error()
				if !strings.Contains(s, "my-model") {
					t.Errorf("wrapped error %q does not name model %q", s, "my-model")
				}
				if !strings.Contains(s, "appears not to accept images") {
					t.Errorf("wrapped error %q missing 'appears not to accept images'", s)
				}
				if !strings.Contains(s, "/model") {
					t.Errorf("wrapped error %q missing '/model' hint", s)
				}
			} else {
				// For non-refusal errors, error must be returned unmodified.
				if tc.err == nil {
					if wrapped != nil {
						t.Errorf("wrapVisionRefusal(nil) = %v, want nil", wrapped)
					}
				} else if wrapped != tc.err {
					t.Errorf("wrapVisionRefusal for non-refusal returned %v, want original %v", wrapped, tc.err)
				}
			}
		})
	}
}

func TestExistingProviderRequestsWithoutImagesAreUnchanged(t *testing.T) {
	// 1. Anthropic message serialization without images.
	textOnly := []Message{
		{Role: RoleUser, Content: []Block{TextBlock("hello world")}},
		{Role: RoleAssistant, Content: []Block{TextBlock("hello there")}},
	}
	anthMsgs := toAnthropicMessages(textOnly)
	anthJSON, err := json.Marshal(anthMsgs)
	if err != nil {
		t.Fatalf("marshal anthropic: %v", err)
	}
	if strings.Contains(string(anthJSON), `"source"`) {
		t.Errorf("text-only anthropic messages contain unexpected source field: %s", anthJSON)
	}

	// 2. OpenAI message serialization without images.
	oaMsgs := toOpenAIMessages("be helpful", textOnly)
	oaJSON, err := json.Marshal(oaMsgs)
	if err != nil {
		t.Fatalf("marshal openai: %v", err)
	}
	// Wire content must be a plain string: "content":"hello world".
	expectedUser := `{"role":"user","content":"hello world"}`
	if !strings.Contains(string(oaJSON), expectedUser) {
		t.Errorf("text-only openai wire output does not match expected plain string:\ngot:  %s\nwant containing: %s", oaJSON, expectedUser)
	}

	// 3. Bedrock message serialization without images.
	bedrockMsgs, err := toBedrockMessages(textOnly)
	if err != nil {
		t.Fatalf("toBedrockMessages: %v", err)
	}
	if len(bedrockMsgs) != 2 {
		t.Fatalf("bedrock messages count = %d, want 2", len(bedrockMsgs))
	}
	if _, ok := bedrockMsgs[0].Content[0].(*types.ContentBlockMemberText); !ok {
		t.Errorf("bedrock msg[0].Content[0] is not text block: %T", bedrockMsgs[0].Content[0])
	}
}
