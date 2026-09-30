package chat_completions

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToAntigravityToolImages(t *testing.T) {
	const pngData = "aW1hZ2U="
	tests := []struct {
		name       string
		content    string
		wantResult string
		wantMIMEs  []string
		wantData   []string
	}{
		{
			name:       "image only",
			content:    `[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]`,
			wantResult: "{}",
			wantMIMEs:  []string{"image/png"},
			wantData:   []string{pngData},
		},
		{
			name:       "mixed text and images preserve non-image blocks",
			content:    `[{"type":"text","text":"before\n\"image\""},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,Zmlyc3Q=","detail":"high"}},{"type":"text","text":"after"},{"type":"image_url","image_url":{"url":"data:image/webp;base64,c2Vjb25k"}},{"type":"custom","value":{"ok":true}}]`,
			wantResult: `[{"type":"text","text":"before\n\"image\""},{"type":"text","text":"after"},{"type":"custom","value":{"ok":true}}]`,
			wantMIMEs:  []string{"image/jpeg", "image/webp"},
			wantData:   []string{"Zmlyc3Q=", "c2Vjb25k"},
		},
		{
			name:       "data URL metadata parameters",
			content:    `[{"type":"image_url","image_url":{"url":"data:image/png;name=screenshot.png;base64,aW1hZ2U="}}]`,
			wantResult: "{}",
			wantMIMEs:  []string{"image/png"},
			wantData:   []string{pngData},
		},
		{
			name:       "case insensitive data URL encoding",
			content:    `[{"type":"image_url","image_url":{"url":"DATA:image/png;BASE64,aW1hZ2U="}}]`,
			wantResult: "{}",
			wantMIMEs:  []string{"image/png"},
			wantData:   []string{pngData},
		},
		{
			name:       "JSON string remains a string",
			content:    `"{\"key\":\"value\",\"items\":[1,2,3]}"`,
			wantResult: `{"key":"value","items":[1,2,3]}`,
		},
		{
			name:       "serialized content array remains a string",
			content:    `"[{\"type\":\"image_url\",\"image_url\":{\"url\":\"data:image/png;base64,aW1hZ2U=\"}}]"`,
			wantResult: `[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]`,
		},
		{
			name:       "text array preserves formatting",
			content:    `[ {"type":"text", "text":"hello"}, {"type":"text", "text":"world"} ]`,
			wantResult: `[ {"type":"text", "text":"hello"}, {"type":"text", "text":"world"} ]`,
		},
		{
			name:       "empty array",
			content:    `[]`,
			wantResult: `[]`,
		},
		{
			name:       "empty string",
			content:    `""`,
			wantResult: `{}`,
		},
		{
			name:       "null content",
			content:    `null`,
			wantResult: `{}`,
		},
		{
			name:       "remote URL remains unchanged",
			content:    `[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]`,
			wantResult: `[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]`,
		},
		{
			name:       "non-base64 data URL remains unchanged",
			content:    `[{"type":"image_url","image_url":{"url":"data:image/png,hello"}}]`,
			wantResult: `[{"type":"image_url","image_url":{"url":"data:image/png,hello"}}]`,
		},
		{
			name:       "empty image payload remains unchanged",
			content:    `[{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}]`,
			wantResult: `[{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}]`,
		},
		{
			name:       "missing image URL remains unchanged",
			content:    `[{"type":"image_url","image_url":{}}]`,
			wantResult: `[{"type":"image_url","image_url":{}}]`,
		},
		{
			name:       "non-image MIME remains unchanged",
			content:    `[{"type":"image_url","image_url":{"url":"data:application/pdf;base64,aW1hZ2U="}}]`,
			wantResult: `[{"type":"image_url","image_url":{"url":"data:application/pdf;base64,aW1hZ2U="}}]`,
		},
		{
			name:       "unsupported image retained beside inline image",
			content:    `[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]`,
			wantResult: `[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]`,
			wantMIMEs:  []string{"image/png"},
			wantData:   []string{pngData},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := toolImageRequest(tt.content)
			out := ConvertOpenAIRequestToAntigravity("gemini-3-flash", input, false)
			response := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse")
			if got := response.Get("id").String(); got != "call_image" {
				t.Fatalf("functionResponse.id = %q, want call_image", got)
			}
			if got := response.Get("name").String(); got != "view_image" {
				t.Fatalf("functionResponse.name = %q, want view_image", got)
			}
			result := response.Get("response.result")
			if result.Type != gjson.String || result.String() != tt.wantResult {
				t.Fatalf("response.result = %s, want string %q", result.Raw, tt.wantResult)
			}
			parts := response.Get("parts").Array()
			if len(parts) != len(tt.wantData) {
				t.Fatalf("image count = %d, want %d", len(parts), len(tt.wantData))
			}
			for i, part := range parts {
				if got := part.Get("inlineData.mimeType").String(); got != tt.wantMIMEs[i] {
					t.Errorf("image %d MIME = %q, want %q", i, got, tt.wantMIMEs[i])
				}
				if got := part.Get("inlineData.data").String(); got != tt.wantData[i] {
					t.Errorf("image %d data = %q, want %q", i, got, tt.wantData[i])
				}
			}
			if len(parts) == 0 && response.Get("parts").Exists() {
				t.Fatal("text-only functionResponse should not gain a parts field")
			}
		})
	}
}

