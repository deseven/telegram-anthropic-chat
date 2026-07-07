// Package tavily wraps the go-tavily client to expose two Anthropic tool-call
// operations — web search and URL content extraction — and to describe those
// tools in the Anthropic SDK's ToolParam shape so the LLM can invoke them.
package tavily

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	tavilyclient "github.com/iamwavecut/go-tavily"

	"github.com/zoo/telegram-anthropic-chat/internal/log"
)

// Tool names exposed to the model.
const (
	ToolWebSearch = "web_search"
	ToolExtract   = "extract_url"
)

// Client wraps a go-tavily client together with the Anthropic tool
// definitions the model is offered.
type Client struct {
	api   *tavilyclient.Client
	tools []anthropic.ToolUnionParam
}

// New creates a Client using the provided Tavily API key. If the key is empty
// nil is returned; callers should check for nil before using the client.
func New(apiKey string) *Client {
	if apiKey == "" {
		return nil
	}
	c := &Client{
		api: tavilyclient.New(apiKey, nil),
	}
	c.tools = buildTools()
	return c
}

// Tools returns the Anthropic tool definitions to send with a Messages request.
func (c *Client) Tools() []anthropic.ToolUnionParam {
	return c.tools
}

// Execute runs the named tool with the given raw JSON input and returns the
// JSON-serialized result (or an error message as the result content). The
// returned string is meant to be placed into a tool_result block.
func (c *Client) Execute(ctx context.Context, name string, input json.RawMessage) string {
	switch name {
	case ToolWebSearch:
		return c.execWebSearch(ctx, input)
	case ToolExtract:
		return c.execExtract(ctx, input)
	default:
		return fmt.Sprintf("unknown tool: %s", name)
	}
}

// webSearchInput is the expected input shape for the web_search tool.
type webSearchInput struct {
	Term string `json:"term"`
}

// extractInput is the expected input shape for the extract_url tool.
type extractInput struct {
	URL           string `json:"url"`
	ExtractDepth  string `json:"extract_depth"`
}

func (c *Client) execWebSearch(ctx context.Context, input json.RawMessage) string {
	var in webSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return fmt.Sprintf("invalid input: %v", err)
	}
	if strings.TrimSpace(in.Term) == "" {
		return "search term is required"
	}

	log.Print("tavily", "web_search: %s", in.Term)
	resp, err := c.api.SearchSimple(ctx, in.Term)
	if err != nil {
		log.Print("tavily", "web_search failed: %v", err)
		return fmt.Sprintf("search failed: %v", err)
	}

	// Trim the response to the fields the model needs: title, url and a
	// content snippet per result. This keeps the tool result compact.
	type result struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	}
	out := struct {
		Query        string  `json:"query"`
		ResponseTime float64 `json:"response_time"`
		Results      []result `json:"results"`
	}{
		Query:        resp.Query,
		ResponseTime: resp.ResponseTime,
	}
	for _, r := range resp.Results {
		out.Results = append(out.Results, result{Title: r.Title, URL: r.URL, Content: r.Content})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func (c *Client) execExtract(ctx context.Context, input json.RawMessage) string {
	var in extractInput
	if err := json.Unmarshal(input, &in); err != nil {
		return fmt.Sprintf("invalid input: %v", err)
	}
	if strings.TrimSpace(in.URL) == "" {
		return "url is required"
	}

	depth := in.ExtractDepth
	if depth != string(tavilyclient.SearchDepthBasic) && depth != string(tavilyclient.SearchDepthAdvanced) {
		depth = "" // let the client apply its default
	}

	log.Print("tavily", "extract_url: %s (depth=%q)", in.URL, depth)
	opts := &tavilyclient.ExtractOptions{
		Format:       string(tavilyclient.FormatMarkdown),
		ExtractDepth: depth,
	}
	resp, err := c.api.Extract(ctx, []string{in.URL}, opts)
	if err != nil {
		log.Print("tavily", "extract_url failed: %v", err)
		return fmt.Sprintf("extract failed: %v", err)
	}

	type result struct {
		URL        string `json:"url"`
		RawContent string `json:"raw_content"`
	}
	type failed struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	out := struct {
		ResponseTime  float64  `json:"response_time"`
		Results       []result `json:"results"`
		FailedResults []failed `json:"failed_results"`
	}{
		ResponseTime: resp.ResponseTime,
	}
	for _, r := range resp.Results {
		out.Results = append(out.Results, result{URL: r.URL, RawContent: r.RawContent})
	}
	for _, f := range resp.FailedResults {
		out.FailedResults = append(out.FailedResults, failed{URL: f.URL, Error: f.Error})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// buildTools returns the Anthropic tool definitions for web_search and
// extract_url.
func buildTools() []anthropic.ToolUnionParam {
	webSearch := anthropic.ToolParam{
		Name:        ToolWebSearch,
		Description: anthropic.String("Search the web for up-to-date information. Use this when you need current facts, news, or details you don't already know. Returns a list of results with title, url and a content snippet."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"term": map[string]any{
					"type":        "string",
					"description": "The search query / search term.",
				},
			},
			Required: []string{"term"},
		},
	}

	extract := anthropic.ToolParam{
		Name:        ToolExtract,
		Description: anthropic.String("Extract the full text content of a specific web page URL in markdown format. Use this to read a page you found via web_search or that the user provided. Returns the page content."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The absolute URL of the web page to extract content from.",
				},
				"extract_depth": map[string]any{
					"type":        "string",
					"enum":        []string{"basic", "advanced"},
					"description": "Extraction depth. \"basic\" is faster, \"advanced\" is more thorough. Defaults to basic.",
				},
			},
			Required: []string{"url"},
		},
	}

	return []anthropic.ToolUnionParam{
		{OfTool: &webSearch},
		{OfTool: &extract},
	}
}
