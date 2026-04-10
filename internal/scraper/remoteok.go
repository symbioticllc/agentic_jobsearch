package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const remoteOKAPIURL = "https://remoteok.com/api"

// RemoteOKScraper hits the RemoteOK JSON API — no auth required
type RemoteOKScraper struct {
	client *http.Client
}

func NewRemoteOKScraper() *RemoteOKScraper {
	return &RemoteOKScraper{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (r *RemoteOKScraper) Name() string { return "RemoteOK" }

type remoteOKJob struct {
	ID          string   `json:"id"`
	Company     string   `json:"company"`
	Position    string   `json:"position"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
	Location    string   `json:"location"`
	URL         string   `json:"url"`
	Date        string   `json:"date"`
	SalaryMin   int      `json:"salary_min"`
	SalaryMax   int      `json:"salary_max"`
}

func (r *RemoteOKScraper) Scrape(ctx context.Context, query SearchQuery) ([]Job, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteOKAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// RemoteOK returns a JSON array where the first element is a legal notice, not a job
	var raw []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode error: %w", err)
	}

	var jobs []Job
	for i, item := range raw {
		if i == 0 {
			continue // skip legal notice object
		}
		var j remoteOKJob
		if err := json.Unmarshal(item, &j); err != nil {
			continue
		}
		if !matchesQuery(j.Position, j.Description, j.Tags, j.Company, query) {
			continue
		}
		comp := ""
		if j.SalaryMin > 0 || j.SalaryMax > 0 {
			if j.SalaryMin > 0 && j.SalaryMax > 0 {
				comp = fmt.Sprintf("$%dk - $%dk", j.SalaryMin/1000, j.SalaryMax/1000)
			} else if j.SalaryMin > 0 {
				comp = fmt.Sprintf("$%dk+", j.SalaryMin/1000)
			} else {
				comp = fmt.Sprintf("Up to $%dk", j.SalaryMax/1000)
			}
		}

		jobs = append(jobs, Job{
			ID:           j.ID,
			Title:        j.Position,
			Company:      j.Company,
			Location:     j.Location,
			Description:  j.Description,
			Compensation: comp,
			URL:          j.URL,
			Source:       "remoteok",
			Tags:         j.Tags,
			Remote:       true,
			ScrapedAt:    time.Now(),
		})
	}
	return jobs, nil
}

// matchesQuery checks if a job matches any keyword and target company in the search query
func matchesQuery(title, description string, tags []string, company string, query SearchQuery) bool {
	if len(query.TargetCompanies) > 0 {
		matchedCompany := false
		lowerCmp := strings.ToLower(company)
		for _, tc := range query.TargetCompanies {
			if strings.Contains(lowerCmp, strings.ToLower(tc)) || strings.Contains(strings.ToLower(tc), lowerCmp) {
				matchedCompany = true
				break
			}
		}
		if !matchedCompany {
			return false
		}
	}

	if len(query.Keywords) == 0 {
		return true
	}
	combined := strings.ToLower(title + " " + description + " " + strings.Join(tags, " "))
	for _, kw := range query.Keywords {
		if strings.Contains(combined, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}
