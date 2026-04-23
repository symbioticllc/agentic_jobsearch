package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/leee/agentic-jobs/internal/scraper"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGO needed
)

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL,
	title        TEXT NOT NULL,
	company      TEXT NOT NULL,
	location     TEXT,
	description  TEXT,
	compensation TEXT,
	url          TEXT UNIQUE NOT NULL,
	source      TEXT,
	tags        TEXT,
	remote           INTEGER DEFAULT 0,
	tailoring_status TEXT DEFAULT 'pending',
	scraped_at      DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_source ON jobs(source);
CREATE INDEX IF NOT EXISTS idx_jobs_company ON jobs(company);

-- FTS5 Full Text Search setup
CREATE VIRTUAL TABLE IF NOT EXISTS jobs_fts USING fts5(
	id UNINDEXED,
	title,
	company,
	location,
	description,
	compensation,
	url UNINDEXED,
	source,
	tags,
	content='jobs',
	content_rowid='rowid'
);

-- Triggers to keep FTS table continuously in sync
CREATE TRIGGER IF NOT EXISTS jobs_ai AFTER INSERT ON jobs BEGIN
  INSERT INTO jobs_fts(rowid, id, title, company, location, description, compensation, url, source, tags) 
  VALUES (new.rowid, new.id, new.title, new.company, new.location, new.description, new.compensation, new.url, new.source, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS jobs_ad AFTER DELETE ON jobs BEGIN
  INSERT INTO jobs_fts(jobs_fts, rowid, id, title, company, location, description, compensation, url, source, tags) 
  VALUES('delete', old.rowid, old.id, old.title, old.company, old.location, old.description, old.compensation, old.url, old.source, old.tags);
END;

CREATE TRIGGER IF NOT EXISTS jobs_au AFTER UPDATE ON jobs BEGIN
  INSERT INTO jobs_fts(jobs_fts, rowid, id, title, company, location, description, compensation, url, source, tags) 
  VALUES('delete', old.rowid, old.id, old.title, old.company, old.location, old.description, old.compensation, old.url, old.source, old.tags);
  INSERT INTO jobs_fts(rowid, id, title, company, location, description, compensation, url, source, tags) 
  VALUES (new.rowid, new.id, new.title, new.company, new.location, new.description, new.compensation, new.url, new.source, new.tags);
END;

CREATE TABLE IF NOT EXISTS settings (
	user_id TEXT NOT NULL,
	key TEXT NOT NULL,
	value TEXT NOT NULL,
	PRIMARY KEY(user_id, key)
);
`

// SQLiteStore persists scraped jobs using a local SQLite database
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) the SQLite DB at the given path and applies the schema
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite at %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply schema: %w", err)
	}
	db.Exec("ALTER TABLE jobs ADD COLUMN compensation TEXT") // Ignore error if column exists
	db.Exec("ALTER TABLE jobs ADD COLUMN tailored_resume TEXT")
	db.Exec("ALTER TABLE jobs ADD COLUMN tailored_report TEXT")
	db.Exec("ALTER TABLE jobs ADD COLUMN fit_brief TEXT")
	db.Exec("ALTER TABLE jobs ADD COLUMN market_salary TEXT")
	db.Exec("ALTER TABLE jobs ADD COLUMN score INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE jobs ADD COLUMN sub_score_tech INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE jobs ADD COLUMN sub_score_domain INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE jobs ADD COLUMN sub_score_senior INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE jobs ADD COLUMN sub_score_location INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE jobs ADD COLUMN sub_score_lateral INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE jobs ADD COLUMN cover_letter TEXT")
	db.Exec("ALTER TABLE jobs ADD COLUMN tailoring_status TEXT DEFAULT 'pending'")
	db.Exec("ALTER TABLE jobs ADD COLUMN applied INTEGER DEFAULT 0")

	// Tailoring history — preserves scores from every tailoring pass
	db.Exec(`CREATE TABLE IF NOT EXISTS tailoring_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT NOT NULL,
		job_id TEXT NOT NULL,
		score INTEGER DEFAULT 0,
		sub_score_tech INTEGER DEFAULT 0,
		sub_score_domain INTEGER DEFAULT 0,
		sub_score_senior INTEGER DEFAULT 0,
		market_salary TEXT,
		fit_brief TEXT,
		template_used TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (job_id) REFERENCES jobs(id)
	)`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_th_job ON tailoring_history(job_id, user_id)")

	// Backfill FTS index for any existing DB records if the index is brand new
	var ftsCount int
	db.QueryRow("SELECT COUNT(*) FROM jobs_fts").Scan(&ftsCount)
	if ftsCount == 0 {
		fmt.Println(" -> Backfilling existing SQLite records into comprehensive FTS5 index...")
		db.Exec("INSERT INTO jobs_fts(rowid, id, title, company, location, description, compensation, url, source, tags) SELECT rowid, id, title, company, location, description, compensation, url, source, tags FROM jobs;")
	}

	// On startup: reset any jobs that were stuck 'processing' from a previous crash.
	// These will never complete — mark them 'failed' so the UI stops polling.
	res, resetErr := db.Exec(`UPDATE jobs SET tailoring_status = 'failed' WHERE tailoring_status = 'processing'`)
	if resetErr == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			fmt.Printf(" ⚠️  Reset %d stale 'processing' tailoring job(s) to 'failed'\n", n)
		}
	}

	fmt.Printf(" ✅ SQLite store ready at: %s\n", path)
	return &SQLiteStore{db: db}, nil
}

// SaveJobs inserts new jobs, ignoring duplicates (keyed by URL)
func (s *SQLiteStore) SaveJobs(userID string, jobs []scraper.Job) (int, error) {
	stmt, err := s.db.Prepare(`
		INSERT OR REPLACE INTO jobs 
			(id, user_id, title, company, location, description, compensation, url, source, tags, remote, scraped_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer stmt.Close()

	inserted := 0
	for _, j := range jobs {
		tags := strings.Join(j.Tags, ",")
		remote := 0
		if j.Remote {
			remote = 1
		}
		res, err := stmt.Exec(
			j.ID, userID, j.Title, j.Company, j.Location, j.Description, j.Compensation,
			j.URL, j.Source, tags, remote, j.ScrapedAt.Format(time.RFC3339),
		)
		if err != nil {
			continue
		}
		rows, _ := res.RowsAffected()
		inserted += int(rows)
	}
	return inserted, nil
}

