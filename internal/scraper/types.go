package scraper

import (
	"context"
	"time"
)

// Job represents a scraped job listing
type Job struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Company        string    `json:"company"`
	Location       string    `json:"location"`
	Description    string    `json:"description"`
	Compensation   string    `json:"compensation"`
	URL            string    `json:"url"`
	Source         string    `json:"source"`
	Tags           []string  `json:"tags"`
	Remote         bool      `json:"remote"`
	ScrapedAt      time.Time `json:"scraped_at"`
	
	// Tailored Metadata
	Score          int       `json:"score"`
	MarketSalary   string    `json:"market_salary"`
	FitBrief       string    `json:"fit_brief"`
	TailoredResume string    `json:"tailored_resume"`
	TailoredReport string    `json:"tailored_report"`
	CoverLetter    string    `json:"cover_letter"`
}

// SearchQuery defines the parameters for job scraping runs
type SearchQuery struct {
	Keywords        []string
	Location        string
	Remote          bool
	TargetCompanies []string
}

// Scraper defines the interface all job source scrapers must implement
type Scraper interface {
	Name() string
	Scrape(ctx context.Context, query SearchQuery) ([]Job, error)
}
