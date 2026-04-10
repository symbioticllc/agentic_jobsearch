package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/leee/agentic-jobs/internal/llm"
	"github.com/chromedp/chromedp"
)

// DirectScraper iterates over target companies, tries to locate their career page (via ATS guess or DDG search), and scrapes jobs.
type DirectScraper struct {
	client       *http.Client
	allocatorCtx context.Context
}

func NewDirectScraper() *DirectScraper {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)
	allocCtx, _ := chromedp.NewExecAllocator(context.Background(), opts...)

	return &DirectScraper{
		client:       &http.Client{Timeout: 30 * time.Second},
		allocatorCtx: allocCtx,
	}
}

func (d *DirectScraper) Name() string { return "Direct-Target" }

func (d *DirectScraper) Scrape(ctx context.Context, query SearchQuery) ([]Job, error) {
	if len(query.TargetCompanies) == 0 {
		return nil, nil
	}

	log.Printf("Starting DirectScraper for %d target companies...", len(query.TargetCompanies))

	compChan := make(chan string, len(query.TargetCompanies))
	for _, company := range query.TargetCompanies {
		compChan <- company
	}
	close(compChan)

	jobsChan := make(chan []Job, len(query.TargetCompanies))
	
	workerCount := 10
	if len(query.TargetCompanies) < workerCount {
		workerCount = len(query.TargetCompanies)
	}

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for company := range compChan {
				select {
				case <-ctx.Done():
					return
				default:
				}

				jobs, found := d.tryATS(company, query)
				if found {
					jobsChan <- jobs
					continue
				}

				// If ATS fails, fallback to DDG search and LLM parsing
				jobs, found = d.fallbackDDGLLM(ctx, company, query)
				if found {
					jobsChan <- jobs
				}
			}
		}()
	}

	wg.Wait()
	close(jobsChan)

	var allJobs []Job
	for jobs := range jobsChan {
		allJobs = append(allJobs, jobs...)
	}

	return allJobs, nil
}

// tryATS tries to hit common ATS APIs by guessing the company slug
func (d *DirectScraper) tryATS(company string, query SearchQuery) ([]Job, bool) {
	slug := sanitizeSlug(company)
	
	// 1. Greenhouse
	ghJobs, ok := d.scrapeGreenhouse(slug, company, query)
	if ok && len(ghJobs) > 0 {
		return ghJobs, true
	}

	// 2. Lever
	levJobs, ok := d.scrapeLever(slug, company, query)
	if ok && len(levJobs) > 0 {
		return levJobs, true
	}

	return nil, false
}

// scraper ATS models
type ghResponse struct {
	Jobs []struct {
		AbsoluteURL string `json:"absolute_url"`
		ID          int    `json:"id"`
		Location    struct {
			Name string `json:"name"`
		} `json:"location"`
		Title string `json:"title"`
	} `json:"jobs"`
}

func (d *DirectScraper) scrapeGreenhouse(slug, realCompanyName string, query SearchQuery) ([]Job, bool) {
	api := fmt.Sprintf("https://boards-api.greenhouse.io/v1/boards/%s/jobs", slug)
	req, _ := http.NewRequest("GET", api, nil)
	resp, err := d.client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, false
	}
	defer resp.Body.Close()

	var res ghResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, false
	}

	var out []Job
	for _, j := range res.Jobs {
		if matchesQuery(j.Title, "", nil, realCompanyName, query) {
			desc := d.fetchGreenhouseDescription(slug, j.ID)
			comp := extractHNCompensation(desc) // attempt extracting US comp string if present

			out = append(out, Job{
				ID:           fmt.Sprintf("gh-%d", j.ID),
				Title:        j.Title,
				Company:      realCompanyName,
				Location:     j.Location.Name,
				Description:  desc,
				Compensation: comp,
				URL:          j.AbsoluteURL,
				Source:       "greenhouse",
				Remote:       strings.Contains(strings.ToLower(j.Location.Name), "remote"),
				ScrapedAt:    time.Now(),
			})
		}
	}
	return out, true
}

