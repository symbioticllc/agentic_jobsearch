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
	"github.com/leee/agentic-jobs/internal/salary"
	"github.com/leee/agentic-jobs/internal/scraper"
)

// stripThinkTags removes Qwen3 <think>...</think> chain-of-thought blocks
// from the raw LLM response before structured parsing begins.
func stripThinkTags(s string) string {
	re := regexp.MustCompile(`(?s)<think>.*?</think>`)
	return strings.TrimSpace(re.ReplaceAllString(s, ""))
}

// cleanBaseResume normalizes the raw resume text before it is passed to the LLM.
// Pandoc-exported .docx files contain formatting artifacts that confuse the model
// into hallucinating "corrected" versions of real employer names, dates, and metrics.
func cleanBaseResume(raw string) string {
	// Remove Pandoc {.underline} spans — keep the visible link text only
	re := regexp.MustCompile(`(?i)\[([^\]]+)\]\{[^}]*\}`)
	s := re.ReplaceAllString(raw, "$1")

	// Strip orphaned backslash escape sequences (e.g. \* \| \\ \,)
	s = regexp.MustCompile(`\\([*|\\,])`).ReplaceAllString(s, "$1")

	// Strip ONLY standalone trailing backslashes at the end of a line
	// (Pandoc line-break markers) without touching real text content
	s = regexp.MustCompile(`\\\s*\n`).ReplaceAllString(s, "\n")

	// Strip any remaining lone backslashes that are NOT escape sequences
	s = regexp.MustCompile(`(?m)^\\\s*$`).ReplaceAllString(s, "")

	// Collapse more-than-2 consecutive blank lines down to 2
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")

	return strings.TrimSpace(s)
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

	// RAG context enrichment is best-effort: if the embedding model is cold-loading
	// or Redis is temporarily unavailable, we still want tailoring to succeed.
	// An empty context means the LLM uses only the Base Resume — still fully functional.
	ragResults, ragErr := a.rag.QueryRelevant(ctx, "project_history", jdSnippet, 5)
	if ragErr != nil {
		fmt.Printf("⚠️  RAG context lookup skipped (non-fatal): %v\n", ragErr)
		ragResults = nil
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

	// Pre-process: strip Pandoc formatting artifacts that cause LLM hallucination
	cleanedResume := cleanBaseResume(baseResume)

	prompt := fmt.Sprintf(`
You are an expert technical recruiter and career coach. Your task is to produce a highly tailored, ATS-optimized resume for this specific job posting.%s

### IMMUTABLE FACTS — DO NOT CHANGE THESE UNDER ANY CIRCUMSTANCES
The following elements from the Base Resume are SACRED and must appear EXACTLY as written:
- **Employer names**: Capital One Financial, Bitso, Bank of America, Wachovia Securities, Symbiotic LLC, City of Richmond, etc. — copy them VERBATIM.
- **Job titles**: Head of Payment Solutions, Sr. Enterprise Architect, Distinguished Engineer, VP Head of Payment Solutions, Design Team Lead, Solution Architect, Principal, etc. — use EXACTLY as provided.
- **Employment dates**: Preserve all date ranges exactly (e.g., 2022–Present, 2010–2022, Feb 2022–Aug 2022, etc.).
- **Degrees and universities**: Pennsylvania State University, Virginia Commonwealth University — use EXACT names and years.
- **Patents**: All US patent numbers must be preserved exactly as listed.
- **Certifications**: LSSBB, CISSP, AWS-ASA/ADA, CDMP, etc. — use exact acronyms and descriptions.
- **Metrics and figures**: $6.5T in payment flows, ~4.5B annual transactions, ~30 million cardholders, ~$8MM cost reduction, etc. — use EXACT numbers.

### ACTIVE TAILORING STRATEGY (follow this carefully)
You must actively tailor the resume to highlight competencies, but remain 100%% factually accurate:
1. **Professional Summary**: Rewrite the summary/objective to mirror the exact language and priorities of the JD. Use the job's own keywords. 3–4 punchy sentences max.
2. **Highlight Areas of Competency**: Under each job, DO NOT completely rewrite or invent new bullet points. Instead, select existing bullet points that match the Job Description and highlight those specific areas of competency. You can slightly tweak phrasing to emphasize these competencies, but the facts must remain 100%% identical to the Base Resume.
3. **Bullet Reordering**: Within each role, reorder existing bullet points so those most relevant to this JD appear FIRST.
4. **De-emphasize Irrelevant Bullets**: Move unrelated bullets to the bottom of that role's list.
5. **Skills Section**: Reorder skills/tech to front-load the technologies explicitly named in the JD.

### RESUME CONTENT CONSTRAINT (READ CAREFULLY)
The RESUME section you output MUST be derived EXCLUSIVELY from the "Base Resume" provided below.
DO NOT import, blend, or merge any content from the "Experience Context" brag-sheet into the RESUME text body.
The brag-sheet exists solely so you understand background depth when choosing WHICH bullet points to emphasize.

### Output Requirements
1. **SCORE**: Honest job fit score 0–100. Score the CANDIDATE'S ACTUAL BACKGROUND against the raw JD requirements — NOT the polished resume output. Use this rubric strictly:
   - **90–100**: Near-perfect match. Candidate meets every required qualification AND most preferred ones. The title, industry, tech stack, and seniority are all direct matches.
   - **75–89**: Strong match. Meets all hard requirements but 1–2 preferred skills or domain nuances are missing.
   - **60–74**: Moderate match. Core skills transfer but meaningful gaps exist (wrong industry, missing major tech, level mismatch).
   - **45–59**: Stretch. Significant gaps — candidate would need to learn key requirements or step up/down in seniority.
   - **Below 45**: Poor fit. Fundamental mismatches in domain, technology, or seniority that cannot be bridged by tailoring.
   BE HONEST AND DISCRIMINATING. Most jobs should score 55–80. A score above 88 is exceptional and must be explicitly justified. Do NOT inflate scores because the resume looks polished after tailoring.
2. **SUB_SCORES**: Break down fit into exactly 3 dimensions (0–100 each): Technical (tech stack overlap), Domain (industry/product area match), Seniority (level alignment).
3. **MARKET_SALARY**: Realistic estimated total comp range for this role in this market.
4. **BRIEF**: 2 sentences max: WHY the candidate is a strong fit (or where the gap is if score < 70).
5. **COVER_LETTER**: A 3–4 paragraph cover letter. Be specific — reference the company's product and connect it to a concrete verified achievement.
6. **RESUME**: The complete tailored resume. CRITICAL RULES:
   - DO NOT INVENT ANY PAST EXPERIENCE, ROLES, COMPANIES, DEGREES, OR METRICS.
   - DO NOT list the target company as past work history.
   - ALL content must trace back to the Base Resume provided below.
   - Terminate the RESUME section with the exact marker: ---END_RESUME---
7. **REPORT**: Bulleted list of specific tailoring changes made.

### ABSOLUTE TRUTH CONSTRAINT
You must NEVER hallucinate or fabricate facts. You are STRICTLY FORBIDDEN from:
  1. Inventing jobs, companies, degrees, metrics, or technical skillsets not in the provided Base Resume.
  2. Renaming, substituting, or paraphrasing any employer names, etc.
  3. Changing employment dates or degree years.
  If the JD requires a skill the candidate lacks, do NOT invent it.

---
### Job Description
Title: %s
Company: %s
Compensation: %s
Description: %s

### Base Resume (SOURCE OF TRUTH — all resume facts come ONLY from here)
%s

### Experience Context (Brag Sheet — use for context depth only, NOT as resume content)
%s

---
Return your response in this EXACT format (do not deviate from these markers). You MUST write the ACTUAL generated text for the cover letter and resume. DO NOT USE PLACEHOLDERS OR INSTRUCTIONS.

SCORE: [holistic score 0-100]
SUB_SCORES:
Technical: [score]
Domain: [score]
Seniority: [score]
MARKET_SALARY: [est salary range]
BRIEF: [brief analysis]
COVER_LETTER:
[WRITE THE ACTUAL TAILORED COVER LETTER TEXT HERE. NO PLACEHOLDERS.]
RESUME:
[WRITE THE FULL, ENTIRE TAILORED RESUME HERE IN MARKDOWN FORMAT. YOU MUST OUTPUT THE ACTUAL RESUME CONTENT BASED ON THE BASE RESUME. DO NOT OUTPUT PLACEHOLDER TEXT LIKE "Insert your resume here".]
---END_RESUME---
REPORT:
[bulleted list of changes made]
`, customInstrBlock, job.Title, job.Company, compStr, job.Description, cleanedResume, contextBuf.String())

	// 3. Call LLM (Deep Class for high-fidelity tailoring)
	// Log cleaned resume length for diagnostic purposes
	fmt.Printf("📄 Base resume: %d chars raw → %d chars after Pandoc cleanup\n", len(baseResume), len(cleanedResume))
	rawResponse, err := llm.Generate(ctx, llm.ClassDeep, prompt)
	if err != nil {
		return result, fmt.Errorf("llm generate failed: %w", err)
	}

	// Strip Qwen3 <think>...</think> chain-of-thought blocks before parsing
	response := stripThinkTags(rawResponse)

	// 4. Parse response
	result.Score = parseScore(response)
	result.SubScores = parseSubScores(response)
	
	apiSalary := salary.FetchMarketSalary(job.Company, job.Title)
	if apiSalary != "" {
		result.MarketSalary = apiSalary
	} else {
		result.MarketSalary = parseMarketSalary(response)
	}
	
	result.FitBrief = parseBrief(response)

	// ── Section Extraction ─────────────────────────────────────────────────
	// Strategy: find the LAST standalone "RESUME" marker in the response
	// (earlier occurrences may be in cover letter text or prompt echo).
	// Then extract cover letter from between the metadata block and RESUME,
	// and the resume body from after RESUME until END_RESUME/REPORT.

	// Step 1: Locate the RESUME section boundary — find LAST occurrence
	//         of "RESUME" or "RESUME:" on its own line.
	resumeIdx := -1
	reResumeMarker := regexp.MustCompile(`(?im)^(?:\*\*|##\s*|---\s*)?RESUME[:\s\*\-]*$`)
	allMatches := reResumeMarker.FindAllStringIndex(response, -1)
	if len(allMatches) > 0 {
		resumeIdx = allMatches[len(allMatches)-1][0] // use the LAST match
	}

	// Step 2: Extract RESUME body (everything after the marker until end delimiters)
	if resumeIdx >= 0 {
		// Find end of the "RESUME" header line
		afterMarker := response[resumeIdx:]
		nlIdx := strings.Index(afterMarker, "\n")
		if nlIdx >= 0 {
			resumeBody := afterMarker[nlIdx+1:]
			// Trim at END_RESUME or REPORT delimiter
			reEnd := regexp.MustCompile(`(?im)^---\s*END[_ ]?RESUME\s*---`)
			if loc := reEnd.FindStringIndex(resumeBody); loc != nil {
				resumeBody = resumeBody[:loc[0]]
			}
			reRpt := regexp.MustCompile(`(?im)^(?:\*\*|##\s*|---\s*)?REPORT[:\s\*\-]*$`)
			if loc := reRpt.FindStringIndex(resumeBody); loc != nil {
				resumeBody = resumeBody[:loc[0]]
			}
			reRptDash := regexp.MustCompile(`(?im)^---\s*REPORT`)
			if loc := reRptDash.FindStringIndex(resumeBody); loc != nil {
				resumeBody = resumeBody[:loc[0]]
			}
			result.TailoredResume = cleanMarkdownTraces(strings.TrimSpace(resumeBody))
		}
	}

	// Step 3: Extract COVER_LETTER — look in the region BEFORE the RESUME marker
	preResume := response
	if resumeIdx > 0 {
		preResume = response[:resumeIdx]
	}
	reCLMarker := regexp.MustCompile(`(?im)^(?:\*\*|##\s*|---\s*)?COVER[_ ]?LETTER[:\s\*\-]*$`)
	clLoc := reCLMarker.FindStringIndex(preResume)
	if clLoc != nil {
		// Cover letter body = everything after the CL marker until the RESUME boundary
		afterCL := preResume[clLoc[1]:]
		result.CoverLetter = cleanMarkdownTraces(strings.TrimSpace(afterCL))
	} else {
		// Fallback: look for "Cover Letter" as a heading (bold or plain)
		reCLHeading := regexp.MustCompile(`(?im)^(?:\*\*|##\s*)?cover\s+letter[:\s\*\-]*$`)
		clLocH := reCLHeading.FindStringIndex(preResume)
		if clLocH != nil {
			afterCL := preResume[clLocH[1]:]
			result.CoverLetter = cleanMarkdownTraces(strings.TrimSpace(afterCL))
		}
	}

	// Step 4: Extract REPORT — everything after the REPORT marker
	reRPMarker := regexp.MustCompile(`(?im)^(?:\*\*|##\s*|---\s*)?REPORT[:\s\*\-]*$`)
	rpLoc := reRPMarker.FindStringIndex(response)
	if rpLoc != nil {
		result.Report = strings.TrimSpace(response[rpLoc[1]:])
	} else {
		// Fallback: inline "REPORT:" with content on same line
		reRPInline := regexp.MustCompile(`(?im)^REPORT:\s*(.+)`)
		if m := reRPInline.FindStringSubmatch(response); len(m) > 1 {
			result.Report = strings.TrimSpace(m[1])
		} else {
			result.Report = "No separate report found."
		}
	}

	// Step 5: Post-process — strip any metadata lines that leaked into the resume body
	result.TailoredResume = stripMetadataFromResume(result.TailoredResume)

	fmt.Printf("\n📋 PARSE DIAGNOSTICS:\n")
	fmt.Printf("  Raw response length:  %d chars\n", len(response))
	fmt.Printf("  Score:                %d\n", result.Score)
	fmt.Printf("  SubScores:            %v\n", result.SubScores)
	fmt.Printf("  MarketSalary:         %s\n", result.MarketSalary)
	fmt.Printf("  FitBrief:             %.100s\n", result.FitBrief)
	fmt.Printf("  CoverLetter length:   %d chars\n", len(result.CoverLetter))
	fmt.Printf("  TailoredResume:       %d chars\n", len(result.TailoredResume))
	fmt.Printf("  Report length:        %d chars\n", len(result.Report))
	if len(response) > 500 {
		fmt.Printf("  Response HEAD (500 chars):\n---\n%s\n---\n", response[:500])
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

// stripMetadataFromResume removes any SCORE/SUB_SCORES/BRIEF/MARKET_SALARY/COVER_LETTER
// header lines and their content that may have leaked into the resume body.
// This is the safety net — even if the primary extraction regex grabs too much,
// this function ensures the resume body starts with actual resume content.
func stripMetadataFromResume(resume string) string {
	if resume == "" {
		return resume
	}

	lines := strings.Split(resume, "\n")
	// Known metadata headers that should never appear in a resume body
	metaHeaders := regexp.MustCompile(`(?i)^\s*(?:\*\*|##\s*)?(SCORE|SUB[_ ]?SCORES?|BRIEF|MARKET[_ ]?SALARY|COVER[_ ]?LETTER|WHY YOU FIT|FIT BRIEF|---\s*END[_ ]?RESUME|---\s*COVER|---\s*REPORT)[:\s\*\-]*$`)
	// Lines that look like sub-score detail lines (e.g. "Technical: 95" or "• Technical: 95 (...)")
	metaScoreLine := regexp.MustCompile(`(?i)^\s*[•\-\*]?\s*(Technical|Domain|Seniority)[:\s]+\d+`)
	// Pipe-separated score summary line
	metaPipeLine := regexp.MustCompile(`(?i)(Technical|Domain|Seniority)\s*[:\s]+\s*\d+\s*\|`)
	// Standalone score line (just a number, possibly with /100)
	metaStandaloneScore := regexp.MustCompile(`^\s*\d{1,3}(/100)?\s*$`)

	// Find where the real resume content starts by skipping metadata lines at the top
	startIdx := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue // skip blank lines
		}
		if metaHeaders.MatchString(trimmed) ||
			metaScoreLine.MatchString(trimmed) ||
			metaPipeLine.MatchString(trimmed) ||
			metaStandaloneScore.MatchString(trimmed) {
			startIdx = i + 1
			continue
		}
		// Also skip lines that are just a dollar range (market salary value)
		if regexp.MustCompile(`(?i)^\s*\$\d+[kK]?\s*[\-–—]\s*\$\d+[kK]?\s*$`).MatchString(trimmed) {
			startIdx = i + 1
			continue
		}
		// First line that doesn't match any metadata pattern — this is where the resume starts
		break
	}

	if startIdx > 0 && startIdx < len(lines) {
		stripped := strings.Join(lines[startIdx:], "\n")
		result := strings.TrimSpace(stripped)
		if len(result) > 50 {
			fmt.Printf("  ✂️  Stripped %d metadata lines from resume body top\n", startIdx)
			return result
		}
	}

	return strings.TrimSpace(resume)
}

func parseScore(s string) int {
	// Try "SCORE: 95" or "SCORE: 95/100" (colon format)
	re := regexp.MustCompile(`(?im)^\s*SCORE[:\s]+(\d+)`)
	match := re.FindStringSubmatch(s)
	if len(match) > 1 {
		val, _ := strconv.Atoi(match[1])
		return val
	}
	// Fallback: SCORE on its own line, number on the next line
	re2 := regexp.MustCompile(`(?im)^\s*SCORE\s*$\s*^\s*(\d+)`)
	match2 := re2.FindStringSubmatch(s)
	if len(match2) > 1 {
		val, _ := strconv.Atoi(match2[1])
		return val
	}
	return 0
}

func parseSubScores(s string) map[string]int {
	scores := make(map[string]int)

	lines := strings.Split(s, "\n")
	inSubScores := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		up := strings.ToUpper(trimmed)

		// Detect SUB_SCORES header (with or without colon)
		if strings.HasPrefix(up, "SUB_SCORES") || strings.HasPrefix(up, "SUB SCORES") {
			inSubScores = true
			// Check for inline content after header on same line
			after := ""
			for _, prefix := range []string{"SUB_SCORES:", "SUB SCORES:", "SUB_SCORES", "SUB SCORES"} {
				if idx := strings.Index(up, prefix); idx >= 0 {
					after = strings.TrimSpace(trimmed[idx+len(prefix):])
					break
				}
			}
			if after != "" && strings.Contains(after, "|") {
				parsePipeSeparatedScores(after, scores)
				break
			}
			continue
		}
		if inSubScores {
			// Stop at next major section
			if strings.HasPrefix(up, "BRIEF") || strings.HasPrefix(up, "MARKET") ||
				strings.HasPrefix(up, "COVER") || strings.HasPrefix(up, "RESUME") ||
				strings.HasPrefix(up, "---") {
				break
			}
			// Skip blank lines within the sub-scores block
			if trimmed == "" {
				continue
			}
			// Check for pipe-separated line (the score summary line)
			if strings.Contains(trimmed, "|") {
				parsePipeSeparatedScores(trimmed, scores)
				continue
			}
			// Parse "Key: Value" or "• Key: Value (explanation)" format
			clean := strings.TrimLeft(trimmed, "•-*– ")
			if strings.Contains(clean, ":") {
				parts := strings.SplitN(clean, ":", 2)
				if len(parts) == 2 {
					k := strings.TrimSpace(parts[0])
					vStr := strings.TrimSpace(parts[1])
					re := regexp.MustCompile(`^\s*(\d+)`)
					numStr := re.FindStringSubmatch(vStr)
					if len(numStr) > 1 {
						val, _ := strconv.Atoi(numStr[1])
						scores[k] = val
					}
				}
			}
		}
	}

	// Fallback: scan entire response for "Technical NN" patterns anywhere
	if len(scores) == 0 {
		dimensions := []string{"Technical", "Domain", "Seniority"}
		for _, dim := range dimensions {
			re := regexp.MustCompile(`(?i)` + dim + `[:\s]+(\d+)`)
			if m := re.FindStringSubmatch(s); len(m) > 1 {
				val, _ := strconv.Atoi(m[1])
				scores[dim] = val
			}
		}
	}

	return scores
}

