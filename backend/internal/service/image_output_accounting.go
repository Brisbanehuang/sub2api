package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
)

type openAIImageOutputCounter struct {
	seen         map[string]struct{}
	seenSizes    map[string]string
	seenOrder    []string
	dataSizes    []string
	count        int
	maxDataCount int
	diagnostics  []openAIImageOutputDiagnostic
}

func newOpenAIImageOutputCounter() *openAIImageOutputCounter {
	return &openAIImageOutputCounter{
		seen:      make(map[string]struct{}),
		seenSizes: make(map[string]string),
	}
}

type openAIImageOutputDiagnostic struct {
	Source        string `json:"source"`
	EventType     string `json:"event_type,omitempty"`
	ItemType      string `json:"item_type,omitempty"`
	Count         int    `json:"count,omitempty"`
	HasResult     bool   `json:"has_result,omitempty"`
	HasB64JSON    bool   `json:"has_b64_json,omitempty"`
	HasURL        bool   `json:"has_url,omitempty"`
	HasID         bool   `json:"has_id,omitempty"`
	HasCallID     bool   `json:"has_call_id,omitempty"`
	SkippedReason string `json:"skipped_reason,omitempty"`
}

func (c *openAIImageOutputCounter) Diagnostics() []openAIImageOutputDiagnostic {
	if c == nil || len(c.diagnostics) == 0 {
		return nil
	}
	out := make([]openAIImageOutputDiagnostic, len(c.diagnostics))
	copy(out, c.diagnostics)
	return out
}

func (c *openAIImageOutputCounter) addDiagnostic(diag openAIImageOutputDiagnostic) {
	if c == nil || diag.Source == "" {
		return
	}
	c.diagnostics = append(c.diagnostics, diag)
}

func (c *openAIImageOutputCounter) Count() int {
	if c == nil {
		return 0
	}
	if c.maxDataCount > c.count {
		return c.maxDataCount
	}
	return c.count
}

func (c *openAIImageOutputCounter) Sizes() []string {
	if c == nil {
		return nil
	}
	sizes := make([]string, 0, len(c.seenOrder)+len(c.dataSizes))
	for _, key := range c.seenOrder {
		if size := strings.TrimSpace(c.seenSizes[key]); size != "" {
			sizes = append(sizes, size)
		}
	}
	if len(sizes) == 0 && len(c.dataSizes) > 0 {
		sizes = append(sizes, c.dataSizes...)
	}
	if len(sizes) == 0 {
		return nil
	}
	return sizes
}

func (c *openAIImageOutputCounter) AddJSONResponse(body []byte) {
	if c == nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return
	}
	c.addDataArray(gjson.GetBytes(body, "data"), "json.data", "")
	c.addOutputArray(gjson.GetBytes(body, "output"), "json.output", "")
	c.addOutputArray(gjson.GetBytes(body, "response.output"), "json.response.output", "")
}

func (c *openAIImageOutputCounter) AddSSEData(data []byte) {
	if c == nil || len(data) == 0 || strings.TrimSpace(string(data)) == "[DONE]" || !gjson.ValidBytes(data) {
		return
	}
	root := gjson.ParseBytes(data)
	eventType := strings.TrimSpace(root.Get("type").String())
	c.addDataArray(root.Get("data"), "sse.data", eventType)
	switch eventType {
	case "response.output_item.done":
		c.addImageOutputItem(root.Get("item"), "response.output_item.done", eventType)
	case "response.completed", "response.done":
		c.addOutputArray(root.Get("response.output"), "response.output", eventType)
	case "image_generation.completed", "image_edit.completed":
		if item := root.Get("item"); item.Exists() {
			c.addImageOutputItem(item, "image_lifecycle.item", eventType)
			return
		}
		if output := root.Get("output"); output.Exists() {
			c.addImageOutputItem(output, "image_lifecycle.output", eventType)
			return
		}
		c.addImageOutputItem(root, "image_lifecycle.root", eventType)
	}
}

func (c *openAIImageOutputCounter) AddSSEBody(body string) {
	if c == nil || strings.TrimSpace(body) == "" {
		return
	}
	forEachOpenAISSEDataPayload(body, c.AddSSEData)
}

