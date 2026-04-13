package aligner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/leee/agentic-jobs/internal/llm"
	"github.com/leee/agentic-jobs/internal/rag"
	"github.com/leee/agentic-jobs/internal/scraper"
)

// stripThinkTags removes Qwen3 <think>...</think> chain-of-thought blocks
// from the raw LLM response before structured parsing begins.
func stripThinkTags(s string) string {
	re := regexp.MustCompile(`(?s)<think>.*?</think>`)
	return strings.TrimSpace(re.ReplaceAllString(s, ""))
}

// TailoredResult contains all the outputs of the tailoring process
type TailoredResult struct {
	Score          int
	SubScores      map[string]int
	MarketSalary   string
	FitBrief       string
	TailoredResume string
	Report         string
	CoverLetter    string
}

// Aligner handles the logic of tailoring a resume for a specific job
type Aligner struct {
	rag *rag.RAG
}

// NewAligner creates a new aligner instance
func NewAligner(r *rag.RAG) *Aligner {
	return &Aligner{
		rag: r,
	}
}

// TailorResume generates a tailored resume for a given job and returns a structured result
func (a *Aligner) TailorResume(ctx context.Context, job scraper.Job, baseResume string, linkedinUrl string, customInstructions string) (TailoredResult, error) {
	var result TailoredResult

	// 1. Get relevant context from RAG
	jdSnippet := job.Title + "\n" + job.Description
	if len(jdSnippet) > 2000 {
		jdSnippet = jdSnippet[:2000]
	}

	ragResults, err := a.rag.QueryRelevant(ctx, "project_history", jdSnippet, 5)
	if err != nil {
		return result, fmt.Errorf("rag query failed: %w", err)
	}

	var contextBuf strings.Builder
	for _, res := range ragResults {
		contextBuf.WriteString(fmt.Sprintf("\n### %s\n%s\n", res.Chunk.Header, res.Chunk.Content))
	}

	compStr := job.Compensation
	if compStr == "" {
		compStr = "Not provided"
	}

	// 2. Formulate the prompt
	var customInstrBlock string
	if customInstructions != "" {
		customInstrBlock = fmt.Sprintf("\n### 🌱 USER REVISION INSTRUCTIONS\nThe candidate explicitly requested the following adjustments for this generation:\n\"%s\"\nMake sure to respect these instructions while maintaining the format constraints.\n", customInstructions)
	}

	prompt := fmt.Sprintf(`
You are an expert career coach and technical recruiter. Your task is to analyze a job description and tailor a professional resume.%s

### IMMUTABLE FACTS — DO NOT CHANGE THESE UNDER ANY CIRCUMSTANCES
The following elements from the Base Resume are SACRED and must appear EXACTLY as written:
- **Employer names**: Capital One Financial, Bitso, Bank of America, Wachovia Securities, Symbiotic LLC, City of Richmond, etc. — copy them VERBATIM.
- **Job titles**: Head of Payment Solutions, Sr. Enterprise Architect, Distinguished Engineer, VP Head of Payment Solutions, Design Team Lead, Solution Architect, Principal, etc. — use EXACTLY as provided.
- **Employment dates**: Preserve all date ranges exactly (e.g., 2022–Present, 2010–2022, Feb 2022–Aug 2022, etc.).
- **Degrees and universities**: Pennsylvania State University, Virginia Commonwealth University — use EXACT names and years.
- **Patents**: All US patent numbers must be preserved exactly as listed.
- **Certifications**: LSSBB, CISSP, AWS-ASA/ADA, CDMP, etc. — use exact acronyms and descriptions.
- **Metrics and figures**: $6.5T in payment flows, ~4.5B annual transactions, ~30 million cardholders, ~$8MM cost reduction, 23 Patents, etc. — use EXACT numbers.

You may reword BULLET POINT DESCRIPTIONS to emphasize relevant skills, but the company name, title, dates, and core metrics for each role MUST remain unchanged.

### Output Requirements
1. **SCORE**: Provide a holistic job fit score from 0-100 based on the candidate's overall experience matching the JD. CRITICAL: The candidate requires ~$400k total comp. If the job explicitly posts compensation significantly lower (e.g. < $300k), penalize the score heavily. If compensation is "Not provided", you MUST use your own tech industry market data to silently estimate the compensation based on the Title, Company, and Seniority role. If your internal market estimate falls significantly below $300k, you must penalize the score heavily.
2. **SUB-SCORES**: Break down the fit into 3 sub-categories, providing a 0-100 score for exactly these three dimensions: "Technical", "Domain", and "Seniority".
3. **MARKET_SALARY**: Based strictly on the job title, company, and typical tech industry market rates for this seniority, output a realistic estimated compensation range (e.g. "$250k - $320k").
4. **BRIEF**: Provide a 1-2 sentence explanation of WHY the candidate is a good fit, or explicitly mention if the compensation might be a mismatch.
5. **RESUME**: Provide the full tailored resume in Markdown. CRITICAL RULES:
   - DO NOT INVENT PAST EXPERIENCE OR NEW ROLES.
   - DO NOT list the target company as past work history!
   - DO NOT rename, substitute, or paraphrase any employer names from the Base Resume. "Capital One Financial" must remain "Capital One Financial", never "Acme Corp" or any other name.
   - Base your rewrite EXCLUSIVELY on the provided Base Resume and Context, carefully rewording accomplishments to highlight underlying technical synergies.
6. **REPORT**: A list of specific alterations made.
7. **COVER_LETTER**: Provide a 3-4 paragraph conversational and highly professional Cover Letter that naturally pitches the candidate's alignment.

### Strategic Extrapolation Constraints
- **LinkedIn Profile Target**: %s
- **Brag Sheet Vector**: The vector Context buffer additionally holds the candidate's core "Brag Sheet".
- **Dynamic Skill Translation**: Leverage the provided LinkedIn profile and Context/Brag Sheet to purposefully craft experiences tailoring specifically to the target company's stack.
- **ABSOLUTE TRUTH CONSTRAINT**: You must NEVER hallucinate or fabricate facts. You are authorized to *highlight*, *scale*, or *re-word* existing achievement DESCRIPTIONS to emphasize relevant skills (like CISSP or Cloud), but you are STRICTLY FORBIDDEN from:
  1. Inventing jobs, companies, degrees, metrics, or technical skillsets that do not exist in the provided materials.
  2. Renaming, substituting, or paraphrasing any employer names, university names, patent numbers, or certification acronyms.
  3. Changing employment dates or degree years.
  If the JD requires a skill the candidate lacks, do NOT invent it!

--- 
### Job Description
Title: %s
Company: %s
Compensation: %s
Description: %s

### Base Resume
%s

### Experience Context (Use these specific projects/details!)
%s

---
Return your response in this EXACT format:
SCORE: [holistic score]
SUB_SCORES:
Technical: [score]
Domain: [score]
Seniority: [score]
MARKET_SALARY: [est salary range]
BRIEF: [brief]
RESUME: 
[full resume]
---COVER_LETTER---
[cover letter]
---REPORT---
[alterations list]
`, customInstrBlock, linkedinUrl, job.Title, job.Company, compStr, job.Description, baseResume, contextBuf.String())

	// 3. Call LLM (Deep Class for high-fidelity tailoring)
	rawResponse, err := llm.Generate(ctx, llm.ClassDeep, prompt)
	if err != nil {
		return result, fmt.Errorf("llm generate failed: %w", err)
	}

	// Strip Qwen3 <think>...</think> chain-of-thought blocks before parsing
	response := stripThinkTags(rawResponse)

	// 4. Parse response
	result.Score = parseScore(response)
	result.SubScores = parseSubScores(response)
	result.MarketSalary = parseMarketSalary(response)
	result.FitBrief = parseBrief(response)

	parts := strings.Split(response, "---COVER_LETTER---")
	var resumeRaw, letterAndReport string
	
	if len(parts) >= 2 {
		resumeRaw = parts[0]
		letterAndReport = parts[1]
		
		subParts := strings.Split(letterAndReport, "---REPORT---")
		if len(subParts) >= 2 {
			result.CoverLetter = cleanMarkdownTraces(strings.TrimSpace(subParts[0]))
			result.Report = strings.TrimSpace(subParts[1])
		} else {
			result.CoverLetter = cleanMarkdownTraces(strings.TrimSpace(letterAndReport))
			result.Report = "No separate report found."
		}
	} else {
		// Fallback if parsing fails
		resumeRaw = response
		subParts := strings.Split(resumeRaw, "---REPORT---")
		if len(subParts) >= 2 {
			resumeRaw = subParts[0]
			result.Report = subParts[1]
		}
		result.CoverLetter = "Cover Letter not generated."
	}
	
	resumeSplits := strings.SplitN(resumeRaw, "RESUME:", 2)
	if len(resumeSplits) >= 2 {
		result.TailoredResume = cleanMarkdownTraces(strings.TrimSpace(resumeSplits[1]))
	} else {
		result.TailoredResume = cleanMarkdownTraces(strings.TrimSpace(resumeRaw))
	}

	// Guard: if the LLM failed to produce any resume content, return an error
	// so the caller does not persist an empty string and mark the job 'completed'.
	if len(result.TailoredResume) < 100 {
		return result, fmt.Errorf("LLM response was parsed but TailoredResume is empty or too short (len=%d). Raw response snippet: %.300s", len(result.TailoredResume), response)
	}

	// 5. Post-generation validation: ensure real company names survived the LLM output
	validateFactualIntegrity(baseResume, result.TailoredResume)

	return result, nil
}

