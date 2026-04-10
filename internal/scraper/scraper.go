package scraper

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// Manager orchestrates multiple job board scrapers concurrently
type Manager struct {
	scrapers []Scraper
}

// NewManager creates a manager with all registered scrapers
func NewManager() *Manager {
	return &Manager{
		scrapers: []Scraper{
			NewRemoteOKScraper(),
			NewHNScraper(),
			NewDirectScraper(),
		},
	}
}

// InitScraper initializes the manager and returns it for use in main
func InitScraper() (*Manager, error) {
	fmt.Println(" -> Initializing Scraper Layer...")
	m := NewManager()
	names := ""
	for i, s := range m.scrapers {
		if i > 0 {
			names += ", "
		}
		names += s.Name()
	}
	fmt.Printf(" ✅ Scraper ready with %d sources: [%s]\n", len(m.scrapers), names)
	return m, nil
}

// ScrapeAll runs all registered scrapers concurrently and returns deduplicated results
func (m *Manager) ScrapeAll(ctx context.Context, query SearchQuery) ([]Job, error) {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		all  []Job
	)

	for _, s := range m.scrapers {
		wg.Add(1)
		go func(s Scraper) {
			defer wg.Done()
			log.Printf("🔍 Scraping source: %s...\n", s.Name())
			jobs, err := s.Scrape(ctx, query)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				log.Printf("⚠️  [%s] scraper error: %v\n", s.Name(), err)
				return
			}
			log.Printf("✅ [%s] returned %d matching jobs\n", s.Name(), len(jobs))
			all = append(all, jobs...)
		}(s)
	}

	wg.Wait()
	deduped := deduplicate(all)
	log.Printf("📦 Deduplication: %d → %d unique jobs\n", len(all), len(deduped))
	return deduped, nil
}

// deduplicate removes jobs with identical URLs
func deduplicate(jobs []Job) []Job {
	seen := make(map[string]struct{})
	var out []Job
	for _, j := range jobs {
		if _, ok := seen[j.URL]; !ok {
			seen[j.URL] = struct{}{}
			out = append(out, j)
		}
	}
	return out
}
