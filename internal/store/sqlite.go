package store

import (
	"database/sql"
	"fmt"
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
	remote      INTEGER DEFAULT 0,
	scraped_at  DATETIME NOT NULL
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
	db.Exec("ALTER TABLE jobs ADD COLUMN cover_letter TEXT")

	// Backfill FTS index for any existing DB records if the index is brand new
	var ftsCount int
	db.QueryRow("SELECT COUNT(*) FROM jobs_fts").Scan(&ftsCount)
	if ftsCount == 0 {
		fmt.Println(" -> Backfilling existing SQLite records into comprehensive FTS5 index...")
		db.Exec("INSERT INTO jobs_fts(rowid, id, title, company, location, description, compensation, url, source, tags) SELECT rowid, id, title, company, location, description, compensation, url, source, tags FROM jobs;")
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

// CountJobs returns the total number of jobs in the store
func (s *SQLiteStore) CountJobs() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&count)
	return count, err
}

// CountBySource returns a map of source → job count
func (s *SQLiteStore) CountBySource() (map[string]int, error) {
	rows, err := s.db.Query("SELECT source, COUNT(*) FROM jobs GROUP BY source")
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
	rows, err := s.db.Query(`SELECT id, title, company, location, description, compensation, url, source, tags, remote, scraped_at, tailored_resume, tailored_report, fit_brief, market_salary, score, cover_letter FROM jobs WHERE user_id = ? ORDER BY scraped_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []scraper.Job
	for rows.Next() {
		var j scraper.Job
		var tagsStr, scrapedAtStr string
		var comp, location, source, tr, trep, fb, ms, cl sql.NullString
		var remoteInt, score sql.NullInt64

		if err := rows.Scan(&j.ID, &j.Title, &j.Company, &location, &j.Description, &comp, &j.URL, &source, &tagsStr, &remoteInt, &scrapedAtStr, &tr, &trep, &fb, &ms, &score, &cl); err != nil {
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
		j.CoverLetter = cl.String
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
	row := s.db.QueryRow(`SELECT id, title, company, location, description, compensation, url, source, tags, remote, scraped_at, tailored_resume, tailored_report, fit_brief, market_salary, score, cover_letter FROM jobs WHERE id = ? AND user_id = ?`, id, userID)
	var j scraper.Job
	var tagsStr, scrapedAtStr string
	var comp, location, source, tr, trep, fb, ms, cl sql.NullString
	var remoteInt, score sql.NullInt64

	if err := row.Scan(&j.ID, &j.Title, &j.Company, &location, &j.Description, &comp, &j.URL, &source, &tagsStr, &remoteInt, &scrapedAtStr, &tr, &trep, &fb, &ms, &score, &cl); err != nil {
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
	j.CoverLetter = cl.String
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
		SELECT j.id, j.title, j.company, j.location, j.description, j.compensation, j.url, j.source, j.tags, j.remote, j.scraped_at, j.tailored_resume, j.tailored_report, j.fit_brief, j.market_salary, j.score, j.cover_letter
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
		var comp, location, source, tr, trep, fb, ms, cl sql.NullString
		var remoteInt, score sql.NullInt64

		if err := rows.Scan(&j.ID, &j.Title, &j.Company, &location, &j.Description, &comp, &j.URL, &source, &tagsStr, &remoteInt, &scrapedAtStr, &tr, &trep, &fb, &ms, &score, &cl); err != nil {
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
		j.CoverLetter = cl.String
		if tagsStr != "" {
			j.Tags = strings.Split(tagsStr, ",")
		}
		j.Remote = remoteInt.Int64 == 1
		j.ScrapedAt, _ = time.Parse(time.RFC3339, scrapedAtStr)
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// SaveTailoredResult permanently associates an LLM generated layout with a specific Tenant's Job
func (s *SQLiteStore) SaveTailoredResult(userID string, id string, resume string, report string, brief string, salary string, score int, coverLetter string) error {
	_, err := s.db.Exec(`
		UPDATE jobs
		SET tailored_resume = ?, tailored_report = ?, fit_brief = ?, market_salary = ?, score = ?, cover_letter = ?
		WHERE id = ? AND user_id = ?
	`, resume, report, brief, salary, score, coverLetter, id, userID)
	return err
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

// Close closes the underlying database connection
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
