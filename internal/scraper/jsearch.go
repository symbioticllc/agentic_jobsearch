package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const jSearchAPIURL = "https://jsearch.p.rapidapi.com/search"

// JSearchScraper hits the RapidAPI JSearch endpoint
type JSearchScraper struct {
	client *http.Client
}

func NewJSearchScraper() *JSearchScraper {
	return &JSearchScraper{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *JSearchScraper) Name() string { return "JSearch" }

type jSearchResponse struct {
	Data []jSearchJob `json:"data"`
}

type jSearchJob struct {
	JobID           string `json:"job_id"`
	JobTitle        string `json:"job_title"`
	EmployerName    string `json:"employer_name"`
	JobDescription  string `json:"job_description"`
	JobIsRemote     bool   `json:"job_is_remote"`
	JobCity         string `json:"job_city"`
	JobState        string `json:"job_state"`
	JobCountry      string `json:"job_country"`
	JobApplyLink    string `json:"job_apply_link"`
	JobMinSalary    *int   `json:"job_min_salary"`
	JobMaxSalary    *int   `json:"job_max_salary"`
	JobEmploymentType string `json:"job_employment_type"`
}

func (s *JSearchScraper) Scrape(ctx context.Context, query SearchQuery, onJobs func([]Job)) ([]Job, error) {
	// Build the search query string
	// We limit the search terms to avoid 414 URI Too Long errors, especially since TargetCompanies can be huge
	qTerms := []string{}
	if len(query.Keywords) > 0 {
		limit := 3
		if len(query.Keywords) < limit {
			limit = len(query.Keywords)
		}
		qTerms = append(qTerms, query.Keywords[:limit]...)
	}
	queryString := strings.Join(qTerms, " ")
	if query.Location != "" {
		queryString += " in " + query.Location
	}
	if queryString == "" || queryString == " in " {
		queryString = "developer" // default fallback
	}

	u, err := url.Parse(jSearchAPIURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("query", queryString)
	q.Set("page", "1")
	q.Set("num_pages", "1")
	// q.Set("country", "us")
	q.Set("date_posted", "all")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	
	// Set the RapidAPI headers
	// Ideally these would be environment variables, but for now using the provided key
	req.Header.Set("X-Rapidapi-Key", "01c33bf5c1msh4886f28a5560857p14c5edjsn9eadd0c07a63")
	req.Header.Set("X-Rapidapi-Host", "jsearch.p.rapidapi.com")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jsearch request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jsearch returned non-200 status: %d", resp.StatusCode)
	}

	var result jSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("jsearch decode error: %w", err)
	}

	var jobs []Job
	for _, j := range result.Data {
		// Formatting location
		locParts := []string{}
		if j.JobCity != "" {
			locParts = append(locParts, j.JobCity)
		}
		if j.JobState != "" {
			locParts = append(locParts, j.JobState)
		}
		if j.JobCountry != "" && j.JobCountry != "US" {
			locParts = append(locParts, j.JobCountry)
		}
		location := strings.Join(locParts, ", ")
		if location == "" {
			location = "Remote"
		}

		// Formatting compensation
		comp := ""
		if j.JobMinSalary != nil && j.JobMaxSalary != nil {
			comp = fmt.Sprintf("$%dk - $%dk", *j.JobMinSalary/1000, *j.JobMaxSalary/1000)
		} else if j.JobMinSalary != nil {
			comp = fmt.Sprintf("$%dk+", *j.JobMinSalary/1000)
		} else if j.JobMaxSalary != nil {
			comp = fmt.Sprintf("Up to $%dk", *j.JobMaxSalary/1000)
		}

		// Filter locally since we did a generic remote search
		if !matchesQuery(j.JobTitle, j.JobDescription, []string{j.JobEmploymentType}, j.EmployerName, query) {
			continue
		}

		jobs = append(jobs, Job{
			ID:           j.JobID,
			Title:        j.JobTitle,
			Company:      j.EmployerName,
			Location:     location,
			Description:  j.JobDescription,
			Compensation: comp,
			URL:          j.JobApplyLink,
			Source:       "jsearch",
			Tags:         []string{j.JobEmploymentType},
			Remote:       j.JobIsRemote || query.Remote,
			ScrapedAt:    time.Now(),
		})
	}

	if onJobs != nil && len(jobs) > 0 {
		onJobs(jobs)
	}
	return jobs, nil
}
