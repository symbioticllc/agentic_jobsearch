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
    let availableResumes = [];
    let activeTenant = localStorage.getItem("tenant_id");
    let reportData = [];

    // ── Startup Health Poller ─────────────────────────────────────────────────
    const banner       = document.getElementById('startup-banner');
    const bannerMsg    = document.getElementById('startup-message');
    const bannerDetail = document.getElementById('startup-detail');
    const healthDot    = document.getElementById('health-dot');
    let startupPoller  = null;
    let systemReady    = false;

    function setBanner(display) { if (banner) banner.style.display = display; }
    function setBannerMsg(text, color) {
        if (bannerMsg) { bannerMsg.textContent = text; bannerMsg.style.color = color || ''; }
    }
    function setBannerDetail(text) { if (bannerDetail) bannerDetail.textContent = text; }
    function updateHealthDot(overall) {
        if (!healthDot) return;
        const colors = { ok: '#10b981', degraded: '#f59e0b', error: '#ef4444' };
        healthDot.style.background = colors[overall] || '#f59e0b';
    }

    async function pollStartupHealth() {
        try {
            const res = await fetch('/api/health');
            if (!res.ok) throw new Error('not ready');
            const data = await res.json();
            updateHealthDot(data.overall);

            const ollama = data.components?.ollama || {};
            const redis  = data.components?.redis  || {};
            const sqlite = data.components?.sqlite || {};

            const issues = [];
            if (ollama.status !== 'ok')    issues.push(ollama.status === 'error' ? '🧠 Ollama unreachable' : '🧠 Model loading from NAS...');
            if (redis.status  === 'error') issues.push('⚡ Redis offline');
            if (sqlite.status === 'error') issues.push('🗄️ SQLite error');

            if (issues.length === 0 && data.overall === 'ok') {
                setBannerMsg('✅ All systems ready', '#10b981');
                setBannerDetail('');
                setBanner('flex');
                if (startupPoller) clearInterval(startupPoller);
                systemReady = true;
                setTimeout(() => setBanner('none'), 2500);
            } else {
                setBanner('flex');
                setBannerMsg(issues.length ? issues.join('  ·  ') : 'Warming up...');
                setBannerDetail(ollama.latency ? `Ollama ${ollama.latency}` : '');
            }
        } catch(e) {
            setBanner('flex');
            setBannerMsg('⏳ Waiting for server...');
            setBannerDetail('');
            if (healthDot) healthDot.style.background = '#ef4444';
        }
    }

    // Start polling — every 4s until all systems ready
    setBanner('flex');
    pollStartupHealth();
    startupPoller = setInterval(async () => {
        if (!systemReady) await pollStartupHealth();
        else clearInterval(startupPoller);
    }, 4000);

    // Override Global Fetch safely
    const originalFetch = window.fetch;
    window.fetch = async (...args) => {
        try {
            let [resource, config] = args;
            if (!config) config = {};
            if (!config.headers) config.headers = {};
            
            const rStr = resource ? resource.toString() : "";
            if (activeTenant && activeTenant !== "null" && !rStr.includes("http") && rStr.startsWith("/api/")) {
                if (config.headers instanceof Headers) {
                    if (!config.headers.has('Authorization')) {
                        config.headers.append('Authorization', `Bearer ${activeTenant}`);
                    }
                } else {
                    if (!config.headers['Authorization'] && !config.headers['authorization']) {
                        config.headers['Authorization'] = `Bearer ${activeTenant}`;
                    }
                }
            }
            return originalFetch(resource, config);
        } catch(e) {
            console.error("fetch override error:", e);
            return originalFetch(...args);
        }
    };

    const authModal = document.getElementById('auth-modal');
    
    // Clear dead string artifacts if present
    if (activeTenant === "null" || activeTenant === "undefined") {
        activeTenant = null;
        localStorage.removeItem("tenant_id");
    }

    if (!activeTenant) {
        if (!authModal.open) authModal.showModal();
    } else {
        // Test connectivity and clear session if stale
        fetchJobs();
        fetchResumes();
    }

    document.getElementById('auth-form').addEventListener('submit', (e) => {
        e.preventDefault();
        const t = document.getElementById('tenant-id-input').value.trim();
        if (t) {
            localStorage.setItem("tenant_id", t);
            activeTenant = t;
            authModal.close();
            fetchJobs();
            fetchResumes();
        }
    });

    // Load available templates safely
    function fetchResumes() {
        fetch('/api/resumes')
            .then(async r => {
                if (r.status === 401 || r.status === 403) {
                     localStorage.removeItem("tenant_id");
                     activeTenant = null;
                     if (authModal && !authModal.open) authModal.showModal();
                     return;
                }
                if (!r.ok) throw new Error(await r.text());
                return r.json();
            })
            .then(data => { availableResumes = data || []; })
            .catch(e => console.error("Could not fetch resumes", e));
    }

    // Event Listeners for Profile Modal
    const profileModal = document.getElementById('profile-modal');
    document.getElementById('open-profile-btn').addEventListener('click', async () => {
        // Fetch existing linkedin
        try {
            const res = await fetch('/api/profile');
            const data = await res.json();
            if (data.linkedin_url) document.getElementById('linkedin-url-input').value = data.linkedin_url;
        } catch(e) {}
        profileModal.showModal();
    });
    
    document.getElementById('close-profile-btn').addEventListener('click', () => {
        profileModal.close();
    });

    document.getElementById('save-linkedin-btn').addEventListener('click', async () => {
        const url = document.getElementById('linkedin-url-input').value;
        const statusEl = document.getElementById('linkedin-status');
        await fetch('/api/profile', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({"linkedin_url": url})
        });
        statusEl.style.display = 'inline';
        setTimeout(() => statusEl.style.display='none', 2000);
    });

    document.getElementById('resume-upload-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const fileInput = document.getElementById('resume-file');
        const gdocInput = document.getElementById('resume-gdoc-url');
        
        if (!fileInput.files[0] && gdocInput.value.trim() === '') return;
        
        const statusEl = document.getElementById('resume-upload-status');
        
        const formData = new FormData();
        if (fileInput.files[0]) formData.append('file', fileInput.files[0]);
        if (gdocInput.value.trim() !== '') formData.append('gdoc_url', gdocInput.value.trim());
        formData.append('type', 'resume');
        
        statusEl.textContent = "Importing...";
        statusEl.style.color = "#fbbf24";
        statusEl.style.display = 'inline';
        
        const res = await fetch('/api/profile/upload', {method: 'POST', body: formData});
        if (res.ok) {
            statusEl.textContent = "✅ Saved to Base Resumes!";
            statusEl.style.color = "#10b981";
            // Refresh resumes explicitly
            fetch('/api/resumes').then(r=>r.json()).then(data=>{ availableResumes = data || []; });
        } else {
            statusEl.textContent = "❌ Upload Failed";
            statusEl.style.color = "#ef4444";
        }
        setTimeout(() => statusEl.style.display='none', 3000);
    });

    document.getElementById('bragsheet-upload-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const fileInput = document.getElementById('bragsheet-file');
        const gdocInput = document.getElementById('brag-gdoc-url');
        
        if (!fileInput.files[0] && gdocInput.value.trim() === '') return;
        
        const statusEl = document.getElementById('brag-upload-status');
        
        const formData = new FormData();
        if (fileInput.files[0]) formData.append('file', fileInput.files[0]);
        if (gdocInput.value.trim() !== '') formData.append('gdoc_url', gdocInput.value.trim());
        formData.append('type', 'bragsheet');
        
        statusEl.textContent = "Parsing & Ingesting Vectors...";
        statusEl.style.color = "#fbbf24";
        statusEl.style.display = 'inline';
        
        const res = await fetch('/api/profile/upload', {method: 'POST', body: formData});
        if (res.ok) {
            statusEl.textContent = "✅ Brag Sheet Ingested into Context Matrix!";
            statusEl.style.color = "#10b981";
        } else {
            statusEl.textContent = "❌ Ingestion Failed";
            statusEl.style.color = "#ef4444";
        }
        setTimeout(() => statusEl.style.display='none', 4000);
    });

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
        scrapeBtn.disabled = true;
        stopScrapeBtn.style.display = 'block';
        stopScrapeBtn.innerHTML = '<span class="icon">🛑</span> Stop';
        stopScrapeBtn.disabled = false;
        
        const isExecParams = execToggle.checked ? '?exec=true' : '';
        
        let dots = 0;
        const progressTexts = ["Initializing Scraper", "Contacting Job Boards", "Parsing Raw Data", "Filtering Irrelevant", "Saving Opportunities", "Finalizing"];
        let step = 0;
        const progressInterval = setInterval(() => {
            dots = (dots + 1) % 4;
            const ds = ".".repeat(dots);
            scrapeBtn.innerHTML = `<span class="icon">⏳</span> ${progressTexts[step] || "Working"}${ds}`;
            if (dots === 3 && step < progressTexts.length - 1) step++;
        }, 800);

        try {
            const res = await fetch('/api/scrape' + isExecParams, { method: 'POST' });
            clearInterval(progressInterval);
            if(res.ok) {
                const data = await res.json();
                alert(`Scraping complete! Added ${data.added} new jobs from ${data.scraped} found.`);
                scrapeBtn.innerHTML = '<span class="icon">⏳</span> Refreshing dashboard...';
                fetchJobs();
            } else if (res.status === 408 || res.status === 409 || res.status === 499 || String(res.status).startsWith("4")) {
                const errText = await res.text();
                alert(`Scraping aborted: ${errText.trim()}`);
            } else {
                alert('Scraping failed or timed out.');
            }
        } catch (e) {
            console.error(e);
            clearInterval(progressInterval);
            alert('Scraping connection dropped explicitly.');
        } finally {
            clearInterval(progressInterval);
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
                // Auto-select the first job so the user lands on a detail view
                if (jobsData.length > 0) {
                    selectJob(jobsData[0].id);
                }
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

    // sanitizeMarkdown specifically repairs LLM hallucinations involving syntax limits, like Pandoc underlines and dangling bold markers
    function sanitizeMarkdown(md) {
        if (!md) return "";
        let clean = md.replace(/\[([^\]]+)\]\{\.underline\}/g, '$1'); // Just extract Pandoc text, remove underline marking entirely
        
        // The user explicitly requested to remove all headers (#) and asterisks/bolding completely
        // Remove all hashtags
        clean = clean.replace(/#/g, '');
        
        // Remove all asterisks (this removes bolding **, italics *, and star bullets)
        // To preserve bullet points, we will convert asterisk bullets to hyphens first
        clean = clean.replace(/^\s*\*\s/gm, '- '); 
        clean = clean.replace(/\*/g, '');
        
        // Clean up any lingering backslashes that were escaping markdown (like \*\*)
        clean = clean.replace(/\\/g, '');

        return clean;
    }

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
            if (!res.ok) {
                const text = await res.text();
                throw new Error(`Fetch error ${res.status}: ${text}`);
            }
            jobsData = await res.json();
            
            if(!jobsData || jobsData.length === 0) {
                jobListEl.innerHTML = '<div class="empty-state">No jobs found. Click Scrape to start!</div>';
                jobCountEl.textContent = '0';
                return;
            }

            renderJobList();
        } catch(e) {
            console.error("Fetch jobs error", e);
            if (e.message.includes("401") || e.message.includes("403") || e.message.includes("Identity Token Required")) {
                localStorage.removeItem("tenant_id");
                activeTenant = null;
                if (authModal && !authModal.open) authModal.showModal();
                jobListEl.innerHTML = '<div class="empty-state" style="color:#ef4444;">Please login to view jobs.</div>';
                return;
            }
            jobListEl.innerHTML = '<div class="empty-state" style="color:#ef4444;">Failed to load jobs.</div>';
        }
    }

    function renderJobList() {
        jobListEl.innerHTML = '';
        let displayedCount = 0;

        jobsData.forEach(job => {
            if (minCompValue > 0) {
                const jobComp = extractMaxComp(job.compensation);
                const marketComp = extractMaxComp(job.market_salary);
                const actualMax = Math.max(jobComp, marketComp);
                if (actualMax < minCompValue) return; // filter this job out
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
                        ${availableResumes.length > 1 ? `
                            <select id="resume-template-select" style="background: rgba(255,255,255,0.05); color: white; border: 1px solid rgba(255,255,255,0.2); border-radius: 6px; padding: 0.5rem;">
                                ${availableResumes.map(r => `<option style="color: #333;" value="${r}">${r}</option>`).join('')}
                            </select>
                        ` : ''}
                        <button class="secondary-btn" id="open-export-modal-btn" style="display:none; color: #fff; border-color: rgba(255,255,255,0.3);">
                            <span class="icon">📄</span> View & Export
                        </button>
                    </div>
                    <div id="retailor-instructions-container" style="display:none; flex: 1; max-width: 300px; margin-left: 1rem;">
                        <input type="text" id="retailor-instructions-input" placeholder="Feedback (e.g. 'Emphasize cloud deployments')" style="width: 100%; padding: 0.5rem 0.75rem; border-radius: 6px; border: 1px solid rgba(255,255,255,0.2); background: rgba(0,0,0,0.2); color: #fff; font-size: 0.85rem;" />
                    </div>
                    <div class="tailor-status" id="tailor-status-area">
                        <span>Has not been tailored yet.</span>
                    </div>
                </div>

                <div id="tailored-output"></div>

                <div id="tailor-polling-overlay" style="display:none; padding: 2rem; background: rgba(0,0,0,0.2); border-radius: 12px; border: 1px dashed rgba(255,255,255,0.1); text-align: center; margin-bottom: 2rem;">
                    <div class="loader" style="width: 40px; height: 40px; border: 4px solid rgba(255,255,255,0.1); border-top: 4px solid #60a5fa; border-radius: 50%; animation: spin 1s linear infinite; margin: 0 auto 1rem;"></div>
                    <div style="font-weight: 600; color: #60a5fa; margin-bottom: 0.5rem;">Thinking...</div>
                    <div style="font-size: 0.85rem; color: var(--text-muted);">Qwen3 30B-A3B (MoE) is aligning your project history to this role.<br/>This usually takes 20-30 seconds.</div>
                </div>

                <h3>Original Description</h3>
                <br/>
                <div class="jd-box">${job.description.replace(/\n/g, '<br/>')}</div>
            </div>
        `;

        document.getElementById('run-tailor-btn').addEventListener('click', () => runTailor(job.id));

        // If the backend already cached the generation in the DB, prefill the exact layout instantly
        if (job.tailored_resume && job.tailored_resume.length > 0) {
            document.getElementById('run-tailor-btn').innerHTML = '<span class="icon">✨</span> Re-Tailor';
            document.getElementById('retailor-instructions-container').style.display = 'block';
            
            // Format mock result structure internally
            const cachedResult = {
                Score: job.score,
                SubScores: {
                    Technical: job.sub_score_tech || 0,
                    Domain: job.sub_score_domain || 0,
                    Seniority: job.sub_score_senior || 0
                },
                MarketSalary: job.market_salary,
                FitBrief: job.fit_brief,
                TailoredResume: job.tailored_resume,
                Report: job.tailored_report,
                CoverLetter: job.cover_letter
            };
            
            // Populate HUD
            const statusArea = document.getElementById('tailor-status-area');
            const outputArea = document.getElementById('tailored-output');
            
            // Extract subscores with fallbacks
            const ts = cachedResult.SubScores?.Technical || 0;
            const ds = cachedResult.SubScores?.Domain || 0;
            const ss = cachedResult.SubScores?.Seniority || 0;

            statusArea.innerHTML = `
                <div style="display: flex; gap: 1.5rem; align-items: center; align-content: center; flex-wrap: wrap;">
                    <div class="score-hud" style="text-align: center;">
                        <span class="val" style="font-size: 2.5rem; font-weight: 800; color: #10b981; display: block; line-height: 1;">${(cachedResult.Score || job.score || 0)}%</span>
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
                        <strong style="color: #c084fc; font-size: 1.1rem;">${cachedResult.MarketSalary || "Unknown"}</strong>
                    </div>
                </div>
            `;
            
            outputArea.innerHTML = `
                <div class="brief-box">
                    <strong>Why you fit:</strong> ${cachedResult.FitBrief}
                </div>
                <h3>Cover Letter</h3>
                <br/>
                <div class="resume-preview">${cachedResult.CoverLetter ? marked.parse(sanitizeMarkdown(cachedResult.CoverLetter)) : "<em>No cover letter generated.</em>"}</div>
                <br/>
                <h3>Tailored Resume Preview</h3>
                <br/>
                <div class="resume-preview">${marked.parse(sanitizeMarkdown(cachedResult.TailoredResume))}</div>
                <br/>
                <h3>Alteration Report</h3>
                <br/>
                <div class="resume-preview">${marked.parse(cachedResult.Report)}</div>
                <br/>
            `;

            // Wire up export button for cached results
            const exportBtn = document.getElementById('open-export-modal-btn');
            exportBtn.style.display = 'inline-flex';
            exportBtn.onclick = () => {
                const modal = document.getElementById('resume-modal');
                const printHtml = document.getElementById('tailored-resume-html');
                const clGenHtml = cachedResult.CoverLetter ? `<h2>Cover Letter</h2>` + marked.parse(sanitizeMarkdown(cachedResult.CoverLetter)) + `<hr />` : '';
                printHtml.innerHTML = clGenHtml + marked.parse(sanitizeMarkdown(cachedResult.TailoredResume));
                modal.showModal();
            };

        } else if (job.tailoring_status === 'processing') {
            document.getElementById('tailor-polling-overlay').style.display = 'block';
            document.getElementById('run-tailor-btn').disabled = true;
            document.getElementById('run-tailor-btn').innerHTML = '<span class="icon">⏳</span> Processing...';
            startPolling(job.id);
        }
    }

    function startPolling(id) {
        if (window._tailorPollingInterval) clearInterval(window._tailorPollingInterval);
        
        window._tailorPollingInterval = setInterval(async () => {
            try {
                const res = await fetch(`/api/jobs/status/${id}`);
                const job = await res.json();
                
                if (job.tailoring_status === 'completed') {
                    clearInterval(window._tailorPollingInterval);
                    document.getElementById('tailor-polling-overlay').style.display = 'none';
                    // Update the local data
                    const idx = jobsData.findIndex(j => j.id === id);
                    if (idx !== -1) jobsData[idx] = job;
                    
                    // Re-render if still looking at this job
                    if (activeJobId === id) {
                        if (!job.tailored_resume || job.tailored_resume.length < 100) {
                            // Parsing failed on backend — show an actionable error
                            document.getElementById('tailored-output').innerHTML = `
                                <div style="padding: 1.5rem; border: 1px solid rgba(239,68,68,0.4); border-radius: 10px; background: rgba(239,68,68,0.08); color: #fca5a5;">
                                    <strong>⚠️ Tailoring Completed But Resume Parse Failed</strong>
                                    <p style="margin-top: 0.5rem; font-size: 0.9rem;">The LLM completed but the structured output could not be extracted.  This usually means the model ignored the required format markers. Try clicking <em>Re-Tailor</em> — a second attempt typically succeeds.</p>
                                </div>`;
                            document.getElementById('run-tailor-btn').innerHTML = '<span class="icon">✨</span> Re-Tailor';
                            document.getElementById('retailor-instructions-container').style.display = 'block';
                            document.getElementById('run-tailor-btn').disabled = false;
                        } else {
                            selectJob(id);
                        }
                    }
                } else if (job.tailoring_status === 'failed') {
                    clearInterval(window._tailorPollingInterval);
                    document.getElementById('tailor-polling-overlay').style.display = 'none';
                    document.getElementById('tailored-output').innerHTML = `
                        <div style="padding: 1.5rem; border: 1px solid rgba(239,68,68,0.4); border-radius: 10px; background: rgba(239,68,68,0.08); color: #fca5a5;">
                            <strong>❌ Tailoring Failed</strong>
                            <p style="margin-top: 0.5rem; font-size: 0.9rem;">The backend LLM process failed. Make sure Ollama is running, or that your GEMINI_API_KEY / ANTHROPIC_API_KEY is set. Click <em>Re-Tailor</em> to retry.</p>
                        </div>`;
                    document.getElementById('run-tailor-btn').innerHTML = '<span class="icon">✨</span> Re-Tailor';
                    document.getElementById('retailor-instructions-container').style.display = 'block';
                    document.getElementById('run-tailor-btn').disabled = false;
                }
            } catch (e) {
                console.error("Polling error", e);
            }
        }, 3000);
    }

    async function runTailor(id) {
        const btn = document.getElementById('run-tailor-btn');
        const statusArea = document.getElementById('tailor-status-area');
        const outputArea = document.getElementById('tailored-output');

        btn.innerHTML = '<span class="icon">⏳</span> Tailoring (Takes ~30s)...';
        btn.disabled = true;

        let templateQuery = "";
        const selectBox = document.getElementById('resume-template-select');
        if (selectBox) {
            templateQuery = "?template=" + encodeURIComponent(selectBox.value);
        } else if (availableResumes.length === 1) {
            templateQuery = "?template=" + encodeURIComponent(availableResumes[0]);
        } else {
            templateQuery = "?template=base_resume.md";
        }

        const instrInput = document.getElementById('retailor-instructions-input');
        if (instrInput && instrInput.value.trim() !== '') {
            templateQuery += "&instructions=" + encodeURIComponent(instrInput.value.trim());
        }

        try {
            const res = await fetch(`/api/jobs/tailor/${id}${templateQuery}`, { method: 'POST' });
            if(res.status === 202) {
                // Accepted for background processing
                document.getElementById('tailor-polling-overlay').style.display = 'block';
                const jobIndex = jobsData.findIndex(j => j.id === id);
                if(jobIndex !== -1) jobsData[jobIndex].tailoring_status = 'processing';
                startPolling(id);
                return;
            }
            
            if(!res.ok) {
                const errText = await res.text();
                throw new Error(errText.trim() || "Tailor failed on backend");
            }
            
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
                <h3>Cover Letter</h3>
                <br/>
                <div class="resume-preview">${result.CoverLetter ? marked.parse(sanitizeMarkdown(result.CoverLetter)) : "<em>No cover letter generated.</em>"}</div>
                <br/>
                <h3>Tailored Resume Preview</h3>
                <br/>
                <div class="resume-preview">${marked.parse(sanitizeMarkdown(result.TailoredResume))}</div>
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
                
                const clGenHtml = result.CoverLetter ? `<h2>Cover Letter</h2>` + marked.parse(sanitizeMarkdown(result.CoverLetter)) + `<hr />` : '';
                printHtml.innerHTML = clGenHtml + marked.parse(sanitizeMarkdown(result.TailoredResume));
                modal.showModal();
            };

            // Vital: Update the local javascript memory list so if the user hits BACK without refreshing, it dynamically remembers!
            const jobIndex = jobsData.findIndex(j => j.id === id);
            if(jobIndex !== -1) {
                jobsData[jobIndex].tailored_resume = result.TailoredResume;
                jobsData[jobIndex].tailored_report = result.Report;
                jobsData[jobIndex].cover_letter = result.CoverLetter;
                jobsData[jobIndex].fit_brief = result.FitBrief;
                jobsData[jobIndex].market_salary = result.MarketSalary;
                jobsData[jobIndex].score = result.Score;
            }

        } catch(e) {
            console.error(e);
            alert("Error running tailor process. Ensure Ollama/Redis are running.\n\nDetails: " + e.message);
        } finally {
            btn.innerHTML = '<span class="icon">✨</span> Re-Tailor';
            btn.disabled = false;
        }
    }

    // Modal Export Bindings
    const resModal = document.getElementById('resume-modal');
    document.getElementById('close-modal-btn').onclick = () => resModal.close();
    
    document.getElementById('export-pdf-btn').onclick = () => {
        const d = new Date();
        const ymd = d.getFullYear() + String(d.getMonth()+1).padStart(2,'0') + String(d.getDate()).padStart(2,'0');
        const activeJob = jobsData.find(j => j.id === activeJobId);
        const safeCompany = (activeJob && activeJob.company) ? activeJob.company.replace(/[^A-Za-z0-9]/g, '_') : 'Resume';
        const exportName = `${ymd}-Mike_Lee_${safeCompany}`;

        const content = document.getElementById('tailored-resume-html').innerHTML;
        const htmlPayload = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>${exportName}</title><style>
            body { font-family: 'Helvetica Neue', Helvetica, Arial, sans-serif; padding: 20px; line-height: 1.5; color: #000; background: #fff; }
            h1, h2, h3 { margin-top: 1.5em; margin-bottom: 0.5em; }
            hr { border: 1px solid #ccc; margin: 2em 0; }
        </style></head><body>${content}</body></html>`;

        const iframe = document.createElement('iframe');
        iframe.style.position = 'absolute';
        iframe.style.width = '0px';
        iframe.style.height = '0px';
        iframe.style.border = 'none';
        document.body.appendChild(iframe);

        const doc = iframe.contentWindow.document;
        doc.open();
        doc.write(htmlPayload);
        doc.close();

        iframe.contentWindow.focus();
        setTimeout(() => {
            iframe.contentWindow.print();
            document.body.removeChild(iframe);
        }, 250);
    };

    document.getElementById('export-docx-btn').onclick = () => {
        const content = document.getElementById('tailored-resume-html').innerHTML;
        // Construct a clean HTML string required by html-docx-js
        const htmlPayload = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Resume</title><style>body{font-family:sans-serif;}</style></head><body>${content}</body></html>`;
        
        const blob = htmlDocx.asBlob(htmlPayload);

        const d = new Date();
        const ymd = d.getFullYear() + String(d.getMonth()+1).padStart(2,'0') + String(d.getDate()).padStart(2,'0');
        const activeJob = jobsData.find(j => j.id === activeJobId);
        const safeCompany = (activeJob && activeJob.company) ? activeJob.company.replace(/[^A-Za-z0-9]/g, '_') : 'Resume';
        const exportName = `${ymd}-Mike_Lee_${safeCompany}.docx`;

        saveAs(blob, exportName);
    };

    document.getElementById('export-gdocs-btn').onclick = async () => {
        const btn = document.getElementById('export-gdocs-btn');
        const originalText = btn.innerHTML;
        btn.innerHTML = '<span class="icon">☁️</span> Uploading...';
        btn.disabled = true;
        
        try {
            const contentHtml = document.getElementById('tailored-resume-html').innerHTML;
            
            // Post payload to our Go driver
            const res = await fetch(`/api/export/gdocs/${activeJobId}`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ rawHtml: contentHtml })
            });

            if (!res.ok) {
                const errText = await res.text();
                throw new Error(errText);
            }

            const data = await res.json();
            alert("Success! Your resume has been uploaded natively to Google Docs.");
            window.open(data.url, '_blank');
        } catch(e) {
            console.error(e);
            
            // Graceful failure fallback check
            if (e.message.includes("missing credentials.json") || e.message.includes("OAuth token not found")) {
                alert(`Authentication Required: ${e.message}`);
            } else {
                alert(`Google Docs Export Failed: ${e.message}`);
            }
        } finally {
            btn.innerHTML = originalText;
            btn.disabled = false;
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

    // ── Company Report Modal ──────────────────────────────────────────────────
    const reportModal = document.getElementById('report-modal');

    document.getElementById('open-report-btn').addEventListener('click', async () => {
        reportModal.showModal();
        await fetchAndRenderReport();
    });

    document.getElementById('close-report-modal-btn').addEventListener('click', () => {
        reportModal.close();
    });

    async function fetchAndRenderReport() {
        const tbody = document.getElementById('report-table-body');
        const badge = document.getElementById('report-total-badge');
        const sourceEl = document.getElementById('source-breakdown');
        tbody.innerHTML = '<tr><td colspan="3" style="padding: 2rem; text-align: center; color: var(--text-muted);">Loading...</td></tr>';
        badge.textContent = '';
        sourceEl.innerHTML = '';

        const sourceMeta = {
            'remoteok':      { label: 'RemoteOK',         icon: '🌐' },
            'hn':            { label: 'HN Who\'s Hiring',  icon: '🟠' },
            'greenhouse':    { label: 'Greenhouse',        icon: '🌿' },
            'lever':         { label: 'Lever',             icon: '⚙️' },
            'ashby':         { label: 'Ashby',             icon: '🔷' },
            'agent-crawler': { label: 'Agent Crawler',     icon: '🤖' },
        };

        try {
            const [reportRes, sourceRes] = await Promise.all([
                fetch('/api/report/companies'),
                fetch('/api/report/sources'),
            ]);

            // ── Source breakdown pills ──────────────────────────────────────
            if (sourceRes.ok) {
                const sources = await sourceRes.json();
                const totalScraped = Object.values(sources).reduce((a, b) => a + b, 0);
                if (Object.keys(sources).length > 0) {
                    sourceEl.innerHTML = Object.entries(sources).map(([src, count]) => {
                        const meta = sourceMeta[src] || { label: src, icon: '📋' };
                        return `<div style="display:inline-flex;align-items:center;gap:0.4rem;padding:0.3rem 0.75rem;border-radius:99px;background:rgba(96,165,250,0.1);border:1px solid rgba(96,165,250,0.25);font-size:0.8rem;">
                            <span>${meta.icon}</span>
                            <span style="color:#94a3b8;">${meta.label}</span>
                            <strong style="color:#60a5fa;">${count}</strong>
                        </div>`;
                    }).join('') + `<div style="display:inline-flex;align-items:center;gap:0.4rem;padding:0.3rem 0.75rem;border-radius:99px;background:rgba(16,185,129,0.1);border:1px solid rgba(16,185,129,0.25);font-size:0.8rem;">
                        <span>📊</span><span style="color:#94a3b8;">Total</span><strong style="color:#10b981;">${totalScraped}</strong>
                    </div>`;
                }
            }

            // ── Company table ───────────────────────────────────────────────
            if (!reportRes.ok) throw new Error(await reportRes.text());
            reportData = await reportRes.json();
            if (!reportData) reportData = [];

            if (reportData.length === 0) {
                tbody.innerHTML = '<tr><td colspan="3" style="padding: 2rem; text-align: center; color: var(--text-muted);">No data yet. Run a scrape first.</td></tr>';
                return;
            }

            const totalJobs = reportData.reduce((s, r) => s + r.total_jobs, 0);
            const totalTailored = reportData.reduce((s, r) => s + r.tailored_count, 0);
            badge.textContent = `${reportData.length} companies · ${totalJobs} jobs · ${totalTailored} tailored`;

            tbody.innerHTML = reportData.map((row, i) => {
                const rowBg = i % 2 === 0 ? 'rgba(0,0,0,0.03)' : 'transparent';
                const tailoredColor = row.tailored_count > 0 ? '#10b981' : '#333';
                const safeCompany = escapeHtml(row.company).replace(/'/g, "\\'");
                const queryStr = row.tailored_count > 0 ? `${safeCompany} tailored` : safeCompany;
                return `<tr 
                    style="border-bottom: 1px solid rgba(0,0,0,0.1); background: ${rowBg}; cursor: pointer; transition: background 0.2s;" 
                    onmouseover="this.style.background='rgba(96,165,250,0.1)'" 
                    onmouseout="this.style.background='${rowBg}'"
                    onclick="document.getElementById('report-modal').close(); document.getElementById('semantic-search-input').value = '${queryStr}'; document.getElementById('semantic-search-btn').click();">
                    <td style="padding: 0.65rem 1.25rem; color: #000; font-weight: 500; text-decoration: underline;">${escapeHtml(row.company)}</td>
                    <td style="padding: 0.65rem 1.25rem; text-align: right; font-weight: 700; color: #60a5fa;">${row.total_jobs}</td>
                    <td style="padding: 0.65rem 1.25rem; text-align: right; font-weight: 700; color: ${tailoredColor};">${row.tailored_count > 0 ? '✅ ' + row.tailored_count : '—'}</td>
                </tr>`;
            }).join('');

        } catch(e) {
            console.error(e);
            tbody.innerHTML = `<tr><td colspan="3" style="padding: 2rem; text-align: center; color: #ef4444;">Failed to load report: ${e.message}</td></tr>`;
        }
    }


    document.getElementById('export-report-csv-btn').addEventListener('click', () => {
        if (!reportData || reportData.length === 0) {
            alert('No data to export. Open the report first.');
            return;
        }
        const header = 'Company,Jobs Found,Resumes Tailored';
        const rows = reportData.map(r =>
            `"${(r.company || '').replace(/"/g, '""')}",${r.total_jobs},${r.tailored_count}`
        );
        const csvContent = [header, ...rows].join('\n');
        const blob = new Blob([csvContent], { type: 'text/csv;charset=utf-8;' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        const d = new Date();
        a.href = url;
        a.download = `job-search-report-${d.getFullYear()}${String(d.getMonth()+1).padStart(2,'0')}${String(d.getDate()).padStart(2,'0')}.csv`;
        a.click();
        URL.revokeObjectURL(url);
    });

    // ── System Health Modal ───────────────────────────────────────────────────
    const healthModal = document.getElementById('health-modal');

    document.getElementById('open-health-btn').addEventListener('click', async () => {
        healthModal.showModal();
        await fetchHealth();
    });

    document.getElementById('close-health-btn').addEventListener('click', () => healthModal.close());
    document.getElementById('refresh-health-btn').addEventListener('click', fetchHealth);

    async function fetchHealth() {
        const listEl = document.getElementById('health-component-list');
        const overallEl = document.getElementById('health-overall-badge');
        listEl.innerHTML = '<div style="text-align:center;color:var(--text-muted);padding:2rem;">Checking...</div>';
        overallEl.textContent = '';

        try {
            const res = await fetch('/api/health');
            if (!res.ok) throw new Error('Health endpoint returned ' + res.status);
            const data = await res.json();

            const overallOk = data.overall === 'ok';
            updateHealthDot(data.overall);
            overallEl.textContent = overallOk ? '✅ All Systems Operational' : '⚠️ Degraded — Check components below';
            overallEl.style.color = overallOk ? '#10b981' : '#f59e0b';

            const componentMeta = {
                ollama:  { label: 'Ollama (qwen3:30b-a3b)', icon: '🧠' },
                redis:   { label: 'Redis Stack',            icon: '⚡' },
                sqlite:  { label: 'SQLite Database',        icon: '🗄️' },
                gemini:  { label: 'Gemini (Cloud Failover)',icon: '☁️' },
                claude:  { label: 'Claude (Cloud Failover)',icon: '☁️' },
            };

            const statusColor = { ok: '#10b981', degraded: '#f59e0b', error: '#ef4444' };
            const statusBg    = { ok: 'rgba(16,185,129,0.08)', degraded: 'rgba(245,158,11,0.08)', error: 'rgba(239,68,68,0.08)' };
            const statusBorder= { ok: 'rgba(16,185,129,0.25)', degraded: 'rgba(245,158,11,0.25)', error: 'rgba(239,68,68,0.25)' };
            const statusPill  = { ok: '● OK', degraded: '● Degraded', error: '● Error' };

            listEl.innerHTML = Object.entries(data.components).map(([key, comp]) => {
                const meta = componentMeta[key] || { label: key, icon: '🔧' };
                const color  = statusColor[comp.status] || '#94a3b8';
                const bg     = statusBg[comp.status]    || 'rgba(0,0,0,0.03)';
                const border = statusBorder[comp.status]|| 'rgba(0,0,0,0.1)';
                const pill   = statusPill[comp.status]  || comp.status;
                const latency = comp.latency ? `<span style="margin-left:auto;font-size:0.75rem;color:#4a4a4a;font-family:monospace;">${comp.latency}</span>` : '';
                return `
                    <div style="display:flex;align-items:center;gap:1rem;padding:0.85rem 1rem;border-radius:10px;background:${bg};border:1px solid ${border};">
                        <span style="font-size:1.3rem;">${meta.icon}</span>
                        <div style="flex:1;min-width:0;">
                            <div style="font-weight:600;font-size:0.9rem;color:#000;margin-bottom:0.2rem;">${meta.label}</div>
                            <div style="font-size:0.8rem;color:#333;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">${comp.detail}</div>
                        </div>
                        ${latency}
                        <span style="font-size:0.75rem;font-weight:700;color:${color};white-space:nowrap;padding:0.2rem 0.6rem;border-radius:99px;background:${bg};border:1px solid ${border};">${pill}</span>
                    </div>`;
            }).join('');

        } catch(e) {
            console.error(e);
            document.getElementById('health-component-list').innerHTML =
                `<div style="padding:2rem;text-align:center;color:#ef4444;">Could not reach health endpoint: ${e.message}</div>`;
        }
    }
});

