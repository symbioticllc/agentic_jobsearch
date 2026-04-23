package salary

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const rapidAPIKey = "01c33bf5c1msh4886f28a5560857p14c5edjsn9eadd0c07a63"

// FetchMarketSalary attempts to fetch real-world salary data for a given role from RapidAPI.
// It cascades through multiple endpoints to maximize the chance of a hit.
func FetchMarketSalary(company string, jobTitle string) string {
	client := &http.Client{Timeout: 10 * time.Second}

	// 1. Try company-specific endpoint
	if company != "" && jobTitle != "" {
		res1 := fetchCompanySpecificSalary(client, company, jobTitle)
		if res1 != "" {
			return res1
		}
	}

	// 2. Try generic market average endpoint
	if jobTitle != "" {
		res2 := fetchMarketAverageSalary(client, jobTitle)
		if res2 != "" {
			return res2
		}
	}

	return "" // Empty means LLM should fallback to its own estimate
}

func fetchCompanySpecificSalary(client *http.Client, company string, jobTitle string) string {
	u := fmt.Sprintf("https://job-salary-data.p.rapidapi.com/company-job-salary?company=%s&job_title=%s&location_type=ANY&years_of_experience=ALL", url.QueryEscape(company), url.QueryEscape(jobTitle))
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Add("x-rapidapi-host", "job-salary-data.p.rapidapi.com")
	req.Header.Add("x-rapidapi-key", rapidAPIKey)

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 200 {
		return ""
	}

	body, _ := io.ReadAll(resp.Body)
	
	// If the API returns {"data": ["3"]} because it has no data, this unmarshal will safely error out.
	var payload struct {
		Status string `json:"status"`
		Data   []struct {
			MinSalary float64 `json:"min_salary"`
			MaxSalary float64 `json:"max_salary"`
		} `json:"data"`
	}
	
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	if len(payload.Data) > 0 && payload.Data[0].MinSalary > 0 {
		min := int(payload.Data[0].MinSalary) / 1000
		max := int(payload.Data[0].MaxSalary) / 1000
		return fmt.Sprintf("$%dk - $%dk (API: Company Match)", min, max)
	}

	return ""
}

func fetchMarketAverageSalary(client *http.Client, jobTitle string) string {
	u := fmt.Sprintf("https://jobs-api14.p.rapidapi.com/v2/salary/range?query=%s&countryCode=us", url.QueryEscape(jobTitle))
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Add("x-rapidapi-host", "jobs-api14.p.rapidapi.com")
	req.Header.Add("x-rapidapi-key", rapidAPIKey)

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return ""
	}

	body, _ := io.ReadAll(resp.Body)
	
	var payload struct {
		Data struct {
			YearlySalary struct {
				Min float64 `json:"min"`
				Max float64 `json:"max"`
			} `json:"yearlySalary"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	if payload.Data.YearlySalary.Min > 0 {
		min := int(payload.Data.YearlySalary.Min) / 1000
		max := int(payload.Data.YearlySalary.Max) / 1000
		return fmt.Sprintf("$%dk - $%dk (API: Market Range)", min, max)
	}

	return ""
}