// parsePipeSeparatedScores handles "Technical 95 | Domain 90 | Seniority 95 | Market Salary ..."
func parsePipeSeparatedScores(s string, scores map[string]int) {
	parts := strings.Split(s, "|")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		// Extract "DimensionName NN" pattern
		re := regexp.MustCompile(`(?i)(Technical|Domain|Seniority)\s+(\d+)`)
		if m := re.FindStringSubmatch(part); len(m) > 2 {
			val, _ := strconv.Atoi(m[2])
			// Capitalize first letter for consistency
			key := strings.ToUpper(m[1][:1]) + strings.ToLower(m[1][1:])
			scores[key] = val
		}
	}
}

func parseBrief(s string) string {
	// Match section header with or without colon, followed by content
	// Handles: "BRIEF: text", "BRIEF\ntext", "WHY YOU FIT:\ntext"
	re := regexp.MustCompile(`(?ims)^\s*(?:BRIEF|WHY YOU FIT|FIT BRIEF)[:\s]*$\s*(.*?)(?:^\s*(?:COVER|RESUME|MARKET|SCORE|SUB|---)|\z)`)
	match := re.FindStringSubmatch(s)
	if len(match) > 1 {
		brief := strings.TrimSpace(match[1])
		if brief != "" {
			return brief
		}
	}
	// Inline format: "BRIEF: text on same line"
	re2 := regexp.MustCompile(`(?i)(?:BRIEF|WHY YOU FIT|FIT BRIEF)[:\s]+([^\n]+)`)
	match2 := re2.FindStringSubmatch(s)
	if len(match2) > 1 {
		brief := strings.TrimSpace(match2[1])
		if brief != "" {
			return brief
		}
	}
	return "No why you fit / brief provided."
}

func parseMarketSalary(s string) string {
	// "MARKET_SALARY: $280k–$350k" or "MARKET SALARY: $280k-$350k"
	re := regexp.MustCompile(`(?i)MARKET[_ ]SALARY[:\s]+([^\n]+)`)
	match := re.FindStringSubmatch(s)
	if len(match) > 1 {
		v := strings.TrimSpace(match[1])
		if v != "" {
			return v
		}
	}
	// Inlined: "Market Salary $350k–$450k" or "Market Salary: $350k–$450k"
	re2 := regexp.MustCompile(`(?i)Market\s+Salary[:\s]+(\$[^\n|]+)`)
	match2 := re2.FindStringSubmatch(s)
	if len(match2) > 1 {
		return strings.TrimSpace(match2[1])
	}
	// Last resort: look for a dollar range anywhere near SCORE/SUB_SCORES block
	re3 := regexp.MustCompile(`(?i)(\$\d+[kK]?\s*[–\-–]\s*\$\d+[kK]?)`)
	match3 := re3.FindStringSubmatch(s)
	if len(match3) > 1 {
		return strings.TrimSpace(match3[1])
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