// validateFactualIntegrity checks that key employer names from the base resume
// appear in the tailored output — logs warnings if the LLM substituted them.
func validateFactualIntegrity(baseResume, tailoredResume string) {
	// Key employer names that MUST appear if they existed in the base resume
	criticalEmployers := []string{
		"Capital One",
		"Bitso",
		"Bank of America",
		"Wachovia",
		"Symbiotic",
	}

	baseLower := strings.ToLower(baseResume)
	tailoredLower := strings.ToLower(tailoredResume)

	for _, employer := range criticalEmployers {
		if strings.Contains(baseLower, strings.ToLower(employer)) {
			if !strings.Contains(tailoredLower, strings.ToLower(employer)) {
				fmt.Printf("⚠️  FACTUAL INTEGRITY WARNING: '%s' was in the base resume but MISSING from tailored output! The LLM may have substituted it.\n", employer)
			}
		}
	}
}

// cleanMarkdownTraces removes structural LLM artifacts like ```markdown
func cleanMarkdownTraces(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```markdown") {
		s = strings.TrimPrefix(s, "```markdown")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

func parseScore(s string) int {
	re := regexp.MustCompile(`(?i)SCORE:\s*(\d+)`)
	match := re.FindStringSubmatch(s)
	if len(match) > 1 {
		val, _ := strconv.Atoi(match[1])
		return val
	}
	return 0
}

func parseSubScores(s string) map[string]int {
	scores := make(map[string]int)
	lines := strings.Split(s, "\n")
	
	inSubScores := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "SUB_SCORES:") {
			inSubScores = true
			continue
		}
		if inSubScores && strings.HasPrefix(strings.ToUpper(line), "BRIEF:") {
			break
		}
		if inSubScores && strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				vStr := strings.TrimSpace(parts[1])
				// Clean non-numeric characters from vStr (like if they output " [85]" or "85/100")
				re := regexp.MustCompile(`\d+`)
				numStr := re.FindString(vStr)
				if numStr != "" {
					val, _ := strconv.Atoi(numStr)
					scores[k] = val
				}
			}
		}
	}
	
	return scores
}

