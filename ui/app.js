document.addEventListener('DOMContentLoaded', () => {
    const jobListEl = document.getElementById('job-list');
    const jobCountEl = document.getElementById('job-count');
    const jobDetailEl = document.getElementById('job-detail-content');
    const scrapeBtn = document.getElementById('scrape-btn');
    const searchInput = document.getElementById('semantic-search-input');
    const searchBtn = document.getElementById('semantic-search-btn');
    const clearBtn = document.getElementById('clear-search-btn');
    const minCompSlider = document.getElementById('min-comp-slider');
    const minCompDisplay = document.getElementById('min-comp-display');
    const execToggle = document.getElementById('scrape-exec-toggle');

    let jobsData = [];
    let activeJobId = null;
    let minCompValue = 0;

    // Load initial jobs
    fetchJobs();

    const stopScrapeBtn = document.getElementById('stop-scrape-btn');

    // Event Listeners
    stopScrapeBtn.addEventListener('click', async () => {
        try {
            stopScrapeBtn.innerHTML = '<span class="icon">⌛</span> Stopping...';
            stopScrapeBtn.disabled = true;
            await fetch('/api/scrape/stop', { method: 'POST' });
        } catch(e) {
            console.error(e);
        }
    });

    scrapeBtn.addEventListener('click', async () => {
        scrapeBtn.innerHTML = '<span class="icon">⏳</span> Scraping...';
        scrapeBtn.disabled = true;
        stopScrapeBtn.style.display = 'block';
        stopScrapeBtn.innerHTML = '<span class="icon">🛑</span> Stop';
        stopScrapeBtn.disabled = false;
        
        const isExecParams = execToggle.checked ? '?exec=true' : '';
        
        try {
            const res = await fetch('/api/scrape' + isExecParams, { method: 'POST' });
            if(res.ok) {
                const data = await res.json();
                alert(`Scraping complete! Added ${data.added} new jobs from ${data.scraped} found.`);
                fetchJobs();
            } else if (res.status === 408 || res.status === 409 || res.status === 499 || String(res.status).startsWith("4")) {
                const errText = await res.text();
                alert(`Scraping aborted: ${errText.trim()}`);
            } else {
                alert('Scraping failed or timed out.');
            }
        } catch (e) {
            console.error(e);
            alert('Scraping connection dropped explicitly.');
        } finally {
            scrapeBtn.innerHTML = '<span class="icon">🔍</span> Scrape New Jobs';
            scrapeBtn.disabled = false;
            stopScrapeBtn.style.display = 'none';
        }
    });

    searchBtn.addEventListener('click', async () => {
        const query = searchInput.value.trim();
        if (!query) return fetchJobs();
        
        searchBtn.disabled = true;
        searchBtn.innerHTML = '...';
        try {
            jobListEl.innerHTML = '<div class="empty-state">Searching...</div>';
            const res = await fetch('/api/jobs/search?q=' + encodeURIComponent(query));
            if (!res.ok) throw new Error('Search failed: ' + res.statusText);
            jobsData = await res.json();
            if (!jobsData) jobsData = [];
            
            if (jobsData.length === 0) {
                jobListEl.innerHTML = '<div class="empty-state" style="color: #60a5fa;">No jobs matched your semantic query. Try broader keywords.</div>';
                jobCountEl.textContent = '0';
            } else {
                renderJobList();
            }
        } catch(e) {
            console.error(e);
            jobListEl.innerHTML = '<div class="empty-state" style="color: #ef4444;">Search error. Check if Redis is running and populated.</div>';
        } finally {
            searchBtn.disabled = false;
            searchBtn.innerHTML = 'Search';
        }
    });

    searchInput.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
            e.preventDefault();
            searchBtn.click();
        }
    });

    function updateClearButtonVisibility() {
        if (searchInput.value.trim() !== '' || minCompValue > 0) {
            clearBtn.style.display = 'inline-block';
        } else {
            clearBtn.style.display = 'none';
        }
    }

    searchInput.addEventListener('input', updateClearButtonVisibility);

    clearBtn.addEventListener('click', () => {
        searchInput.value = '';
        
        // Reset compensation filter to defaults
        minCompSlider.value = 0;
        minCompValue = 0;
        minCompDisplay.textContent = '$0';

        updateClearButtonVisibility();
        fetchJobs(); // This re-fetches the unmodified list of jobs from SQLite
    });

    minCompSlider.addEventListener('input', (e) => {
        minCompValue = parseInt(e.target.value, 10);
        if (minCompValue === 0) {
            minCompDisplay.textContent = '$0';
        } else {
            minCompDisplay.textContent = `$${minCompValue / 1000}k+`;
        }
        updateClearButtonVisibility();
        renderJobList();
    });

    function extractMaxComp(compStr) {
        if (!compStr) return 0;
        const s = compStr.toLowerCase();
        const matches = s.match(/\d+(?:,\d+)?(?:\.\d+)?/g);
        if (!matches) return 0;
        
        let max = 0;
        for (let m of matches) {
            let num = parseFloat(m.replace(/,/g, ''));
            if (num < 1000 && s.includes('k')) num *= 1000;
            if (num > max) max = num;
        }
        return max;
    }

    async function fetchJobs() {
        try {
            jobListEl.innerHTML = '<div class="empty-state">Loading jobs...</div>';
            const res = await fetch('/api/jobs');
            if (!res.ok) throw new Error('Network response not ok');
            jobsData = await res.json();
            
            if(!jobsData || jobsData.length === 0) {
                jobListEl.innerHTML = '<div class="empty-state">No jobs found. Click Scrape to start!</div>';
                jobCountEl.textContent = '0';
                return;
            }

            renderJobList();
        } catch(e) {
            console.error("Fetch jobs error", e);
            jobListEl.innerHTML = '<div class="empty-state" style="color:#ef4444;">Failed to load jobs.</div>';
        }
    }

    function renderJobList() {
        jobListEl.innerHTML = '';
        let displayedCount = 0;

        jobsData.forEach(job => {
            if (minCompValue > 0) {
                const jobComp = extractMaxComp(job.compensation);
                if (jobComp < minCompValue) return; // filter this job out
            }

            displayedCount++;
            const card = document.createElement('div');
            card.className = `job-card ${activeJobId === job.id ? 'active' : ''}`;
            card.dataset.jobId = job.id;
            card.onclick = () => selectJob(job.id);

            // Tags parsing
            let tagsHtml = '';
            if(job.tags && job.tags.length > 0) {
                tagsHtml = `<div class="job-tags">` + 
                    job.tags.slice(0,3).map(t => `<span class="tag">${t}</span>`).join('') +
                    (job.tags.length > 3 ? `<span class="tag">+${job.tags.length-3}</span>` : '') +
                `</div>`;
            }

            let compHtml = '';
            if(job.compensation) {
                compHtml = `<div class="job-compensation" style="color: #10b981; font-weight: 600; font-size: 0.85rem; padding-bottom: 0.5rem;">💰 ${job.compensation}</div>`;
            }

            let matchHtml = '';
            if (job.exact_match) {
                matchHtml = `<div style="color: #3b82f6; font-weight: 800; font-size: 0.85rem; margin-top: 0.5rem; display: flex; align-items: center; gap: 0.25rem;">
                    <span>✨ Exact Keyword Match</span>
                </div>`;
            } else if (job.vector_distance !== undefined) {
                // Approximate confidence mapping (distance 0 = 100%, distance ~1.0 = 50%)
                const confidence = Math.max(0, 100 - (job.vector_distance * 75)).toFixed(1);
                matchHtml = `<div style="color: #c084fc; font-weight: 700; font-size: 0.85rem; margin-top: 0.5rem; display: flex; align-items: center; gap: 0.25rem;">
                    <span>⚡ Semantic Match: ${confidence}%</span>
                </div>`;
            }

            card.innerHTML = `
                <div class="job-title">${job.title}</div>
                <div class="job-company">${job.company}</div>
                ${compHtml}
                ${tagsHtml}
                ${matchHtml}
            `;
            jobListEl.appendChild(card);
        });

        jobCountEl.textContent = displayedCount;
        
        if (displayedCount === 0 && jobsData.length > 0) {
            jobListEl.innerHTML = '<div class="empty-state" style="color: #60a5fa;">No jobs meet your compensation requirements.</div>';
        }
    }

    function selectJob(id) {
        activeJobId = id;
        
        // Update active classes directly in the DOM to prevent scroll jumps
        const cards = document.querySelectorAll('.job-card');
        cards.forEach(card => {
            if (card.dataset.jobId === id) {
                card.classList.add('active');
            } else {
                card.classList.remove('active');
            }
        });
        
        const job = jobsData.find(j => j.id === id);
        if(!job) return;

        renderJobDetail(job);
    }

    function renderJobDetail(job) {
        jobDetailEl.innerHTML = `
            <div class="detail-view">
                <h1>${job.title}</h1>
                <div class="company-info">
                    <strong>${job.company}</strong> &bull; 
                    <a href="${job.url}" target="_blank">View Original Post ↗</a>
                </div>
                ${job.compensation ? `<div class="job-compensation" style="color: #10b981; font-weight: 700; font-size: 1.1rem; margin-bottom: 1.5rem;">💰 Compensation: ${job.compensation}</div>` : ''}

                <div class="action-bar" style="display:flex; justify-content:space-between; align-items:center;">
                    <div style="display:flex; gap:1rem; align-items:center;">
                        <button class="primary-btn" id="run-tailor-btn">
                            <span class="icon">✨</span> Align & Tailor Resume
                        </button>
                        <button class="secondary-btn" id="open-export-modal-btn" style="display:none; color: #fff; border-color: rgba(255,255,255,0.3);">
                            <span class="icon">📄</span> View & Export
                        </button>
                    </div>
                    <div class="tailor-status" id="tailor-status-area">
                        <span>Has not been tailored yet.</span>
                    </div>
                </div>

                <div id="tailored-output"></div>

                <h3>Original Description</h3>
                <br/>
                <div class="jd-box">${job.description.replace(/\n/g, '<br/>')}</div>
            </div>
        `;

        document.getElementById('run-tailor-btn').addEventListener('click', () => runTailor(job.id));

        // If the backend already cached the generation in the DB, prefill the exact layout instantly
        if (job.tailored_resume && job.tailored_resume.length > 0) {
            document.getElementById('run-tailor-btn').innerHTML = '<span class="icon">✨</span> Re-Tailor';
            
            // Format mock result structure internally
            const cachedResult = {
                Score: job.score,
                MarketSalary: job.market_salary,
                FitBrief: job.fit_brief,
                TailoredResume: job.tailored_resume,
                Report: job.tailored_report
            };
            
            // Populate HUD
            const statusArea = document.getElementById('tailor-status-area');
            const outputArea = document.getElementById('tailored-output');
            
            statusArea.innerHTML = `
                <div style="display: flex; gap: 1.5rem; align-items: center; align-content: center; flex-wrap: wrap;">
                    <div class="score-hud" style="text-align: center;">
                        <span class="val" style="font-size: 2.5rem; font-weight: 800; color: #10b981; display: block; line-height: 1;">${cachedResult.Score || 0}%</span>
                        <span class="label" style="font-size: 0.8rem; font-weight: 600; text-transform: uppercase; color: var(--text-muted); letter-spacing: 1px;">Holistic Fit</span>
                    </div>
                    <div style="display: flex; flex-direction: column; gap: 0.25rem; border-left: 2px solid rgba(255,255,255,0.1); padding-left: 1.5rem;">
                        <span style="font-size: 0.8rem; font-weight: 600; text-transform: uppercase; color: var(--text-muted); letter-spacing: 1px;">Estimated Market Value</span>
                        <strong style="color: #c084fc; font-size: 1.1rem;">${cachedResult.MarketSalary || "Unknown"}</strong>
                    </div>
                </div>
            `;
            
            outputArea.innerHTML = `
                <div class="brief-box">
                    <strong>Why you fit:</strong> ${cachedResult.FitBrief}
                </div>
                <h3>Tailored Resume Preview</h3>
                <br/>
                <div class="resume-preview">${marked.parse(cachedResult.TailoredResume)}</div>
                <br/>
                <h3>Alteration Report</h3>
                <br/>
                <div class="resume-preview">${marked.parse(cachedResult.Report)}</div>
                <br/>
            `;
            
            // Re-bind Export modal hooks for cached object
            const exportBtn = document.getElementById('open-export-modal-btn');
            exportBtn.style.display = 'inline-flex';
            exportBtn.onclick = () => {
                const modal = document.getElementById('resume-modal');
                const printHtml = document.getElementById('tailored-resume-html');
                printHtml.innerHTML = marked.parse(cachedResult.TailoredResume);
                modal.showModal();
            };
        }
    }

    async function runTailor(id) {
        const btn = document.getElementById('run-tailor-btn');
        const statusArea = document.getElementById('tailor-status-area');
        const outputArea = document.getElementById('tailored-output');

        btn.innerHTML = '<span class="icon">⏳</span> Tailoring (Takes ~30s)...';
        btn.disabled = true;

        try {
            const res = await fetch(`/api/jobs/tailor/${id}`, { method: 'POST' });
            if(!res.ok) throw new Error("Tailor failed on backend");
            
            const result = await res.json();
            
            // Extract subscores with fallbacks
            const ts = result.SubScores?.Technical || 0;
            const ds = result.SubScores?.Domain || 0;
            const ss = result.SubScores?.Seniority || 0;

            // Format output into the UI
            statusArea.innerHTML = `
                <div style="display: flex; gap: 1.5rem; align-items: center; align-content: center; flex-wrap: wrap;">
                    <div class="score-hud" style="text-align: center;">
                        <span class="val" style="font-size: 2.5rem; font-weight: 800; color: #10b981; display: block; line-height: 1;">${result.Score}%</span>
                        <span class="label" style="font-size: 0.8rem; font-weight: 600; text-transform: uppercase; color: var(--text-muted); letter-spacing: 1px;">Holistic Fit</span>
                    </div>
                    
                    <div style="display: flex; flex-direction: column; gap: 0.5rem; border-left: 2px solid rgba(255,255,255,0.1); padding-left: 1.5rem;">
                        <div style="display: flex; justify-content: space-between; gap: 1rem; font-size: 0.85rem;">
                            <span style="color: var(--text-muted);">Technical</span>
                            <strong style="color: #60a5fa;">${ts}%</strong>
                        </div>
                        <div style="display: flex; justify-content: space-between; gap: 1rem; font-size: 0.85rem;">
                            <span style="color: var(--text-muted);">Domain</span>
                            <strong style="color: #60a5fa;">${ds}%</strong>
                        </div>
                        <div style="display: flex; justify-content: space-between; gap: 1rem; font-size: 0.85rem;">
                            <span style="color: var(--text-muted);">Seniority</span>
                            <strong style="color: #60a5fa;">${ss}%</strong>
                        </div>
                    </div>

                    <div style="display: flex; flex-direction: column; gap: 0.25rem; border-left: 2px solid rgba(255,255,255,0.1); padding-left: 1.5rem;">
                        <span style="font-size: 0.8rem; font-weight: 600; text-transform: uppercase; color: var(--text-muted); letter-spacing: 1px;">Estimated Market Value</span>
                        <strong style="color: #c084fc; font-size: 1.1rem;">${result.MarketSalary || "Unknown"}</strong>
                    </div>
                </div>
            `;

            outputArea.innerHTML = `
                <div class="brief-box">
                    <strong>Why you fit:</strong> ${result.FitBrief}
                </div>
                <h3>Tailored Resume Preview</h3>
                <br/>
                <div class="resume-preview">${marked.parse(result.TailoredResume)}</div>
                <br/>
                <h3>Alteration Report</h3>
                <br/>
                <div class="resume-preview">${marked.parse(result.Report)}</div>
                <br/>
            `;

            // Setup the Export Modal
            const exportBtn = document.getElementById('open-export-modal-btn');
            exportBtn.style.display = 'inline-flex';
            
            exportBtn.onclick = () => {
                const modal = document.getElementById('resume-modal');
                const printHtml = document.getElementById('tailored-resume-html');
                
                printHtml.innerHTML = marked.parse(result.TailoredResume);
                modal.showModal();
            };

        } catch(e) {
            console.error(e);
            alert("Error running tailor process. Ensure Ollama/Chroma are running.");
        } finally {
            btn.innerHTML = '<span class="icon">✨</span> Re-Tailor';
            btn.disabled = false;
        }
    }

    // Modal Export Bindings
    const resModal = document.getElementById('resume-modal');
    document.getElementById('close-modal-btn').onclick = () => resModal.close();
    
    document.getElementById('export-pdf-btn').onclick = () => {
        // We use native window.print() and CSS @media print
        window.print();
    };

    document.getElementById('export-docx-btn').onclick = () => {
        const content = document.getElementById('tailored-resume-html').innerHTML;
        // Construct a clean HTML string required by html-docx-js
        const htmlPayload = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Resume</title><style>body{font-family:sans-serif;}</style></head><body>${content}</body></html>`;
        
        const blob = htmlDocx.asBlob(htmlPayload);
        saveAs(blob, "Tailored_Resume.docx");
    };

    document.getElementById('export-gdocs-btn').onclick = async () => {
        const contentHtml = document.getElementById('tailored-resume-html').innerHTML;

        try {
            // Attempt to write HTML into the clipboard buffer
            const blob = new Blob([contentHtml], { type: 'text/html' });
            const plainBlob = new Blob([document.getElementById('tailored-resume-html').innerText], { type: 'text/plain' });
            await navigator.clipboard.write([
                new ClipboardItem({
                    'text/html': blob,
                    'text/plain': plainBlob
                })
            ]);
            alert("Resume formatted to clipboard! Opening Google Docs... Press Cmd+V (or Ctrl+V) to paste your resume securely into the blank document.");
            window.open('https://docs.google.com/document/create', '_blank');
        } catch(e) {
            console.error("Clipboard API failed due to localhost constraint", e);
            alert("Please copy the text manually, then paste into Google Docs. (Clipboard API requires HTTPS)");
            window.open('https://docs.google.com/document/create', '_blank');
        }
    };

    function escapeHtml(unsafe) {
        if(!unsafe) return "";
        return unsafe
             .replace(/&/g, "&amp;")
             .replace(/</g, "&lt;")
             .replace(/>/g, "&gt;")
             .replace(/"/g, "&quot;")
             .replace(/'/g, "&#039;");
    }
});
