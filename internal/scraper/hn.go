package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const hnAlgoliaURL = "https://hn.algolia.com/api/v1/search"

// HNScraper scrapes the monthly "Ask HN: Who is Hiring?" thread
type HNScraper struct {
	client *http.Client
}

func NewHNScraper() *HNScraper {
	return &HNScraper{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *HNScraper) Name() string { return "HN-Hiring" }

type hnSearchResult struct {
	Hits []struct {
		ObjectID string `json:"objectID"`
		Title    string `json:"title"`
	} `json:"hits"`
}

type hnItem struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
	By   string `json:"by"`
	Kids []int  `json:"kids"`
	Time int64  `json:"time"`
}

func (h *HNScraper) Scrape(ctx context.Context, query SearchQuery, onJobs func([]Job)) ([]Job, error) {
	// Step 1: Find the latest "Who is Hiring" thread via Algolia
	params := url.Values{}
	params.Set("query", "Ask HN: Who is hiring")
	params.Set("tags", "ask_hn,story")
	params.Set("hitsPerPage", "1")

	resp, err := h.client.Get(hnAlgoliaURL + "?" + params.Encode())
	if err != nil {
		return nil, fmt.Errorf("algolia request failed: %w", err)
	}
	defer resp.Body.Close()

	var result hnSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Hits) == 0 {
		return nil, fmt.Errorf("no HN hiring thread found")
	}

	threadID := result.Hits[0].ObjectID

	// Step 2: Fetch the thread item to get comment IDs
	threadURL := fmt.Sprintf("https://hacker-news.firebaseio.com/v0/item/%s.json", threadID)
	threadResp, err := h.client.Get(threadURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch HN thread: %w", err)
	}
	defer threadResp.Body.Close()

	var thread hnItem
	if err := json.NewDecoder(threadResp.Body).Decode(&thread); err != nil {
		return nil, err
	}

	// Step 3: Fetch top-level comments (each is one job post), capped at 100
	limit := 100
	if len(thread.Kids) < limit {
		limit = len(thread.Kids)
	}

	var jobs []Job
	for _, kid := range thread.Kids[:limit] {
		select {
		case <-ctx.Done():
			return jobs, ctx.Err()
		default:
		}

		itemURL := fmt.Sprintf("https://hacker-news.firebaseio.com/v0/item/%d.json", kid)
		itemResp, err := h.client.Get(itemURL)
		if err != nil {
			continue
		}

		var comment hnItem
		if err := json.NewDecoder(itemResp.Body).Decode(&comment); err != nil {
			itemResp.Body.Close()
			continue
		}
		itemResp.Body.Close()

		text := stripHTML(comment.Text)
		title, company := parseHNJobTitle(text)
		
		if !matchesQuery(title, text, nil, company, query) {
			continue
		}
		comp := extractHNCompensation(text)

		jobs = append(jobs, Job{
			ID:           fmt.Sprintf("hn-%d", comment.ID),
			Title:        title,
			Company:      company,
			Location:     "See description",
			Description:  text,
			Compensation: comp,
			URL:          fmt.Sprintf("https://news.ycombinator.com/item?id=%d", comment.ID),
			Source:       "hn-hiring",
			Remote:       strings.Contains(strings.ToLower(text), "remote"),
			ScrapedAt:    time.Now(),
		})
	}

	if onJobs != nil && len(jobs) > 0 {
		onJobs(jobs)
	}
	return jobs, nil
}

// parseHNJobTitle attempts to extract company and role from the first line of an HN post.
// HN posts typically follow "Company | Role | Location | ..." or "Company (Remote) | Role"
func parseHNJobTitle(text string) (title, company string) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return "Software Engineer", "Unknown"
	}
	first := strings.TrimSpace(lines[0])
	parts := strings.Split(first, "|")
	if len(parts) >= 2 {
		return strings.TrimSpace(parts[1]), strings.TrimSpace(parts[0])
	}
	if len(first) > 80 {
		first = first[:80] + "..."
	}
	return first, "Unknown"
}

// stripHTML removes HTML tags and decodes common HTML entities from HN comment text
func stripHTML(s string) string {
	replacements := [][2]string{
		{"<p>", "\n"}, {"</p>", ""}, {"<br>", "\n"}, {"<br/>", "\n"},
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`},
		{"&#x27;", "'"}, {"&#x2F;", "/"}, {"&nbsp;", " "},
	}
	for _, pair := range replacements {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	var b strings.Builder
	inTag := false
	for _, ch := range s {
		switch {
		case ch == '<':
			inTag = true
		case ch == '>':
			inTag = false
		case !inTag:
			b.WriteRune(ch)
		}
	}
	return strings.TrimSpace(b.String())
}

// extractHNCompensation attempts to find salary bands in the freeform HN text
func extractHNCompensation(text string) string {
	re := regexp.MustCompile(`(?i)\$[\d,]{2,3}k?\s*(?:-|to)\s*\$[\d,]{2,3}k?|\$[\d,]{2,3}k`)
	match := re.FindString(text)
	return match
}
