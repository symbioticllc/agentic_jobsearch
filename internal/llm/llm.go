package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/ollama"
)

var globalLLM llms.Model

// InitLLM sets up Ollama via LangchainGo
func InitLLM() error {
	fmt.Println(" -> Initializing LLM Orchestration Layer (Ollama + Gemma)...")

	var err error
	globalLLM, err = ollama.New(
		ollama.WithServerURL("http://localhost:11434"),
		ollama.WithModel("gemma2:9b"),
	)
	if err != nil {
		return fmt.Errorf("failed to initialize Ollama: %w", err)
	}

	// Quick connection test
	ctx := context.Background()
	_, err = globalLLM.Call(ctx, "hello")
	if err != nil {
		log.Printf("⚠️  Ollama connectivity warning: %v\n", err)
	} else {
		fmt.Println(" ✅ Ollama connected successfully!")
	}

	return nil
}

// Generate sends a prompt to the LLM and returns the response string
func Generate(ctx context.Context, prompt string) (string, error) {
	if globalLLM == nil {
		log.Println("⚠️  Ollama not initialized, falling back directly to Claude...")
		return generateClaude(ctx, prompt)
	}

	resp, err := globalLLM.Call(ctx, prompt)
	if err != nil {
		log.Printf("⚠️  Ollama generation failed: %v. Firing failover to Anthropic Claude 3.5...\n", err)
		return generateClaude(ctx, prompt)
	}

	return resp, nil
}

// generateClaude directly contacts anthropic using minimal standard-lib HTTP structs
func generateClaude(ctx context.Context, prompt string) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}

	payload := map[string]interface{}{
		"model":      "claude-3-5-sonnet-20241022",
		"max_tokens": 4096,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": prompt,
			},
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}

	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("claude API request dropped: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("claude API rejected request with status: %d", resp.StatusCode)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode claude JSON response: %w", err)
	}

	if len(result.Content) == 0 {
		return "", fmt.Errorf("claude API returned an empty text content array")
	}

	return result.Content[0].Text, nil
}

// ExtractedJob holds the structured JSON output from the LLM HTML extraction wrapper
type ExtractedJob struct {
	Title       string `json:"title"`
	Location    string `json:"location"`
	Description string `json:"description"`
}

// ExtractJobsFromText uses the LLM to process unstructured markdown/text from a career page and return jobs in JSON format.
func ExtractJobsFromText(ctx context.Context, company string, text string) ([]ExtractedJob, error) {
	if globalLLM == nil {
		return nil, fmt.Errorf("LLM not initialized")
	}

	if len(text) > 8000 {
		text = text[:8000] // truncate to fit context window reasonably
	}

	prompt := fmt.Sprintf(`
You are an intelligent data extraction agent. 
Analyze the following unstructured text extracted from the career page of "%s".
Extract all open software engineering, data, and technical job roles.

Return the results ONLY as a valid JSON array of objects. 
Each object must have exactly these keys: "title", "location", "description".
If you do not find any relevant jobs, return an empty array [].

Text to analyze:
---
%s
---
Output JSON array:`, company, text)

	response, err := Generate(ctx, prompt)
	if err != nil {
		return nil, err
	}

	// Clean up LLM output (remove markdown blocks if present)
	cleaned := strings.TrimSpace(response)
	if strings.HasPrefix(cleaned, "```json") {
		cleaned = strings.TrimPrefix(cleaned, "```json")
		cleaned = strings.TrimSuffix(cleaned, "```")
	} else if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimPrefix(cleaned, "```")
		cleaned = strings.TrimSuffix(cleaned, "```")
	}
	cleaned = strings.TrimSpace(cleaned)

	var jobs []ExtractedJob
	if err := json.Unmarshal([]byte(cleaned), &jobs); err != nil {
		return nil, fmt.Errorf("failed to parse extracted JSON: %w (Raw: %s)", err, cleaned)
	}

	return jobs, nil
}