func (d *DirectScraper) fetchGreenhouseDescription(slug string, jobID int) string {
	api := fmt.Sprintf("https://boards-api.greenhouse.io/v1/boards/%s/jobs/%d", slug, jobID)
	req, _ := http.NewRequest("GET", api, nil)
	resp, err := d.client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return "(Missing description details)"
	}
	defer resp.Body.Close()

	var details struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&details); err == nil {
		return stripHTML(details.Content)
	}
	return "(Could not parse greenhouse content)"
}

func sanitizeSlug(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, ",", "")
	return s
}

type leverResponse []struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	HostedURL string `json:"hostedUrl"`
	Categories struct {
		Location string `json:"location"`
	} `json:"categories"`
	DescriptionPlain string `json:"descriptionPlain"`
}

func (d *DirectScraper) scrapeLever(slug, realCompanyName string, query SearchQuery) ([]Job, bool) {
	api := fmt.Sprintf("https://api.lever.co/v0/postings/%s", slug)
	req, _ := http.NewRequest("GET", api, nil)
	resp, err := d.client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, false
	}
	defer resp.Body.Close()

	var res leverResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, false
	}

	var out []Job
	for _, j := range res {
		if matchesQuery(j.Text, j.DescriptionPlain, nil, realCompanyName, query) {
			
			comp := ""
			// try extract lever comp
			if d := extractHNCompensation(j.DescriptionPlain); d != "" {
				comp = d
			}

			out = append(out, Job{
				ID:          "lev-" + j.ID,
				Title:       j.Text,
				Company:     realCompanyName,
				Location:    j.Categories.Location,
				Description: j.DescriptionPlain,
				Compensation: comp,
				URL:         j.HostedURL,
				Source:      "lever",
				Remote:      strings.Contains(strings.ToLower(j.Categories.Location), "remote"),
				ScrapedAt:   time.Now(),
			})
		}
	}
	return out, true
}

// Fallback search logic - hits DuckDuckGo and tries to parse the html
func (d *DirectScraper) fallbackDDGLLM(ctx context.Context, company string, query SearchQuery) ([]Job, bool) {
	// Let's implement a DuckDuckGo Lite search for "company name careers"
	q := url.QueryEscape(company + " careers jobs")
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://lite.duckduckgo.com/lite/", strings.NewReader("q="+q))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)") // needed to not get 403
	
	resp, err := d.client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return nil, false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	
	// find first real result link: `class="result-url" href="([^"]+)"`
	re := regexp.MustCompile(`class="result-url" href="([^"]+)"`)
	matches := re.FindStringSubmatch(string(body))
	if len(matches) < 2 {
		return nil, false
	}
	careerURL := matches[1]

	log.Printf("🤖 Agent found likely career URL for %s: %s . Analyzing with LLM by executing Headless DOM render...", company, careerURL)

	// Fetch the actual career page using Headless Chrome Injection
	chromCtx, cancelTab := chromedp.NewContext(d.allocatorCtx)
	defer cancelTab()

	timeoutCtx, cancelTimeout := context.WithTimeout(chromCtx, 30*time.Second)
	defer cancelTimeout()

	var pgBody string
	err = chromedp.Run(timeoutCtx,
		chromedp.Navigate(careerURL),
		chromedp.Sleep(5*time.Second), // Let React/Workday hydrate JSON into GUI
		chromedp.OuterHTML("html", &pgBody),
	)
	if err != nil {
		log.Printf("⚠️ Headless browser failed to render %s: %v", company, err)
		return nil, false
	}

	// extremely naive html strip
	text := stripHTML(pgBody)

	extracted, err := llm.ExtractJobsFromText(ctx, company, text)
	if err != nil {
		log.Printf("⚠️ Agent extraction failed for %s: %v", company, err)
		return nil, false
	}

	var jobs []Job
	for _, ej := range extracted {
		jobs = append(jobs, Job{
			ID:          "agent-" + sanitizeSlug(company) + "-" + sanitizeSlug(ej.Title),
			Title:       ej.Title,
			Company:     company,
			Location:    ej.Location,
			Description: ej.Description,
			URL:         careerURL, // We just link to the main career portal since deep-links are hard to parse statically
			Source:      "agent-crawler",
			Remote:      strings.Contains(strings.ToLower(ej.Location), "remote"),
			ScrapedAt:   time.Now(),
		})
	}

	if len(jobs) > 0 {
		return jobs, true
	}
	return nil, false
}