func parseBrief(s string) string {
	// Capture everything from BRIEF: until the next known section header or end-of-string.
	// This handles both single-line and multi-line brief outputs from different LLMs.
	re := regexp.MustCompile(`(?is)BRIEF:\s*(.*?)(?:\n(?:RESUME:|MARKET_SALARY:|SCORE:|SUB_SCORES:|---)|$)`)
	match := re.FindStringSubmatch(s)
	if len(match) > 1 {
		brief := strings.TrimSpace(match[1])
		if brief != "" {
			return brief
		}
	}
	// Fallback: grab single line
	reSingle := regexp.MustCompile(`(?i)BRIEF:\s*([^\n]+)`)
	matchS := reSingle.FindStringSubmatch(s)
	if len(matchS) > 1 {
		return strings.TrimSpace(matchS[1])
	}
	return "No brief provided."
}

func parseMarketSalary(s string) string {
	re := regexp.MustCompile(`(?i)MARKET_SALARY:\s*([^\n]+)`)
	match := re.FindStringSubmatch(s)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return "Unknown"
}

// SaveJobProfile saves the JD, fit brief, and score to the potential-jobs folder using a SHA256 identity
func SaveJobProfile(job scraper.Job, result TailoredResult) error {
	dir := "./potential-jobs"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	hash := sha256.Sum256([]byte(job.ID))
	signature := fmt.Sprintf("%x", hash)
	filename := fmt.Sprintf("%s.md", signature)
	path := filepath.Join(dir, filename)

	content := fmt.Sprintf(`# Job Profile: %s @ %s

## Fit Score (Holistic): %d%%
_Technical: %d%% | Domain: %d%% | Seniority: %d%%_

## Market Estimated Salary
**%s**

## Why you're a match: 
%s

## Original Job Description
Source: %s
URL: %s

%s
`, job.Title, job.Company, result.Score, result.SubScores["Technical"], result.SubScores["Domain"], result.SubScores["Seniority"], result.MarketSalary, result.FitBrief, job.Source, job.URL, job.Description)

	return os.WriteFile(path, []byte(content), 0644)
}

// SaveTailoredResume saves the content and report to the queued_resume folder using a SHA256 identity
func SaveTailoredResume(job scraper.Job, result TailoredResult) error {
	dir := "./queued_resume"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	hash := sha256.Sum256([]byte(job.ID))
	signature := fmt.Sprintf("%x", hash)
	
	resumeFile := filepath.Join(dir, signature+".md")
	reportFile := filepath.Join(dir, signature+"_REPORT.md")

	if err := os.WriteFile(resumeFile, []byte(result.TailoredResume), 0644); err != nil {
		return err
	}
	return os.WriteFile(reportFile, []byte(result.Report), 0644)
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "/", "-")
	reg, _ := regexp.Compile("[^a-zA-Z0-9_-]+")
	return reg.ReplaceAllString(s, "")
}