func (c *openAIImageOutputCounter) addDataArray(data gjson.Result, source string, eventType string) {
	if !data.IsArray() {
		return
	}
	items := data.Array()
	imageCount := 0
	hasB64JSON := false
	hasURL := false
	sizes := make([]string, 0, len(items))
	for _, item := range items {
		if !item.IsObject() {
			continue
		}
		itemHasURL := strings.TrimSpace(item.Get("url").String()) != ""
		itemHasB64JSON := strings.TrimSpace(item.Get("b64_json").String()) != ""
		hasImageOutput := itemHasURL || itemHasB64JSON
		if !hasImageOutput {
			continue
		}
		hasURL = hasURL || itemHasURL
		hasB64JSON = hasB64JSON || itemHasB64JSON
		imageCount++
		if size := strings.TrimSpace(item.Get("size").String()); size != "" {
			sizes = append(sizes, size)
		}
	}
	if imageCount > c.maxDataCount {
		c.maxDataCount = imageCount
		c.addDiagnostic(openAIImageOutputDiagnostic{
			Source:     source,
			EventType:  eventType,
			Count:      imageCount,
			HasB64JSON: hasB64JSON,
			HasURL:     hasURL,
		})
	}
	if len(sizes) > 0 {
		c.dataSizes = sizes
	}
}

func (c *openAIImageOutputCounter) addOutputArray(output gjson.Result, source string, eventType string) {
	if !output.IsArray() {
		return
	}
	output.ForEach(func(_, item gjson.Result) bool {
		c.addImageOutputItem(item, source, eventType)
		return true
	})
}

func (c *openAIImageOutputCounter) addImageOutputItem(item gjson.Result, source string, eventType string) {
	if !item.Exists() || !item.IsObject() {
		return
	}
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType != "" && itemType != "image_generation_call" && itemType != "image_generation.completed" && itemType != "image_edit.completed" {
		return
	}
	if strings.Contains(strings.ToLower(item.Raw), "partial_image") {
		return
	}
	result := strings.TrimSpace(item.Get("result").String())
	if result == "" {
		result = strings.TrimSpace(item.Get("b64_json").String())
	}
	if result == "" {
		result = strings.TrimSpace(item.Get("url").String())
	}
	if result == "" {
		return
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("call_id").String())
	}
	if key == "" {
		key = hashOpenAIImageOutputResult(result)
	}
	if key == "" {
		return
	}
	size := strings.TrimSpace(item.Get("size").String())
	if _, exists := c.seen[key]; exists {
		if size != "" && strings.TrimSpace(c.seenSizes[key]) == "" {
			c.seenSizes[key] = size
		}
		return
	}
	c.seen[key] = struct{}{}
	c.seenOrder = append(c.seenOrder, key)
	if size != "" {
		c.seenSizes[key] = size
	}
	c.count++
	c.addDiagnostic(openAIImageOutputDiagnostic{
		Source:     source,
		EventType:  eventType,
		ItemType:   itemType,
		HasResult:  strings.TrimSpace(item.Get("result").String()) != "",
		HasB64JSON: strings.TrimSpace(item.Get("b64_json").String()) != "",
		HasURL:     strings.TrimSpace(item.Get("url").String()) != "",
		HasID:      strings.TrimSpace(item.Get("id").String()) != "",
		HasCallID:  strings.TrimSpace(item.Get("call_id").String()) != "",
	})
}

func hashOpenAIImageOutputResult(result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(result))
	return hex.EncodeToString(sum[:])
}

func countOpenAIResponseImageOutputsFromJSONBytes(body []byte) int {
	count, _ := analyzeOpenAIResponseImageOutputsFromJSONBytes(body)
	return count
}

func analyzeOpenAIResponseImageOutputsFromJSONBytes(body []byte) (int, []openAIImageOutputDiagnostic) {
	counter := newOpenAIImageOutputCounter()
	counter.AddJSONResponse(body)
	return counter.Count(), counter.Diagnostics()
}

func collectOpenAIResponseImageOutputSizesFromJSONBytes(body []byte) []string {
	counter := newOpenAIImageOutputCounter()
	counter.AddJSONResponse(body)
	return counter.Sizes()
}

func countOpenAIImageOutputsFromSSEBody(body string) int {
	count, _ := analyzeOpenAIImageOutputsFromSSEBody(body)
	return count
}

func analyzeOpenAIImageOutputsFromSSEBody(body string) (int, []openAIImageOutputDiagnostic) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEBody(body)
	return counter.Count(), counter.Diagnostics()
}

func collectOpenAIImageOutputSizesFromSSEBody(body string) []string {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEBody(body)
	return counter.Sizes()
}