// ClearTenantData permanently deletes all jobs and tailoring history for a specific user.
func (s *SQLiteStore) ClearTenantData(userID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM jobs WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tailoring_history WHERE user_id = ?`, userID); err != nil {
		return err
	}

	return tx.Commit()
}

// CountJobs returns the total number of jobs in the store
func (s *SQLiteStore) CountJobs() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&count)
	return count, err
}

// CountBySource returns a map of source → job count scoped to a tenant
func (s *SQLiteStore) CountBySource(userID string) (map[string]int, error) {
	rows, err := s.db.Query("SELECT source, COUNT(*) FROM jobs WHERE user_id = ? GROUP BY source ORDER BY COUNT(*) DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			continue
		}
		result[source] = count
	}
	return result, nil
}

// GetAllJobs returns all jobs stored in the database
func (s *SQLiteStore) GetAllJobs(userID string) ([]scraper.Job, error) {
	rows, err := s.db.Query(`SELECT id, title, company, location, description, compensation, url, source, tags, remote, scraped_at, tailored_resume, tailored_report, fit_brief, market_salary, score, sub_score_tech, sub_score_domain, sub_score_senior, sub_score_location, sub_score_lateral, cover_letter, tailoring_status, applied FROM jobs WHERE user_id = ? ORDER BY scraped_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []scraper.Job
	for rows.Next() {
		var j scraper.Job
		var tagsStr, scrapedAtStr string
		var comp, location, source, tr, trep, fb, ms, cl, status sql.NullString
		var remoteInt, score, st, sd, ss, loc, lat, applied sql.NullInt64

		if err := rows.Scan(&j.ID, &j.Title, &j.Company, &location, &j.Description, &comp, &j.URL, &source, &tagsStr, &remoteInt, &scrapedAtStr, &tr, &trep, &fb, &ms, &score, &st, &sd, &ss, &loc, &lat, &cl, &status, &applied); err != nil {
			continue
		}

		j.Compensation = comp.String
		j.Location = location.String
		j.Source = source.String
		j.TailoredResume = tr.String
		j.TailoredReport = trep.String
		j.FitBrief = fb.String
		j.MarketSalary = ms.String
		j.CoverLetter = cl.String
		j.TailoringStatus = status.String
		j.Applied = applied.Int64 == 1
		j.Score = int(score.Int64)
		j.SubScoreTech = int(st.Int64)
		j.SubScoreDomain = int(sd.Int64)
		j.SubScoreSenior = int(ss.Int64)
		j.SubScoreLocation = int(loc.Int64)
		j.SubScoreLateral = int(lat.Int64)
		if tagsStr != "" {
			j.Tags = strings.Split(tagsStr, ",")
		}
		j.Remote = remoteInt.Int64 == 1
		j.ScrapedAt, _ = time.Parse(time.RFC3339, scrapedAtStr)
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// GetJobByID returns a single job by its ID
func (s *SQLiteStore) GetJobByID(userID string, id string) (scraper.Job, error) {
	row := s.db.QueryRow(`SELECT id, title, company, location, description, compensation, url, source, tags, remote, scraped_at, tailored_resume, tailored_report, fit_brief, market_salary, score, sub_score_tech, sub_score_domain, sub_score_senior, sub_score_location, sub_score_lateral, cover_letter, tailoring_status, applied FROM jobs WHERE id = ? AND user_id = ?`, id, userID)
	var j scraper.Job
	var tagsStr, scrapedAtStr string
	var comp, location, source, tr, trep, fb, ms, cl, status sql.NullString
	var remoteInt, score, st, sd, sss, sloc, slat, applied sql.NullInt64

	if err := row.Scan(&j.ID, &j.Title, &j.Company, &location, &j.Description, &comp, &j.URL, &source, &tagsStr, &remoteInt, &scrapedAtStr, &tr, &trep, &fb, &ms, &score, &st, &sd, &sss, &sloc, &slat, &cl, &status, &applied); err != nil {
		return j, err
	}

	j.Compensation = comp.String
	j.Location = location.String
	j.Source = source.String
	j.TailoredResume = tr.String
	j.TailoredReport = trep.String
	j.FitBrief = fb.String
	j.MarketSalary = ms.String
	j.Score = int(score.Int64)
	j.SubScoreTech = int(st.Int64)
	j.SubScoreDomain = int(sd.Int64)
	j.SubScoreSenior = int(sss.Int64)
	j.SubScoreLocation = int(sloc.Int64)
	j.SubScoreLateral = int(slat.Int64)
	j.CoverLetter = cl.String
	j.TailoringStatus = status.String
	j.Applied = applied.Int64 == 1
	if tagsStr != "" {
		j.Tags = strings.Split(tagsStr, ",")
	}
	j.Remote = remoteInt.Int64 == 1
	j.ScrapedAt, _ = time.Parse(time.RFC3339, scrapedAtStr)
	return j, nil
}

// SearchFTS performs a blazingly fast full-text match across local jobs scoped to a tenant
func (s *SQLiteStore) SearchFTS(userID string, query string) ([]scraper.Job, error) {
	// Simple escaping: wrap query in quotes to handle spaces functionally as exact phrasing
	escapedQuery := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`

	rows, err := s.db.Query(`
		SELECT j.id, j.title, j.company, j.location, j.description, j.compensation, j.url, j.source, j.tags, j.remote, j.scraped_at, j.tailored_resume, j.tailored_report, j.fit_brief, j.market_salary, j.score, j.sub_score_tech, j.sub_score_domain, j.sub_score_senior, j.sub_score_location, j.sub_score_lateral, j.cover_letter, j.tailoring_status, j.applied
		FROM jobs_fts f
		JOIN jobs j ON f.rowid = j.rowid
		WHERE jobs_fts MATCH ? AND j.user_id = ?
		ORDER BY rank
		LIMIT 25
	`, escapedQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []scraper.Job
	for rows.Next() {
		var j scraper.Job
		var tagsStr, scrapedAtStr string
		var comp, location, source, tr, trep, fb, ms, cl, status sql.NullString
		var remoteInt, score, st, sd, sss, sloc, slat, applied sql.NullInt64

		if err := rows.Scan(&j.ID, &j.Title, &j.Company, &location, &j.Description, &comp, &j.URL, &source, &tagsStr, &remoteInt, &scrapedAtStr, &tr, &trep, &fb, &ms, &score, &st, &sd, &sss, &sloc, &slat, &cl, &status, &applied); err != nil {
			continue
		}

		j.Compensation = comp.String
		j.Location = location.String
		j.Source = source.String
		j.TailoredResume = tr.String
		j.TailoredReport = trep.String
		j.FitBrief = fb.String
		j.MarketSalary = ms.String
		j.Score = int(score.Int64)
		j.SubScoreTech = int(st.Int64)
		j.SubScoreDomain = int(sd.Int64)
		j.SubScoreSenior = int(sss.Int64)
		j.SubScoreLocation = int(sloc.Int64)
		j.SubScoreLateral = int(slat.Int64)
		j.CoverLetter = cl.String
		j.TailoringStatus = status.String
		j.Applied = applied.Int64 == 1
		if tagsStr != "" {
			j.Tags = strings.Split(tagsStr, ",")
		}
		j.Remote = remoteInt.Int64 == 1
		j.ScrapedAt, _ = time.Parse(time.RFC3339, scrapedAtStr)
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// UpdateTailoringStatus updates the tracking state of a background job
func (s *SQLiteStore) UpdateTailoringStatus(userID, id, status string) error {
	_, err := s.db.Exec(`UPDATE jobs SET tailoring_status = ? WHERE id = ? AND user_id = ?`, status, id, userID)
	return err
}

// UpdateAppliedStatus updates the tracking state of an applied job marker
func (s *SQLiteStore) UpdateAppliedStatus(userID, id string, applied bool) error {
	val := 0
	if applied {
		val = 1
	}
	_, err := s.db.Exec(`UPDATE jobs SET applied = ? WHERE id = ? AND user_id = ?`, val, id, userID)
	return err
}

// SaveTailoredResult permanently associates an LLM generated layout with a specific Tenant's Job.
// It also logs the scores to tailoring_history so previous results are preserved.
func (s *SQLiteStore) SaveTailoredResult(userID string, id string, resume string, report string, brief string, salary string, score int, st int, sd int, ss int, sloc int, slat int, coverLetter string) error {
	// 1. Log to history table (preserves every tailoring pass)
	s.db.Exec(`
		INSERT INTO tailoring_history (user_id, job_id, score, sub_score_tech, sub_score_domain, sub_score_senior, market_salary, fit_brief)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, userID, id, score, st, sd, ss, salary, brief)

	// 2. Update the active job record with latest results
	_, err := s.db.Exec(`
		UPDATE jobs
		SET tailored_resume = ?, tailored_report = ?, fit_brief = ?, market_salary = ?, score = ?, sub_score_tech = ?, sub_score_domain = ?, sub_score_senior = ?, sub_score_location = ?, sub_score_lateral = ?, cover_letter = ?, tailoring_status = 'completed', tags = CASE WHEN tags LIKE '%tailored%' THEN tags WHEN tags = '' OR tags IS NULL THEN 'tailored' ELSE tags || ',tailored' END
		WHERE id = ? AND user_id = ?
	`, resume, report, brief, salary, score, st, sd, ss, sloc, slat, coverLetter, id, userID)
	return err
}

// TailoringHistoryRow represents one historical tailoring attempt
type TailoringHistoryRow struct {
	ID             int    `json:"id"`
	Score          int    `json:"score"`
	SubScoreTech   int    `json:"sub_score_tech"`
	SubScoreDomain int    `json:"sub_score_domain"`
	SubScoreSenior int    `json:"sub_score_senior"`
	MarketSalary   string `json:"market_salary"`
	FitBrief       string `json:"fit_brief"`
	CreatedAt      string `json:"created_at"`
}

// GetTailoringHistory returns all previous tailoring attempts for a given job
func (s *SQLiteStore) GetTailoringHistory(userID string, jobID string) ([]TailoringHistoryRow, error) {
	rows, err := s.db.Query(`
		SELECT id, score, sub_score_tech, sub_score_domain, sub_score_senior, 
		       COALESCE(market_salary,''), COALESCE(fit_brief,''), created_at
		FROM tailoring_history
		WHERE user_id = ? AND job_id = ?
		ORDER BY created_at DESC
	`, userID, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []TailoringHistoryRow
	for rows.Next() {
		var h TailoringHistoryRow
		if err := rows.Scan(&h.ID, &h.Score, &h.SubScoreTech, &h.SubScoreDomain, &h.SubScoreSenior, &h.MarketSalary, &h.FitBrief, &h.CreatedAt); err == nil {
			history = append(history, h)
		}
	}
	return history, nil
}

// Setup and Configuration Logic maps to specific explicitly routed users
func (s *SQLiteStore) SaveSetting(userID string, key string, value string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO settings (user_id, key, value) VALUES (?, ?, ?)`, userID, key, value)
	return err
}

func (s *SQLiteStore) GetSetting(userID string, key string) (string, error) {
	var val string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE user_id = ? AND key = ?`, userID, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil // Safely return empty if setting hasn't been mapped yet
	}
	return val, err
}

// CompanyReportRow contains per-company scrape statistics for a tenant
type CompanyReportRow struct {
	Company      string `json:"company"`
	TotalJobs    int    `json:"total_jobs"`
	TailoredCount int   `json:"tailored_count"`
	AppliedCount  int   `json:"applied_count"`
}

// GetCompanyReport returns aggregated statistics grouped by company for a specific tenant
func (s *SQLiteStore) GetCompanyReport(userID string) ([]CompanyReportRow, error) {
	rows, err := s.db.Query(`
		SELECT company, COUNT(*) as total,
		       SUM(CASE WHEN tailoring_status = 'completed' THEN 1 ELSE 0 END) as tailored,
			   SUM(applied) as applied
		FROM jobs
		WHERE user_id = ?
		GROUP BY company
		ORDER BY total DESC, company ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var report []CompanyReportRow
	for rows.Next() {
		var row CompanyReportRow
		if err := rows.Scan(&row.Company, &row.TotalJobs, &row.TailoredCount, &row.AppliedCount); err != nil {
			continue
		}
		report = append(report, row)
	}
	return report, nil
}
// TrendDataPoint represents a single day's aggregated job data
type TrendDataPoint struct {
	Date       string `json:"date"`
	Total      int    `json:"total"`
	Company    string `json:"company,omitempty"`
	Source     string `json:"source,omitempty"`
}

// TopPayingCompanyRow holds max-salary data for a company
type TopPayingCompanyRow struct {
	Company    string `json:"company"`
	MaxSalary  int    `json:"max_salary"`   // highest parsed value in thousands
	SalaryText string `json:"salary_text"`  // human-readable original text
	JobCount   int    `json:"job_count"`
}

// TopPayingJobRow holds salary data for an individual job listing
type TopPayingJobRow struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Company    string `json:"company"`
	MaxSalary  int    `json:"max_salary"`
	SalaryText string `json:"salary_text"`
	Source     string `json:"source"` // "compensation" or "market_salary"
}

// CategoryRow holds job count per role category
type CategoryRow struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
	AvgSalary int   `json:"avg_salary"` // 0 if unknown
}

// TrendReport contains all time-series data for the trends dashboard
type TrendReport struct {
	DailyTotals        []TrendDataPoint      `json:"daily_totals"`
	DailyByCompany     []TrendDataPoint      `json:"daily_by_company"`
	DailyBySource      []TrendDataPoint      `json:"daily_by_source"`
	TopCompanies       []CompanyReportRow    `json:"top_companies"`
	TopPayingCompanies []TopPayingCompanyRow `json:"top_paying_companies"`
	TopPayingJobs      []TopPayingJobRow     `json:"top_paying_jobs"`
	Categories         []CategoryRow         `json:"categories"`
}

// GetTrendData returns time-series job data for trend analysis charts
func (s *SQLiteStore) GetTrendData(userID string) (*TrendReport, error) {
	report := &TrendReport{}

	// 1. Daily totals (all jobs per day)
	rows, err := s.db.Query(`
		SELECT DATE(scraped_at) as day, COUNT(*) as cnt
		FROM jobs WHERE user_id = ?
		GROUP BY DATE(scraped_at)
		ORDER BY day ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var dp TrendDataPoint
		if err := rows.Scan(&dp.Date, &dp.Total); err == nil {
			report.DailyTotals = append(report.DailyTotals, dp)
		}
	}

	// 2. Daily by top companies (top 10 by volume)
	rows2, err := s.db.Query(`
		SELECT DATE(scraped_at) as day, company, COUNT(*) as cnt
		FROM jobs
		WHERE user_id = ? AND company IN (
			SELECT company FROM jobs WHERE user_id = ?
			GROUP BY company ORDER BY COUNT(*) DESC LIMIT 10
		)
		GROUP BY DATE(scraped_at), company
		ORDER BY day ASC, cnt DESC
	`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var dp TrendDataPoint
		if err := rows2.Scan(&dp.Date, &dp.Company, &dp.Total); err == nil {
			report.DailyByCompany = append(report.DailyByCompany, dp)
		}
	}

	// 3. Daily by source
	rows3, err := s.db.Query(`
		SELECT DATE(scraped_at) as day, source, COUNT(*) as cnt
		FROM jobs WHERE user_id = ?
		GROUP BY DATE(scraped_at), source
		ORDER BY day ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows3.Close()
	for rows3.Next() {
		var dp TrendDataPoint
		if err := rows3.Scan(&dp.Date, &dp.Source, &dp.Total); err == nil {
			report.DailyBySource = append(report.DailyBySource, dp)
		}
	}

	// 4. Top companies for the pie/bar chart
	report.TopCompanies, _ = s.GetCompanyReport(userID)

	// 5. Top paying companies
	report.TopPayingCompanies, _ = s.getTopPayingCompanies(userID)

	// 6. Top paying individual jobs
	report.TopPayingJobs, _ = s.getTopPayingJobs(userID)

	// 7. Job category breakdown
	report.Categories, _ = s.getCategoryBreakdown(userID)

	return report, nil
}

// getTopPayingJobs ranks individual job listings by parsed salary, returning up to 20
func (s *SQLiteStore) getTopPayingJobs(userID string) ([]TopPayingJobRow, error) {
	rows, err := s.db.Query(`
		SELECT id, title, company,
			COALESCE(compensation,'') as comp,
			COALESCE(market_salary,'') as ms
		FROM jobs
		WHERE user_id = ? AND (compensation != '' OR market_salary != '')
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rawJob struct {
		id, title, company, comp, ms string
	}
	var rawJobs []rawJob
	for rows.Next() {
		var r rawJob
		if err := rows.Scan(&r.id, &r.title, &r.company, &r.comp, &r.ms); err == nil {
			rawJobs = append(rawJobs, r)
		}
	}

	var result []TopPayingJobRow
	for _, r := range rawJobs {
		// Prefer market_salary (LLM-derived, more reliable) over raw scraped compensation
		source := "compensation"
		text := r.comp
		if r.ms != "" {
			text = r.ms
			source = "market_salary"
		}
		val, snippet := parseBestSalary(text)
		if val > 0 {
			result = append(result, TopPayingJobRow{
				ID:        r.id,
				Title:     r.title,
				Company:   r.company,
				MaxSalary: val,
				SalaryText: snippet,
				Source:    source,
			})
		}
	}

	// Sort descending
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].MaxSalary > result[i].MaxSalary {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	if len(result) > 20 {
		result = result[:20]
	}
	return result, nil
}

// getCategoryBreakdown buckets jobs by role category inferred from title keywords
func (s *SQLiteStore) getCategoryBreakdown(userID string) ([]CategoryRow, error) {
	rows, err := s.db.Query(`
		SELECT title, COALESCE(compensation,''), COALESCE(market_salary,'')
		FROM jobs WHERE user_id = ?
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type catData struct {
		count     int
		salaries  []int
	}
	cats := map[string]*catData{}

	categoryOrder := []string{
		"Executive / VP",
		"Director",
		"Engineering Manager",
		"Staff / Principal IC",
		"Senior Engineer",
		"Product Management",
		"Platform / Infrastructure",
		"Security",
		"Data / ML",
		"Other",
	}
	for _, c := range categoryOrder {
		cats[c] = &catData{}
	}

	classify := func(title string) string {
		t := strings.ToLower(title)
		switch {
		case containsAny(t, "chief", " cto", " cio", " ciso", "vp ", "vp,", "vice president", "svp", "evp", "managing director"):
			return "Executive / VP"
		case containsAny(t, "director"):
			return "Director"
		case containsAny(t, "engineering manager", "head of engineering", "head of platform", "head of infra"):
			return "Engineering Manager"
		case containsAny(t, "staff ", "principal ", "distinguished ", "fellow"):
			return "Staff / Principal IC"
		case containsAny(t, "senior ", "sr.", "sr "):
			return "Senior Engineer"
		case containsAny(t, "product manager", "product management", "product owner", "product specialist", "product lead"):
			return "Product Management"
		case containsAny(t, "platform", "infrastructure", "devops", "sre", "reliability", "site reliability"):
			return "Platform / Infrastructure"
		case containsAny(t, "security", "vulnerability", "compliance", "ciso", "soc "):
			return "Security"
		case containsAny(t, "data", "machine learning", "ml ", "ai ", "analytics", "scientist"):
			return "Data / ML"
		default:
			return "Other"
		}
	}

	for rows.Next() {
		var title, comp, ms string
		if err := rows.Scan(&title, &comp, &ms); err != nil {
			continue
		}
		cat := classify(title)
		cats[cat].count++

		// Parse salary if available
		text := comp
		if ms != "" {
			text = ms
		}
		if text != "" {
			if val, _ := parseBestSalary(text); val > 0 {
				cats[cat].salaries = append(cats[cat].salaries, val)
			}
		}
	}

	var result []CategoryRow
	for _, name := range categoryOrder {
		d := cats[name]
		if d.count == 0 {
			continue
		}
		avg := 0
		if len(d.salaries) > 0 {
			sum := 0
			for _, s := range d.salaries {
				sum += s
			}
			avg = sum / len(d.salaries)
		}
		result = append(result, CategoryRow{Category: name, Count: d.count, AvgSalary: avg})
	}
	return result, nil
}

// containsAny returns true if s contains any of the given substrings
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// getTopPayingCompanies parses salary figures from compensation and market_salary fields.
// SQLite lacks regex, so we fetch the raw text and parse in Go.
func (s *SQLiteStore) getTopPayingCompanies(userID string) ([]TopPayingCompanyRow, error) {
	// Collect per-company: all compensation snippets and job count
	rows, err := s.db.Query(`
		SELECT
			company,
			COUNT(*) as job_count,
			GROUP_CONCAT(COALESCE(compensation,''), '|||') as comp_texts,
			GROUP_CONCAT(COALESCE(market_salary,''), '|||') as salary_texts
		FROM jobs
		WHERE user_id = ? AND company != ''
		GROUP BY company
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type raw struct {
		company     string
		jobCount    int
		compTexts   string
		salaryTexts string
	}
	var raws []raw
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.company, &r.jobCount, &r.compTexts, &r.salaryTexts); err == nil {
			raws = append(raws, r)
		}
	}

	// Parse salary numbers from combined text
	var result []TopPayingCompanyRow
	for _, r := range raws {
		combined := r.compTexts + "|||" + r.salaryTexts
		maxVal, bestText := parseBestSalary(combined)
		if maxVal > 0 {
			result = append(result, TopPayingCompanyRow{
				Company:    r.company,
				MaxSalary:  maxVal,
				SalaryText: bestText,
				JobCount:   r.jobCount,
			})
		}
	}

	// Sort descending by max salary
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].MaxSalary > result[i].MaxSalary {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	// Return top 15
	if len(result) > 15 {
		result = result[:15]
	}
	return result, nil
}

// parseBestSalary scans a block of text (pipe-separated) looking for salary numbers.
// Returns the highest value found (in thousands) and the matching text snippet.
func parseBestSalary(text string) (int, string) {
	// Match patterns like: $350k, $350,000, $350K, 350000, 350k
	numRe := regexp.MustCompile(`\$(\d[\d,]*)[kK]?|\b(\d{3,})[kK]\b|\b(\d{5,})\b`)

	best := 0
	bestSnippet := ""

	for _, chunk := range strings.Split(text, "|||") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		matches := numRe.FindAllStringSubmatch(chunk, -1)
		for _, m := range matches {
			var raw string
			if m[1] != "" {
				raw = strings.ReplaceAll(m[1], ",", "")
			} else if m[2] != "" {
				raw = m[2]
			} else if m[3] != "" {
				raw = m[3]
			}
			if raw == "" {
				continue
			}
			val := 0
			fmt.Sscanf(raw, "%d", &val)
			// Normalise: if value looks like raw dollars (>= 10000), convert to thousands
			if val >= 10000 {
				val = val / 1000
			}
			// Sanity bounds: $30k–$2000k
			if val < 30 || val > 2000 {
				continue
			}
			if val > best {
				best = val
				// Trim the snippet to 40 chars for display
				snippet := chunk
				if len(snippet) > 40 {
					snippet = snippet[:40] + "…"
				}
				bestSnippet = snippet
			}
		}
	}
	return best, bestSnippet
}

// Close closes the underlying database connection
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