func TestConvertOpenAIRequestToAntigravityToolImagesStayScopedToCallAndTurn(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"user","content":"inspect images"},
		{"role":"assistant","tool_calls":[
			{"id":"call_shared","type":"function","function":{"name":"first_image","arguments":"{}"}},
			{"id":"call_other","type":"function","function":{"name":"second_image","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_other","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,c2Vjb25k"}}]},
		{"role":"tool","tool_call_id":"call_shared","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,Zmlyc3Q="}}]},
		{"role":"assistant","tool_calls":[{"id":"call_shared","type":"function","function":{"name":"third_image","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_shared","content":[{"type":"image_url","image_url":{"url":"data:image/webp;base64,dGhpcmQ="}}]}
	]}`)
	out := ConvertOpenAIRequestToAntigravity("gemini-3-flash", input, false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) != 5 {
		t.Fatalf("contents count = %d, want 5", len(contents))
	}
	for _, want := range []struct {
		content int
		part    int
		id      string
		name    string
		mime    string
		data    string
	}{
		{2, 0, "call_shared", "first_image", "image/png", "Zmlyc3Q="},
		{2, 1, "call_other", "second_image", "image/jpeg", "c2Vjb25k"},
		{4, 0, "call_shared", "third_image", "image/webp", "dGhpcmQ="},
	} {
		response := contents[want.content].Get("parts").Array()[want.part].Get("functionResponse")
		if response.Get("id").String() != want.id || response.Get("name").String() != want.name {
			t.Fatalf("tool image assigned to wrong call: %s", response.Raw)
		}
		parts := response.Get("parts").Array()
		if len(parts) != 1 || parts[0].Get("inlineData.mimeType").String() != want.mime || parts[0].Get("inlineData.data").String() != want.data {
			t.Fatalf("wrong image for %s", want.name)
		}
	}
}

func TestConvertOpenAIRequestToAntigravityLargeToolImageIsNotText(t *testing.T) {
	// Match the size of the reported PNG without checking in a real screenshot.
	data := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 1513607)))
	content, err := json.Marshal([]any{map[string]any{
		"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64," + data},
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := ConvertOpenAIRequestToAntigravity("gemini-3-flash", toolImageRequest(string(content)), false)
	response := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse")
	if got := response.Get("response.result").String(); got != "{}" {
		t.Fatalf("image-only result contains %d text bytes, want empty fallback", len(got))
	}
	if got := response.Get("parts.0.inlineData.data").String(); got != data {
		t.Fatal("large image payload was lost or changed")
	}
	if got := strings.Count(string(out), data); got != 1 {
		t.Fatalf("large base64 payload appears %d times, want once as native image data", got)
	}
}

func toolImageRequest(content string) []byte {
	return []byte(`{"messages":[
		{"role":"user","content":"inspect image"},
		{"role":"assistant","tool_calls":[{"id":"call_image","type":"function","function":{"name":"view_image","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_image","content":` + content + `}
	]}`)
}
