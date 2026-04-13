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
	// Also try with dashes preserved for companies like "jp-morgan" vs "jpmorgan"
	dashSlug := sanitizeSlugWithDashes(company)

	slugsToTry := []string{slug}
	if dashSlug != slug {
		slugsToTry = append(slugsToTry, dashSlug)
	}

	for _, s := range slugsToTry {
		// 1. Greenhouse
		ghJobs, ok := d.scrapeGreenhouse(s, company, query)
		if ok && len(ghJobs) > 0 {
			return ghJobs, true
		}

		// 2. Lever
		levJobs, ok := d.scrapeLever(s, company, query)
		if ok && len(levJobs) > 0 {
			return levJobs, true
		}

		// 3. Ashby
		aJobs, ok := d.scrapeAshby(s, company, query)
		if ok && len(aJobs) > 0 {
			return aJobs, true
		}
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

// sanitizeSlugWithDashes preserves word boundaries as dashes (e.g. "JP Morgan" -> "jp-morgan")
func sanitizeSlugWithDashes(s string) string {
	s = strings.ToLower(s)
	s = strings.TrimSpace(s)
	// Replace common separators with a single dash
	re := regexp.MustCompile(`[\s.,;:&]+`)
	s = re.ReplaceAllString(s, "-")
	// Remove any remaining non-alphanumeric/dash characters
	re2 := regexp.MustCompile(`[^a-z0-9-]`)
	s = re2.ReplaceAllString(s, "")
	// Collapse multiple dashes
	re3 := regexp.MustCompile(`-{2,}`)
	s = re3.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
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

// Ashby ATS response structures
type ashbyResponse struct {
	JobPostings []struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		LocationName string `json:"locationName"`
		JobURL       string `json:"jobUrl"`
		DescriptionSections []struct {
			Content string `json:"content"`
		} `json:"descriptionSections"`
	} `json:"jobPostings"`
}

func (d *DirectScraper) scrapeAshby(slug, realCompanyName string, query SearchQuery) ([]Job, bool) {
	api := fmt.Sprintf("https://api.ashbyhq.com/posting-api/job-board/%s", slug)
	req, _ := http.NewRequest("GET", api, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, false
	}
	defer resp.Body.Close()

	var res ashbyResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, false
	}

	var out []Job
	for _, j := range res.JobPostings {
		// Join all description sections
		var descParts []string
		for _, sec := range j.DescriptionSections {
			if sec.Content != "" {
				descParts = append(descParts, stripHTML(sec.Content))
			}
		}
		desc := strings.Join(descParts, "\n")

		if matchesQuery(j.Title, desc, nil, realCompanyName, query) {
			comp := extractHNCompensation(desc)
			locLower := strings.ToLower(j.LocationName)
			out = append(out, Job{
				ID:           "ashby-" + j.ID,
				Title:        j.Title,
				Company:      realCompanyName,
				Location:     j.LocationName,
				Description:  desc,
				Compensation: comp,
				URL:          j.JobURL,
				Source:       "ashby",
				Remote:       strings.Contains(locLower, "remote"),
				ScrapedAt:    time.Now(),
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

	// Workday explicit bypass
	if strings.Contains(careerURL, ".myworkdayjobs.com") {
		log.Printf("🤖 Enterprise Workday ATS Detected via DDG. Hijacking URL for native REST ingestion...")
		wdJobs, ok := d.scrapeWorkday(careerURL, company, query)
		if ok && len(wdJobs) > 0 {
			return wdJobs, true
		}
		log.Printf("⚠️ Native Workday ATS ingestion failed. Falling back to Headless injection...")
	}

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

type workdayResponse struct {
	JobPostings []struct {
		Title          string `json:"title"`
		ExternalPath   string `json:"externalPath"`
		LocationsText  string `json:"locationsText"`
		PostingDate    string `json:"postedOn"`
		SearchScore    int    `json:"searchScore"`
		TimeType       string `json:"timeType"`
		ReqID          string `json:"bulletFields"` // Sometimes maps to the req
	} `json:"jobPostings"`
}

func (d *DirectScraper) scrapeWorkday(careerURL, realCompanyName string, query SearchQuery) ([]Job, bool) {
	// Parse: https://{tenant}.{node}.myworkdayjobs.com/{portal}
	reWD := regexp.MustCompile(`https://([^.]+)\.([^.]+)\.myworkdayjobs\.com/([^/?#]+)`)
	matches := reWD.FindStringSubmatch(careerURL)
	if len(matches) < 4 {
		return nil, false
	}
	tenant := matches[1]
	wdNode := matches[2]
	portal := matches[3]

	apiURL := fmt.Sprintf("https://%s.%s.myworkdayjobs.com/wday/cxs/%s/%s/jobs", tenant, wdNode, tenant, portal)
	baseJobURL := fmt.Sprintf("https://%s.%s.myworkdayjobs.com/%s", tenant, wdNode, portal)

	// Use each keyword as a Workday searchText query for better relevance
	searchTerms := query.Keywords
	if len(searchTerms) == 0 {
		searchTerms = []string{""}
	}

	seen := make(map[string]struct{})
	var out []Job

	for _, term := range searchTerms {
		payload := fmt.Sprintf(`{"appliedFacets":{},"limit":50,"offset":0,"searchText":"%s"}`, term)
		req, _ := http.NewRequest("POST", apiURL, strings.NewReader(payload))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

		resp, err := d.client.Do(req)
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}

		var res workdayResponse
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		log.Printf("🔍 Workday [%s] keyword=%q -> %d results", realCompanyName, term, len(res.JobPostings))

		for _, j := range res.JobPostings {
			if _, ok := seen[j.ExternalPath]; ok {
				continue
			}
			seen[j.ExternalPath] = struct{}{}

			jobURL := baseJobURL + j.ExternalPath
			locLower := strings.ToLower(j.LocationsText)
			idSlugs := strings.Split(j.ExternalPath, "/")

			out = append(out, Job{
				ID:           "wday-" + idSlugs[len(idSlugs)-1],
				Title:        j.Title,
				Company:      realCompanyName,
				Location:     j.LocationsText,
				Description:  fmt.Sprintf("[Workday %s] %s - %s. Visit the link above for the full description.", realCompanyName, j.Title, j.LocationsText),
				Compensation: "",
				URL:          jobURL,
				Source:       "workday",
				Remote:       strings.Contains(locLower, "remote"),
				ScrapedAt:    time.Now(),
			})
		}
	}
	log.Printf("✅ Workday [%s] Complete! %d unique listings.", realCompanyName, len(out))
	return out, len(out) > 0
}
