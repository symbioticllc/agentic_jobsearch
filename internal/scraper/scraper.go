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
			NewJSearchScraper(),
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

// SourceNames returns the names of all registered scrapers.
func (m *Manager) SourceNames() []string {
	names := make([]string, len(m.scrapers))
	for i, s := range m.scrapers {
		names[i] = s.Name()
	}
	return names
}

// ProgressCallback is invoked incrementally.
// sourceName is the scraper name, found is the batch job count, isDone indicates completion, isErr indicates failure.
type ProgressCallback func(sourceName string, found int, isDone bool, isErr bool)

// ScrapeAll runs all registered scrapers concurrently and returns deduplicated results.
func (m *Manager) ScrapeAll(ctx context.Context, query SearchQuery) ([]Job, error) {
	return m.ScrapeAllWithProgress(ctx, query, nil)
}

// ScrapeAllWithProgress runs all scrapers concurrently, invoking cb incrementally.
func (m *Manager) ScrapeAllWithProgress(ctx context.Context, query SearchQuery, cb ProgressCallback) ([]Job, error) {
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
			
			jobs, err := s.Scrape(ctx, query, func(batch []Job) {
				if cb != nil {
					cb(s.Name(), len(batch), false, false)
				}
			})
			
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				log.Printf("⚠️  [%s] scraper error: %v\n", s.Name(), err)
				if cb != nil {
					cb(s.Name(), 0, true, true)
				}
				return
			}
			log.Printf("✅ [%s] returned %d matching jobs\n", s.Name(), len(jobs))
			all = append(all, jobs...)
			if cb != nil {
				cb(s.Name(), 0, true, false)
			}
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
