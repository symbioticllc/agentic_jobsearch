document.addEventListener('DOMContentLoaded', () => {
    const jobListEl = document.getElementById('job-list');
    const jobCountEl = document.getElementById('job-count');
    const jobDetailEl = document.getElementById('job-detail-content');
    const scrapeBtn = document.getElementById('scrape-btn');
    const vectorizeBtn = document.getElementById('vectorize-btn');
    const searchInput = document.getElementById('semantic-search-input');
    const searchBtn = document.getElementById('semantic-search-btn');
    const clearBtn = document.getElementById('clear-search-btn');
    const minCompSlider = document.getElementById('min-comp-slider');
    const minCompDisplay = document.getElementById('min-comp-display');
    const execToggle = document.getElementById('scrape-exec-toggle');

    let jobsData = [];
    let activeJobId = null;
    let minCompValue = 0;
    let activeRoleFilter = 'All';
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

        // Populate model dropdowns
        try {
            const mRes = await fetch('/api/models');
            const mData = await mRes.json();
            const fastSel = document.getElementById('fast-model-select');
            const deepSel = document.getElementById('deep-model-select');
            fastSel.innerHTML = '';
            deepSel.innerHTML = '';
            (mData.models || []).forEach(m => {
                const o1 = document.createElement('option');
                o1.value = m; o1.textContent = m;
                if (m === mData.active_fast) o1.selected = true;
                fastSel.appendChild(o1);

                const o2 = document.createElement('option');
                o2.value = m; o2.textContent = m;
                if (m === mData.active_deep) o2.selected = true;
                deepSel.appendChild(o2);
            });
        } catch(e) {
            document.getElementById('fast-model-select').innerHTML = '<option>Ollama unavailable</option>';
            document.getElementById('deep-model-select').innerHTML = '<option>Ollama unavailable</option>';
        }

        profileModal.showModal();
    });

    document.getElementById('save-models-btn').addEventListener('click', async () => {
        const fast = document.getElementById('fast-model-select').value;
        const deep = document.getElementById('deep-model-select').value;
        const statusEl = document.getElementById('model-save-status');
        const btn = document.getElementById('save-models-btn');

        btn.disabled = true;
        btn.innerHTML = '<span class="icon">⏳</span> Loading Models...';
        statusEl.style.display = 'none';

        try {
            const res = await fetch('/api/models/configure', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({fast, deep})
            });
            if (res.ok) {
                const data = await res.json();
                statusEl.textContent = `✅ Models saved! Fast: ${data.fast} | Deep: ${data.deep} — preloading in background.`;
                statusEl.style.color = '#10b981';
                statusEl.style.display = 'block';
            } else {
                const err = await res.text();
                statusEl.textContent = `❌ Failed: ${err.trim()}`;
                statusEl.style.color = '#ef4444';
                statusEl.style.display = 'block';
            }
        } catch(e) {
            statusEl.textContent = '❌ Connection error.';
            statusEl.style.color = '#ef4444';
            statusEl.style.display = 'block';
        } finally {
            btn.disabled = false;
            btn.innerHTML = '<span class="icon">💾</span> Save & Load Models';
        }
    });

    const clearCacheBtn = document.getElementById('clear-cache-btn');
    if (clearCacheBtn) {
        clearCacheBtn.addEventListener('click', async () => {
            if (!confirm('🚨 WARNING 🚨\n\nAre you sure you want to completely wipe all saved jobs, resumes, and brag sheets for this tenant? This action cannot be undone.')) {
                return;
            }
            
            clearCacheBtn.disabled = true;
            clearCacheBtn.innerHTML = '<span class="icon">⌛</span> Clearing Cache...';
            try {
                const res = await fetch('/api/profile/clear', { method: 'POST' });
                if (res.ok) {
                    // Cache cleared successfully
                    window.location.reload(); // Reload to start fresh
                } else {
                    const err = await res.text();
                    alert(`Failed to clear cache: ${err}`);
                    clearCacheBtn.disabled = false;
                    clearCacheBtn.innerHTML = '<span class="icon">🗑️</span> Clear All Cache Data';
                }
            } catch (e) {
                console.error(e);
                alert('Connection error. Could not clear cache.');
                clearCacheBtn.disabled = false;
                clearCacheBtn.innerHTML = '<span class="icon">🗑️</span> Clear All Cache Data';
            }
        });
    }

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

    document.getElementById('tailored-only-toggle').addEventListener('change', () => {
        renderJobList();
        if (activeJobId) {
            selectJob(activeJobId);
        }
    });

    // Event Listeners
    stopScrapeBtn.addEventListener('click', async () => {
        try {
            stopScrapeBtn.innerHTML = '<span class="icon">⌛</span> Stopping...';
            stopScrapeBtn.disabled = true;
            await fetch('/api/scrape/stop', { method: 'POST' });
            stopScrapeProgressPolling();
        } catch(e) {
            console.error(e);
        } finally {
            stopScrapeBtn.innerHTML = '<span class="icon">🛑</span> Stop';
            stopScrapeBtn.disabled = false;
            stopScrapeBtn.style.display = 'none';
        }
    });

    // ── Scrape Progress Polling ───────────────────────────────────────────────
    let _scrapePoller = null;

    function stopScrapeProgressPolling() {
        if (_scrapePoller) {
            clearInterval(_scrapePoller);
            _scrapePoller = null;
        }
    }

    function formatElapsed(secs) {
        if (secs < 60) return `${secs}s`;
        return `${Math.floor(secs/60)}m ${secs % 60}s`;
    }

    function updateScrapeProgress(prog) {
        const statusText  = document.getElementById('scrape-status-text');
        const elapsed     = document.getElementById('scrape-elapsed');
        const progressBar = document.getElementById('scrape-progress-bar');
        const badges      = document.getElementById('scrape-source-badges');
        const jobsFound   = document.getElementById('scrape-jobs-found');
        const spinner     = document.getElementById('scrape-spinner');
        const inlineStop  = document.getElementById('scrape-inline-stop-btn');
        const statusBar   = document.getElementById('scrape-status-bar');

        // Elapsed timer
        if (elapsed) elapsed.textContent = formatElapsed(prog.elapsed_seconds || 0);

        // Progress bar: proportional to sources completed
        const total = prog.sources_total || 1;
        const done  = prog.sources_done  || 0;
        const pct   = Math.round((done / total) * 100);
        if (progressBar) {
            // While running use at least 8% so the bar is always visible
            progressBar.style.width = prog.running ? `${Math.max(8, pct)}%` : '100%';
            if (!prog.running) {
                progressBar.style.background = 'linear-gradient(90deg,#10b981,#34d399)';
            }
        }

        // Per-source badge pills
        if (badges && prog.source_statuses) {
            badges.innerHTML = '';
            Object.entries(prog.source_statuses).forEach(([name, status]) => {
                const colors = {
                    running: { bg: 'rgba(96,165,250,0.12)',  border: 'rgba(96,165,250,0.35)',  text: '#60a5fa', icon: '⟳' },
                    done:    { bg: 'rgba(16,185,129,0.12)',  border: 'rgba(16,185,129,0.35)',  text: '#10b981', icon: '✓' },
                    error:   { bg: 'rgba(239,68,68,0.12)',   border: 'rgba(239,68,68,0.35)',   text: '#f87171', icon: '✗' },
                };
                const c = colors[status] || colors.running;
                const pill = document.createElement('span');
                pill.style.cssText = `display:inline-flex;align-items:center;gap:0.2rem;padding:0.15rem 0.45rem;border-radius:4px;font-size:0.72rem;font-weight:600;background:${c.bg};border:1px solid ${c.border};color:${c.text};`;
                pill.innerHTML = `<span style="font-size:0.7rem;">${c.icon}</span>${name}`;
                badges.appendChild(pill);
            });
        }

        // Live job count
        if (jobsFound) {
            const n = prog.jobs_found || 0;
            jobsFound.textContent = n > 0 ? `${n} job${n !== 1 ? 's' : ''} found` : '';
        }

        // Status label
        if (statusText) {
            if (!prog.running) {
                statusText.textContent = `✅ Scan complete`;
                statusText.style.color = '#10b981';
                if (spinner) spinner.style.opacity = '0';
                if (inlineStop) inlineStop.style.display = 'none';
                // Refresh job list to show newly found jobs
                fetchJobs();
                // Auto-dismiss after 5s
                setTimeout(() => {
                    if (statusBar) {
                        statusBar.style.display = 'none';
                        // Reset styles for next use
                        statusText.style.color = '';
                        if (progressBar) { progressBar.style.width = '0%'; progressBar.style.background = 'linear-gradient(90deg,#3b82f6,#8b5cf6)'; }
                    }
                    stopScrapeProgressPolling();
                }, 5000);
            } else {
                statusText.textContent = `🔍 Scanning job boards... (${done}/${total} sources)`;
                statusText.style.color = '';
                if (inlineStop) inlineStop.style.display = 'inline-block';
            }
        }
    }

    function startScrapeProgressPolling() {
        stopScrapeProgressPolling();
        // Poll immediately then every 1.5s
        const poll = async () => {
            try {
                const res = await fetch('/api/scrape/status');
                if (!res.ok) return;
                const prog = await res.json();
                updateScrapeProgress(prog);
                // Stop polling once scrape is done and we've shown the complete state
                if (!prog.running) stopScrapeProgressPolling();
            } catch(e) {
                console.warn('Scrape status poll error:', e);
            }
        };
        poll();
        _scrapePoller = setInterval(poll, 1500);
    }

    scrapeBtn.addEventListener('click', async () => {
        scrapeBtn.disabled = true;

        const params = new URLSearchParams();
        if (execToggle.checked) params.set('exec', 'true');
        const kwInput = document.getElementById('custom-keywords-input');
        if (kwInput && kwInput.value.trim()) params.set('keywords', kwInput.value.trim());
        const qStr = params.toString() ? '?' + params.toString() : '';

        const statusBar  = document.getElementById('scrape-status-bar');
        const statusText = document.getElementById('scrape-status-text');
        const progressBar = document.getElementById('scrape-progress-bar');

        // Reset and show progress widget
        statusBar.style.display = 'block';
        statusBar.style.borderColor = '';
        statusBar.style.background = '';
        if (progressBar) { progressBar.style.width = '8%'; progressBar.style.background = 'linear-gradient(90deg,#3b82f6,#8b5cf6)'; }
        const badgesEl = document.getElementById('scrape-source-badges');
        if (badgesEl) badgesEl.innerHTML = '';
        const jobsEl = document.getElementById('scrape-jobs-found');
        if (jobsEl) jobsEl.textContent = '';
        const elapsedEl = document.getElementById('scrape-elapsed');
        if (elapsedEl) elapsedEl.textContent = '0s';
        const spinner = document.getElementById('scrape-spinner');
        if (spinner) spinner.style.opacity = '1';
        statusText.textContent = 'Starting scrape...';
        statusText.style.color = '';

        try {
            const res = await fetch('/api/scrape' + qStr, { method: 'POST' });
            if (res.ok) {
                // Kick off live polling
                startScrapeProgressPolling();
            } else {
                const errText = await res.text();
                statusText.textContent = `⚠️ Failed: ${errText.trim()}`;
                statusBar.style.borderColor = 'rgba(245,158,11,0.3)';
                setTimeout(() => { statusBar.style.display = 'none'; statusBar.style.borderColor = ''; }, 5000);
            }
        } catch (e) {
            console.error(e);
            statusText.textContent = '❌ Connection dropped.';
            setTimeout(() => { statusBar.style.display = 'none'; }, 4000);
        } finally {
            scrapeBtn.disabled = false;
        }
    });

    if (vectorizeBtn) {
        vectorizeBtn.addEventListener('click', async () => {
            vectorizeBtn.disabled = true;
            vectorizeBtn.innerHTML = '<span class="icon">⌛</span> Backgrounding...';
            
            const statusBar = document.getElementById('scrape-status-bar');
            const statusText = document.getElementById('scrape-status-text');
            statusBar.style.display = 'block';
            statusText.textContent = 'Initiating Semantic Vectorization...';

            try {
                const res = await fetch('/api/vectorize', { method: 'POST' });
                if (res.ok) {
                    const data = await res.json();
                    statusText.textContent = `✅ Redis Ingestion Backgrounded! Status: ${data.status}`;
                    statusBar.style.borderColor = 'rgba(16,185,129,0.3)';
                    statusBar.style.background = 'rgba(16,185,129,0.08)';
                    statusText.style.color = '#10b981';
                    setTimeout(() => { statusBar.style.display = 'none'; statusBar.style.borderColor = ''; statusBar.style.background = ''; statusText.style.color = ''; }, 5000);
                } else {
                    statusText.textContent = '❌ Interpret/Vector failed.';
                    setTimeout(() => { statusBar.style.display = 'none'; }, 4000);
                }
            } catch(e) {
                console.error(e);
                statusText.textContent = '❌ Interpret/Vector connection dropped.';
                setTimeout(() => { statusBar.style.display = 'none'; }, 4000);
            } finally {
                vectorizeBtn.disabled = false;
                vectorizeBtn.innerHTML = '<span class="icon">🧠</span> Interpret & Vectorize';
            }
        });
    }

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

    // sanitizeMarkdown removes structural LLM artifacts and Pandoc formatting noise.
    // ── Job category classifier (mirrors Go backend logic) ──────────────────
    const CATEGORY_META = {
        'Executive / VP':          { color: '#f97316', bg: 'rgba(249,115,22,0.15)',  icon: '👑' },
        'Director':                { color: '#ec4899', bg: 'rgba(236,72,153,0.15)',  icon: '🎯' },
        'Engineering Manager':     { color: '#8b5cf6', bg: 'rgba(139,92,246,0.15)', icon: '🧭' },
        'Staff / Principal IC':    { color: '#3b82f6', bg: 'rgba(59,130,246,0.15)', icon: '⭐' },
        'Senior Engineer':         { color: '#06b6d4', bg: 'rgba(6,182,212,0.15)',  icon: '🔧' },
        'Product Management':      { color: '#10b981', bg: 'rgba(16,185,129,0.15)', icon: '📦' },
        'Platform / Infrastructure':{ color: '#6366f1', bg: 'rgba(99,102,241,0.15)', icon: '⚙️' },
        'Security':                { color: '#ef4444', bg: 'rgba(239,68,68,0.15)',   icon: '🛡️' },
        'Data / ML':               { color: '#f59e0b', bg: 'rgba(245,158,11,0.15)', icon: '📊' },
        'Other':                   { color: '#94a3b8', bg: 'rgba(148,163,184,0.1)', icon: '💼' },
    };

    function classifyJobCategory(title) {
        const t = (title || '').toLowerCase();
        if (/chief|\bcto\b|\bcio\b|\bciso\b|\bvp\b|vice president|\bsvp\b|\bevp\b|managing director/.test(t)) return 'Executive / VP';
        if (/director/.test(t)) return 'Director';
        if (/engineering manager|head of engineering|head of platform|head of infra/.test(t)) return 'Engineering Manager';
        if (/\bstaff\b|\bprincipal\b|\bdistinguished\b|\bfellow\b/.test(t)) return 'Staff / Principal IC';
        if (/\bsenior\b|\bsr\.\b|\bsr\b/.test(t)) return 'Senior Engineer';
        if (/product manager|product management|product owner|product specialist|product lead/.test(t)) return 'Product Management';
        if (/platform|infrastructure|devops|\bsre\b|reliability|site reliability/.test(t)) return 'Platform / Infrastructure';
        if (/security|vulnerability|compliance|\bciso\b|\bsoc\b/.test(t)) return 'Security';
        if (/\bdata\b|machine learning|\bml\b|\bai\b|analytics|scientist/.test(t)) return 'Data / ML';
        return 'Other';
    }

    function getCategoryBadge(title, opts = {}) {
        const cat = classifyJobCategory(title);
        const meta = CATEGORY_META[cat] || CATEGORY_META['Other'];
        const size = opts.small ? '0.62rem' : '0.7rem';
        const pad  = opts.small ? '0.1rem 0.35rem' : '0.15rem 0.5rem';
        return `<span style="font-size:${size}; padding:${pad}; border-radius:99px; font-weight:600; background:${meta.bg}; color:${meta.color}; white-space:nowrap; letter-spacing:0.2px;">${meta.icon} ${cat}</span>`;
    }

    // We preserve heading markers (#) and bolding (**) for the resume section,
    // but clean up Pandoc-specific inline formatting that breaks markdown parsers.
    function sanitizeMarkdown(md) {
        if (!md) return "";
        // Remove Pandoc {.underline} inline spans, keep the visible text
        let clean = md.replace(/\[([^\]]+)\]\{\.underline\}/g, '$1');

        // Strip orphaned backslash escape artifacts from Pandoc docx export
        clean = clean.replace(/\\\|/g, '|');
        clean = clean.replace(/\\\*/g, '*');
        clean = clean.replace(/\\,/g, ',');

        // Strip lone trailing backslashes (Pandoc line breaks)
        clean = clean.replace(/\\\s*$/gm, '');

        // Collapse excessive blank lines
        clean = clean.replace(/\n{4,}/g, '\n\n\n');

        return clean;
    }

    clearBtn.addEventListener('click', () => {
        searchInput.value = '';
        
        // Reset compensation filter to defaults
        minCompSlider.value = 0;
        minCompValue = 0;
        minCompDisplay.textContent = '$0';

        // Reset role filter
        activeRoleFilter = 'All';
        document.querySelectorAll('.role-filter-btn').forEach(b => {
            b.classList.remove('active');
            if (b.dataset.role === 'All') b.classList.add('active');
        });

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

    document.querySelectorAll('.role-filter-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('.role-filter-btn').forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            activeRoleFilter = btn.dataset.role;
            renderJobList();
        });
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
            if (activeRoleFilter !== 'All') {
                if (classifyJobCategory(job.title) !== activeRoleFilter) return;
            }

            if (minCompValue > 0) {
                const jobComp = extractMaxComp(job.compensation);
                const marketComp = extractMaxComp(job.market_salary);
                const actualMax = Math.max(jobComp, marketComp);
                if (actualMax < minCompValue) return; // filter this job out
            }

            if (document.getElementById('tailored-only-toggle').checked) {
                if (!job.tailored_resume || job.tailored_resume.length === 0) return;
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

            if (job.applied) {
                matchHtml += `<div style="color: #10b981; font-weight: 800; font-size: 0.85rem; margin-top: 0.5rem; display: flex; align-items: center; gap: 0.25rem;">
                    <span>✅ Formal Application Submitted</span>
                </div>`;
            }

            card.innerHTML = `
                <div class="job-title">${job.title}</div>
                <div class="job-company" style="display:flex; align-items:center; gap:0.5rem; flex-wrap:wrap;">
                    ${job.company}
                    ${getCategoryBadge(job.title, { small: true })}
                </div>
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

    function renderJobDetail(job, openTailoredTab = false) {
        const hasTailoredData = job.tailored_resume && job.tailored_resume.length > 0;
        const initTabJob = openTailoredTab ? '' : 'active';
        const initPanelJob = openTailoredTab ? '' : 'active';
        const initTabTailor = openTailoredTab ? 'active' : '';
        const initPanelTailor = openTailoredTab ? 'active' : '';
        const displayBadge = (hasTailoredData || job.tailoring_status === 'processing') ? 'inline-flex' : 'none';
        const badgeClass = job.tailoring_status === 'processing' ? 'tab-badge processing' : 'tab-badge';
        const badgeContent = job.tailoring_status === 'processing' ? '⏳' : '✓';

        jobDetailEl.innerHTML = `
            <div class="detail-view" style="padding: 0;">
                <div class="detail-tabs">
                    <button class="detail-tab-btn ${initTabJob}" id="tab-btn-job">📋 Job Info</button>
                    <button class="detail-tab-btn ${initTabTailor}" id="tab-btn-tailored">✨ Tailored Resume <span class="${badgeClass}" id="tab-badge-tailored" style="display:${displayBadge}">${badgeContent}</span></button>
                </div>
                
                <div class="tab-panel ${initPanelJob}" id="tab-panel-job" style="padding: 1.5rem;">
                    <h1 style="font-size: 1.35rem; font-weight: 700; letter-spacing: -0.02em;">${job.title}</h1>
                    <div class="company-info" style="margin-bottom: 0.75rem;">
                        <strong>${job.company}</strong> &bull; 
                        <a href="${job.url}" target="_blank">View Original Post ↗</a>
                        ${job.location ? ` &bull; <span style="color: var(--text-muted);">${job.location}</span>` : ''}
                        &bull; ${getCategoryBadge(job.title)}
                    </div>
                    ${job.compensation ? `<div style="color: #10b981; font-weight: 600; font-size: 0.95rem; margin-bottom: 1rem;">💰 ${job.compensation}</div>` : ''}

                    <div style="display: flex; align-items: center; gap: 0.75rem; flex-wrap: wrap; padding-bottom: 1rem; border-bottom: 1px solid rgba(255,255,255,0.06); margin-bottom: 1rem;">
                        <button class="primary-btn" id="run-tailor-btn" style="font-size: 0.82rem; padding: 0.45rem 1rem;">
                            <span class="icon">✨</span> Align & Tailor Resume
                        </button>
                        ${availableResumes.length > 1 ? `
                            <select id="resume-template-select" style="background: rgba(255,255,255,0.05); color: white; border: 1px solid rgba(255,255,255,0.15); border-radius: 6px; padding: 0.4rem 0.5rem; font-size: 0.8rem;">
                                ${availableResumes.map(r => `<option style="color: #333;" value="${r}">${r}</option>`).join('')}
                            </select>
                        ` : ''}
                        
                        <button class="secondary-btn" id="toggle-applied-btn" style="border-color: ${job.applied ? 'rgba(16,185,129,0.25)' : 'rgba(255,255,255,0.15)'}; color: ${job.applied ? '#10b981' : 'var(--text-muted)'}; font-size: 0.82rem; padding: 0.45rem 0.85rem;">
                            <span class="icon">${job.applied ? '✅' : '☑️'}</span> ${job.applied ? 'Applied' : 'Mark Applied'}
                        </button>
                        <div id="retailor-instructions-container" style="display:none; flex: 1; min-width: 200px;">
                            <input type="text" id="retailor-instructions-input" placeholder="Re-tailor feedback (e.g. 'Emphasize cloud')" style="width: 100%; padding: 0.4rem 0.65rem; border-radius: 6px; border: 1px solid rgba(255,255,255,0.15); background: rgba(0,0,0,0.2); color: #fff; font-size: 0.8rem;" />
                        </div>
                    </div>

                    <h3>Original Description</h3>
                    <br/>
                    <div class="jd-box">${job.description.replace(/\n/g, '<br/>')}</div>
                </div>

                <div class="tab-panel ${initPanelTailor}" id="tab-panel-tailored" style="padding: 1.5rem;">
                    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 1rem;">
                        <h2 style="font-size: 1.2rem; font-weight: 700;">Tailored Results</h2>
                        <button class="secondary-btn" id="open-export-modal-btn" style="display:none; color: #fff; border-color: rgba(255,255,255,0.2); font-size: 0.82rem; padding: 0.45rem 0.85rem;">
                            <span class="icon">📄</span> Export PDF
                        </button>
                    </div>

                    <div id="tailor-status-area" style="margin-bottom: 1rem;">
                        <div style="padding: 2rem; text-align: center; color: var(--text-muted); background: rgba(255,255,255,0.02); border-radius: 12px; border: 1px dashed rgba(255,255,255,0.1);">
                            Not yet tailored. Run the "Align & Tailor Resume" from the Job Info tab to get started.
                        </div>
                    </div>

                    <div id="tailor-polling-overlay" style="display:none; padding: 3rem; background: rgba(15, 23, 42, 0.4); border-radius: 12px; border: 1px dashed rgba(96, 165, 250, 0.3); text-align: center; margin-bottom: 2rem;">
                        <div class="loader" style="width: 40px; height: 40px; border: 4px solid rgba(255,255,255,0.1); border-top: 4px solid #60a5fa; border-radius: 50%; animation: spin 1s linear infinite; margin: 0 auto 1.5rem;"></div>
                        <div style="font-weight: 600; color: #60a5fa; margin-bottom: 0.5rem; font-size: 1.1rem;">Aligning your project history to this role...</div>
                        <div id="tailor-elapsed-timer" style="font-size: 1.25rem; font-weight: 800; color: #a78bfa; font-variant-numeric: tabular-nums; margin-bottom: 0.5rem;">00:00</div>
                        <div style="font-size: 0.85rem; color: var(--text-muted);">Please wait. Time varies by model and hardware.</div>
                    </div>

                    <div id="tailored-output"></div>
                </div>
            </div>
        `;

        // Tab switching logic
        const tabJob = document.getElementById('tab-btn-job');
        const tabTailored = document.getElementById('tab-btn-tailored');
        const panelJob = document.getElementById('tab-panel-job');
        const panelTailored = document.getElementById('tab-panel-tailored');

        tabJob.addEventListener('click', () => {
            tabJob.classList.add('active');
            tabTailored.classList.remove('active');
            panelJob.classList.add('active');
            panelTailored.classList.remove('active');
        });

        tabTailored.addEventListener('click', () => {
            tabTailored.classList.add('active');
            tabJob.classList.remove('active');
            panelTailored.classList.add('active');
            panelJob.classList.remove('active');
        });

        document.getElementById('run-tailor-btn').addEventListener('click', () => runTailor(job.id));

        document.getElementById('toggle-applied-btn').addEventListener('click', async () => {
            const btn = document.getElementById('toggle-applied-btn');
            btn.disabled = true;
            const newStatus = !job.applied;
            try {
                await fetch(`/api/jobs/apply/${job.id}`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ applied: newStatus })
                });
                job.applied = newStatus;
                selectJob(job.id); 
                renderJobList();
            } catch(e) {
                console.error("Failed to mark applied", e);
                btn.disabled = false;
            }
        });

        if (hasTailoredData) {
            document.getElementById('run-tailor-btn').innerHTML = '<span class="icon">✨</span> Re-Tailor (Using Latest Options)';
            document.getElementById('retailor-instructions-container').style.display = 'block';
            
            const cachedResult = {
                Score: job.score,
                SubScores: { Technical: job.sub_score_tech || 0, Domain: job.sub_score_domain || 0, Seniority: job.sub_score_senior || 0 },
                MarketSalary: job.market_salary,
                FitBrief: job.fit_brief,
                TailoredResume: job.tailored_resume,
                Report: job.tailored_report,
                CoverLetter: job.cover_letter
            };
            
            populateTailoringOutput(job.id, cachedResult);
        } else if (job.tailoring_status === 'processing') {
            document.getElementById('tailor-status-area').style.display = 'none';
            document.getElementById('tailor-polling-overlay').style.display = 'block';
            document.getElementById('run-tailor-btn').disabled = true;
            document.getElementById('run-tailor-btn').innerHTML = '<span class="icon">⏳</span> Processing...';
            startPolling(job.id);
        }
    }

    function populateTailoringOutput(id, result) {
        const statusArea = document.getElementById('tailor-status-area');
        const outputArea = document.getElementById('tailored-output');
        const exportBtn = document.getElementById('open-export-modal-btn');
        const ts = result.SubScores?.Technical || 0;
        const ds = result.SubScores?.Domain || 0;
        const ss = result.SubScores?.Seniority || 0;

        statusArea.style.display = 'block';
        statusArea.innerHTML = `
            <div style="display: grid; grid-template-columns: auto 1fr auto; gap: 1.5rem; align-items: center; padding: 1.25rem 1.5rem; border-radius: 12px; background: rgba(16, 185, 129, 0.05); border: 1px solid rgba(16, 185, 129, 0.15);">
                <div style="text-align: center; padding-right: 0.75rem;">
                    <span style="font-size: 2.25rem; font-weight: 800; color: #10b981; display: block; line-height: 1;">${result.Score}%</span>
                    <span style="font-size: 0.7rem; font-weight: 700; text-transform: uppercase; color: var(--text-muted); letter-spacing: 1px;">Fit Score</span>
                </div>
                <div style="display: grid; grid-template-columns: 1fr; gap: 0.4rem; border-left: 1px solid rgba(255,255,255,0.08); padding-left: 1.5rem; font-size: 0.85rem;">
                    <div style="display: flex; justify-content: space-between;"><span style="color: var(--text-muted);">Technical Alignment</span><strong style="color: #60a5fa;">${ts}%</strong></div>
                    <div style="display: flex; justify-content: space-between;"><span style="color: var(--text-muted);">Domain Expertise</span><strong style="color: #60a5fa;">${ds}%</strong></div>
                    <div style="display: flex; justify-content: space-between;"><span style="color: var(--text-muted);">Seniority Fit</span><strong style="color: #60a5fa;">${ss}%</strong></div>
                </div>
                <div style="text-align: right; border-left: 1px solid rgba(255,255,255,0.08); padding-left: 1.5rem;">
                    <span style="font-size: 0.7rem; font-weight: 700; text-transform: uppercase; color: var(--text-muted); letter-spacing: 1px; display: block;">Market Estimate</span>
                    <strong style="color: #c084fc; font-size: 1.15rem;">${result.MarketSalary || "Unknown"}</strong>
                </div>
            </div>
        `;
        
        outputArea.innerHTML = `
            <div class="tailored-section-card" style="background: rgba(96, 165, 250, 0.05); border-color: rgba(96, 165, 250, 0.15);">
                <h3 style="color: #60a5fa;">🧠 Candidate Fit Analysis</h3>
                <div style="font-size: 0.95rem; line-height: 1.6; color: #e2e8f0;">${result.FitBrief}</div>
            </div>
            
            <div id="score-history-panel" style="margin-bottom: 1.25rem;"></div>

            <div class="tailored-section-card">
                <h3><span class="icon">📝</span> Cover Letter</h3>
                <div class="resume-preview">${result.CoverLetter ? marked.parse(sanitizeMarkdown(result.CoverLetter)) : "<em>No cover letter generated.</em>"}</div>
            </div>

            <div class="tailored-section-card">
                <h3><span class="icon">📄</span> Tailored Resume</h3>
                <div class="resume-preview">${marked.parse(sanitizeMarkdown(result.TailoredResume))}</div>
            </div>

            <div class="tailored-section-card">
                <h3><span class="icon">📊</span> Alteration Report</h3>
                <div class="resume-preview">${marked.parse(result.Report)}</div>
            </div>
        `;

        loadScoreHistory(id);

        if (exportBtn) {
            exportBtn.style.display = 'inline-flex';
            exportBtn.onclick = () => {
                const modal = document.getElementById('resume-modal');
                const printHtml = document.getElementById('tailored-resume-html');
                let printContent = '';
                if (result.CoverLetter) {
                    printContent += `<div class="doc-section cover-letter-section"><h2 style="margin-bottom:1.5rem;">Cover Letter</h2>${marked.parse(sanitizeMarkdown(result.CoverLetter))}</div><div class="doc-page-break"></div>`;
                }
                printContent += `<div class="doc-section resume-section">${marked.parse(sanitizeMarkdown(result.TailoredResume))}</div>`;
                printHtml.innerHTML = printContent;
                modal.showModal();
            };
        }
    }

    function startPolling(id) {
        if (window._tailorPollingInterval) clearInterval(window._tailorPollingInterval);
        if (window._tailorTimerInterval)  clearInterval(window._tailorTimerInterval);

        const tailorStart = Date.now();
        const btn = document.getElementById('run-tailor-btn');
        const timerEl = document.getElementById('tailor-elapsed-timer');

        window._tailorTimerInterval = setInterval(() => {
            const elapsed = Math.floor((Date.now() - tailorStart) / 1000);
            const mins = String(Math.floor(elapsed / 60)).padStart(2, '0');
            const secs = String(elapsed % 60).padStart(2, '0');
            const label = `${mins}:${secs}`;
            if (btn) btn.innerHTML = `<span class="icon">⏳</span> Tailoring... ${label}`;
            if (timerEl) timerEl.textContent = label;
        }, 1000);

        const CLIENT_TIMEOUT_MS = 20 * 60 * 1000;

        window._tailorPollingInterval = setInterval(async () => {
            try {
                const res = await fetch(`/api/jobs/status/${id}`);
                const statusData = await res.json();
                const job = statusData.job || statusData;
                
                if (job.tailoring_status === 'completed') {
                    clearInterval(window._tailorPollingInterval);
                    clearInterval(window._tailorTimerInterval);
                    
                    if (document.getElementById('tailor-polling-overlay')) {
                        document.getElementById('tailor-polling-overlay').style.display = 'none';
                    }
                    
                    const idx = jobsData.findIndex(j => j.id === id);
                    if (idx !== -1) jobsData[idx] = job;
                    
                    if (activeJobId === id) {
                        if (!job.tailored_resume || job.tailored_resume.length < 100) {
                            document.getElementById('tailored-output').innerHTML = `
                                <div style="padding: 1.5rem; border: 1px solid rgba(239,68,68,0.4); border-radius: 10px; background: rgba(239,68,68,0.08); color: #fca5a5;">
                                    <strong>⚠️ Tailoring Completed But Resume Parse Failed</strong>
                                    <p style="margin-top: 0.5rem; font-size: 0.9rem;">The LLM completed but the structured output could not be extracted. Re-Tailor usually succeeds.</p>
                                </div>`;
                            if (btn) {
                                btn.innerHTML = '<span class="icon">✨</span> Re-Tailor';
                                btn.disabled = false;
                            }
                            document.getElementById('retailor-instructions-container').style.display = 'block';
                            document.getElementById('tab-btn-tailored').click(); // Auto-switch to show error
                        } else {
                            // Re-render job detail entirely and open the Tailored tab!
                            // wait, we pass true to openTailoredTab
                            renderJobDetail(job, true); 
                        }
                    }
                } else if (job.tailoring_status === 'failed') {
                    clearInterval(window._tailorPollingInterval);
                    clearInterval(window._tailorTimerInterval);
                    if (document.getElementById('tailor-polling-overlay')) {
                        document.getElementById('tailor-polling-overlay').style.display = 'none';
                    }
                    if (activeJobId === id) {
                        document.getElementById('tailored-output').innerHTML = `
                            <div style="padding: 1.5rem; border: 1px solid rgba(239,68,68,0.4); border-radius: 10px; background: rgba(239,68,68,0.08); color: #fca5a5;">
                                <strong>❌ Tailoring Failed</strong>
                                <p style="margin-top: 0.5rem; font-size: 0.9rem;">The backend LLM process failed. Click Re-Tailor to retry.</p>
                            </div>`;
                        if (btn) {
                            btn.innerHTML = '<span class="icon">✨</span> Re-Tailor';
                            btn.disabled = false;
                        }
                        document.getElementById('retailor-instructions-container').style.display = 'block';
                        document.getElementById('tab-btn-tailored').click();
                    }
                } else if (Date.now() - tailorStart > CLIENT_TIMEOUT_MS) {
                    clearInterval(window._tailorPollingInterval);
                    clearInterval(window._tailorTimerInterval);
                    if (document.getElementById('tailor-polling-overlay')) {
                        document.getElementById('tailor-polling-overlay').style.display = 'none';
                    }
                    if (activeJobId === id) {
                        document.getElementById('tailored-output').innerHTML = `
                            <div style="padding: 1.5rem; border: 1px solid rgba(245,158,11,0.4); border-radius: 10px; background: rgba(245,158,11,0.07); color: #fde68a;">
                                <strong>⏱ Tailoring Timed Out (20 min)</strong>
                                <p style="margin-top: 0.5rem; font-size: 0.9rem;">The LLM did not respond. Click Re-Tailor to try again.</p>
                            </div>`;
                        if (btn) {
                            btn.innerHTML = '<span class="icon">✨</span> Re-Tailor';
                            btn.disabled = false;
                        }
                        document.getElementById('retailor-instructions-container').style.display = 'block';
                        document.getElementById('tab-btn-tailored').click();
                    }
                }
            } catch (e) {
                console.error("Polling error", e);
            }
        }, 3000);
    }

    async function runTailor(id) {
        const btn = document.getElementById('run-tailor-btn');
        btn.innerHTML = '<span class="icon">⏳</span> Requesting...';
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
                const jobIndex = jobsData.findIndex(j => j.id === id);
                if(jobIndex !== -1) jobsData[jobIndex].tailoring_status = 'processing';
                
                // Clear old outputs and switch to tailoring tab to show polling HUD
                document.getElementById('tailored-output').innerHTML = '';
                document.getElementById('tailor-status-area').style.display = 'none';
                document.getElementById('tailor-polling-overlay').style.display = 'block';
                const tb = document.getElementById('tab-badge-tailored');
                if (tb) {
                    tb.style.display = 'inline-flex';
                    tb.className = 'tab-badge processing';
                    tb.textContent = '⏳';
                }
                
                // Auto switch to tailored tab so they can see progress
                document.getElementById('tab-btn-tailored').click();
                
                startPolling(id);
                return;
            }
            
            if(!res.ok) {
                const errText = await res.text();
                throw new Error(errText.trim() || "Tailor failed on backend");
            }
            
            const result = await res.json();
            
            const jobIndex = jobsData.findIndex(j => j.id === id);
            if(jobIndex !== -1) {
                jobsData[jobIndex].tailored_resume = result.TailoredResume;
                jobsData[jobIndex].tailored_report = result.Report;
                jobsData[jobIndex].cover_letter = result.CoverLetter;
                jobsData[jobIndex].fit_brief = result.FitBrief;
                jobsData[jobIndex].market_salary = result.MarketSalary;
                jobsData[jobIndex].score = result.Score;
                jobsData[jobIndex].tailoring_status = 'completed';
            }

            // Polling handles async now, this is just for synchronous success fallback 
            // Re-render and open tailored tab
            renderJobDetail(jobsData[jobIndex], true);

        } catch(e) {
            console.error(e);
            alert("Error running tailor process.\n\nDetails: " + e.message);
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
            body { font-family: 'Helvetica Neue', Helvetica, Arial, sans-serif; padding: 20px; line-height: 1.6; color: #000; background: #fff; font-size: 11pt; }
            h1 { font-size: 1.4em; margin-top: 0; margin-bottom: 0.3em; }
            h2 { font-size: 1.2em; margin-top: 1.8em; margin-bottom: 0.5em; border-bottom: 1px solid #ccc; padding-bottom: 0.2em; }
            h3 { font-size: 1em; margin-top: 1.2em; margin-bottom: 0.3em; }
            p { margin: 0.4em 0; }
            ul { margin: 0.4em 0; padding-left: 1.4em; }
            li { margin-bottom: 0.2em; }
            hr { border: 1px solid #ddd; margin: 1.5em 0; }
            .doc-section { page-break-inside: avoid; }
            .doc-page-break { page-break-after: always; margin: 0; height: 0; border: none; }
            .cover-letter-section { margin-bottom: 2em; }
            @media print { .doc-page-break { page-break-after: always; } }
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
            const totalApplied = reportData.reduce((s, r) => s + (r.applied_count || 0), 0);
            badge.textContent = `${reportData.length} companies · ${totalJobs} jobs · ${totalTailored} tailored · ${totalApplied} applied`;

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
                    <td style="padding: 0.65rem 1.25rem; text-align: right; font-weight: 700; color: ${row.applied_count > 0 ? '#10b981' : '#333'};">${row.applied_count > 0 ? '🏆 ' + row.applied_count : '—'}</td>
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
            const [res, mRes] = await Promise.all([
                fetch('/api/health'),
                fetch('/api/models').catch(() => null)
            ]);
            if (!res.ok) throw new Error('Health endpoint returned ' + res.status);
            const data = await res.json();

            const overallOk = data.overall === 'ok';
            updateHealthDot(data.overall);
            overallEl.textContent = overallOk ? '✅ All Systems Operational' : '⚠️ Degraded — Check components below';
            overallEl.style.color = overallOk ? '#10b981' : '#f59e0b';

            // Build Ollama label from live active model names
            let ollamaLabel = 'Ollama LLM';
            if (mRes && mRes.ok) {
                const mData = await mRes.json();
                if (mData.active_fast && mData.active_deep) {
                    ollamaLabel = `Ollama — ⚡${mData.active_fast} / 🔬${mData.active_deep}`;
                }
            }

            const componentMeta = {
                ollama:  { label: ollamaLabel,              icon: '🧠' },
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

// ═══════════════════════════════════════════════════════════════════════════
// SCORE HISTORY — shows previous tailoring attempts for a job
// ═══════════════════════════════════════════════════════════════════════════
async function loadScoreHistory(jobId) {
    const panel = document.getElementById('score-history-panel');
    if (!panel) return;

    try {
        const resp = await fetch(`/api/jobs/history/${jobId}`, {
            headers: { 'Authorization': 'Bearer ' + localStorage.getItem('tenant_id') }
        });
        if (!resp.ok) return;
        const history = await resp.json();
        if (!history || history.length <= 0) {
            panel.innerHTML = '';
            return;
        }

        // Reverse for chronological order (API returns newest first)
        const chrono = [...history].reverse();

        const rows = history.map((h, i) => {
            const date = new Date(h.created_at).toLocaleDateString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
            const isCurrent = i === 0;
            const border = isCurrent ? 'border-color: rgba(16,185,129,0.4);' : '';
            const badge = isCurrent ? '<span style="font-size:0.65rem; background:rgba(16,185,129,0.15); color:#10b981; padding:0.15rem 0.4rem; border-radius:99px; font-weight:600; margin-left:0.5rem;">CURRENT</span>' : '';
            return `
                <div style="display: grid; grid-template-columns: 60px 1fr auto; gap: 0.75rem; align-items: center; padding: 0.6rem 0.85rem; border-radius: 8px; background: rgba(255,255,255,0.02); border: 1px solid rgba(255,255,255,0.06); ${border}">
                    <div style="text-align: center;">
                        <span style="font-size: 1.25rem; font-weight: 700; color: ${h.score >= 80 ? '#10b981' : h.score >= 60 ? '#f59e0b' : '#ef4444'};">${h.score}%</span>
                    </div>
                    <div style="font-size: 0.78rem; color: #94a3b8;">
                        <span style="color:#60a5fa;">T:${h.sub_score_tech}</span> · 
                        <span style="color:#a78bfa;">D:${h.sub_score_domain}</span> · 
                        <span style="color:#f59e0b;">S:${h.sub_score_senior}</span>
                        ${h.market_salary && h.market_salary !== 'Unknown' ? ` · <span style="color:#c084fc;">${h.market_salary}</span>` : ''}
                        ${badge}
                    </div>
                    <div style="font-size: 0.72rem; color: #64748b; white-space: nowrap;">${date}</div>
                </div>
            `;
        }).join('');

        // Mini sparkline of score trend
        const scores = chrono.map(h => h.score);
        let sparkline = '';
        if (scores.length > 1) {
            const max = Math.max(...scores, 100);
            const min = Math.min(...scores, 0);
            const w = 120, ht = 32;
            const points = scores.map((s, i) => {
                const x = (i / (scores.length - 1)) * w;
                const y = ht - ((s - min) / (max - min)) * ht;
                return `${x},${y}`;
            }).join(' ');
            sparkline = `
                <svg width="${w}" height="${ht}" style="margin-left: auto;">
                    <polyline points="${points}" fill="none" stroke="#10b981" stroke-width="2" stroke-linejoin="round"/>
                    ${scores.map((s, i) => {
                        const x = (i / (scores.length - 1)) * w;
                        const y = ht - ((s - min) / (max - min)) * ht;
                        return `<circle cx="${x}" cy="${y}" r="3" fill="#10b981"/>`;
                    }).join('')}
                </svg>
            `;
        }

        panel.innerHTML = `
            <details style="border: 1px solid rgba(255,255,255,0.06); border-radius: 10px; background: rgba(255,255,255,0.02); overflow: hidden;">
                <summary style="padding: 0.75rem 1rem; cursor: pointer; display: flex; align-items: center; gap: 0.75rem; font-size: 0.85rem; font-weight: 600; color: #94a3b8; user-select: none;">
                    <span>📊 Tailoring History</span>
                    <span style="font-size: 0.75rem; color: #64748b; font-weight: 400;">${history.length} attempt${history.length !== 1 ? 's' : ''}</span>
                    ${sparkline}
                </summary>
                <div style="display: flex; flex-direction: column; gap: 0.4rem; padding: 0.75rem 1rem;">
                    ${rows}
                </div>
            </details>
        `;
    } catch(e) {
        console.error('Score history error:', e);
    }
}

// ═══════════════════════════════════════════════════════════════════════════
// TRENDS ANALYSIS MODAL
// ═══════════════════════════════════════════════════════════════════════════
(function() {
    const trendsModal = document.getElementById('trends-modal');
    const openBtn = document.getElementById('open-trends-btn');
    const closeBtn = document.getElementById('close-trends-btn');

    if (!openBtn || !trendsModal) return;

    // Track chart instances for cleanup
    let chartInstances = {};

    openBtn.addEventListener('click', () => {
        trendsModal.showModal();
        // Always start at the top
        const body = trendsModal.querySelector('.modal-body-scroll');
        if (body) body.scrollTop = 0;
        loadTrendsData();
    });

    closeBtn.addEventListener('click', () => trendsModal.close());
    trendsModal.addEventListener('click', e => { if (e.target === trendsModal) trendsModal.close(); });

    // Color palette for company/source series
    const PALETTE = [
        '#3b82f6', '#8b5cf6', '#10b981', '#f97316', '#ec4899',
        '#06b6d4', '#f59e0b', '#ef4444', '#84cc16', '#6366f1',
        '#14b8a6', '#e879f7', '#fb923c', '#a3e635', '#38bdf8'
    ];

    async function loadTrendsData() {
        try {
            const resp = await fetch('/api/report/trends', {
                headers: { 'Authorization': 'Bearer ' + localStorage.getItem('tenant_id') }
            });
            if (!resp.ok) throw new Error('Failed to fetch trend data');
            const data = await resp.json();
            renderTrends(data);
        } catch(e) {
            console.error('Trends fetch error:', e);
        }
    }

    function renderTrends(data) {
        // Destroy existing chart instances
        Object.values(chartInstances).forEach(c => c.destroy());
        chartInstances = {};

        renderSummaryCards(data);
        renderDailyVolumeChart(data.daily_totals || []);
        renderCompanyTrendChart(data.daily_by_company || []);
        renderSourceChart(data.daily_by_source || []);
        renderCompanyBarChart(data.top_companies || []);
        renderTopPayingChart(data.top_paying_companies || []);
        renderTopPayingJobsTable(data.top_paying_jobs || []);
        renderCategoryCharts(data.categories || []);
    }

    function renderSummaryCards(data) {
        const totals = data.daily_totals || [];
        const companies = data.top_companies || [];
        const totalJobs = totals.reduce((sum, d) => sum + d.total, 0);
        const uniqueDays = totals.length;
        const avgPerDay = uniqueDays > 0 ? Math.round(totalJobs / uniqueDays) : 0;
        const uniqueCompanies = companies.length;
        const tailoredCount = companies.reduce((sum, c) => sum + c.tailored_count, 0);

        document.getElementById('trend-summary-row').innerHTML = [
            { label: 'Total Jobs Tracked', val: totalJobs.toLocaleString(), color: '#3b82f6', icon: '📋' },
            { label: 'Avg Jobs / Day', val: avgPerDay, color: '#10b981', icon: '📊' },
            { label: 'Unique Companies', val: uniqueCompanies, color: '#a78bfa', icon: '🏢' },
            { label: 'Resumes Tailored', val: tailoredCount, color: '#f97316', icon: '✨' },
        ].map(s => `
            <div style="background: rgba(15,23,42,0.7); border: 1px solid rgba(255,255,255,0.08); border-radius: 12px; padding: 1rem 1.25rem; text-align: center;">
                <div style="font-size: 1.5rem; margin-bottom: 0.25rem;">${s.icon}</div>
                <div style="font-size: 1.75rem; font-weight: 700; color: ${s.color};">${s.val}</div>
                <div style="font-size: 0.75rem; color: #94a3b8; text-transform: uppercase; letter-spacing: 0.5px; margin-top: 0.25rem;">${s.label}</div>
            </div>
        `).join('');
    }

    function chartDefaults() {
        return {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                legend: { labels: { color: '#94a3b8', font: { size: 11 } } }
            },
            scales: {
                x: { ticks: { color: '#64748b', font: { size: 10 } }, grid: { color: 'rgba(255,255,255,0.04)' } },
                y: { ticks: { color: '#64748b', font: { size: 10 } }, grid: { color: 'rgba(255,255,255,0.04)' }, beginAtZero: true }
            }
        };
    }

    function renderDailyVolumeChart(dailyTotals) {
        const ctx = document.getElementById('chart-daily-volume').getContext('2d');
        const labels = dailyTotals.map(d => d.date);
        const values = dailyTotals.map(d => d.total);

        chartInstances['dailyVolume'] = new Chart(ctx, {
            type: 'line',
            data: {
                labels,
                datasets: [{
                    label: 'Jobs Discovered',
                    data: values,
                    borderColor: '#3b82f6',
                    backgroundColor: 'rgba(59,130,246,0.1)',
                    borderWidth: 2,
                    fill: true,
                    tension: 0.4,
                    pointRadius: 3,
                    pointBackgroundColor: '#3b82f6',
                    pointHoverRadius: 6,
                }]
            },
            options: {
                ...chartDefaults(),
                plugins: {
                    ...chartDefaults().plugins,
                    legend: { display: false },
                    tooltip: {
                        backgroundColor: 'rgba(15,23,42,0.95)',
                        titleColor: '#f8fafc',
                        bodyColor: '#94a3b8',
                        borderColor: 'rgba(255,255,255,0.1)',
                        borderWidth: 1,
                    }
                }
            }
        });
    }

    function renderCompanyTrendChart(dailyByCompany) {
        const ctx = document.getElementById('chart-company-trend').getContext('2d');

        // Group by company — build { company: { date: count } }
        const companyMap = {};
        const allDates = new Set();
        dailyByCompany.forEach(d => {
            allDates.add(d.date);
            if (!companyMap[d.company]) companyMap[d.company] = {};
            companyMap[d.company][d.date] = d.total;
        });

        const sortedDates = [...allDates].sort();
        const companies = Object.keys(companyMap);

        const datasets = companies.map((company, i) => ({
            label: company,
            data: sortedDates.map(date => companyMap[company][date] || 0),
            backgroundColor: PALETTE[i % PALETTE.length] + '99',
            borderColor: PALETTE[i % PALETTE.length],
            borderWidth: 1,
        }));

        chartInstances['companyTrend'] = new Chart(ctx, {
            type: 'bar',
            data: { labels: sortedDates, datasets },
            options: {
                ...chartDefaults(),
                plugins: {
                    ...chartDefaults().plugins,
                    legend: { position: 'bottom', labels: { color: '#94a3b8', font: { size: 10 }, boxWidth: 12, padding: 12 } },
                    tooltip: { backgroundColor: 'rgba(15,23,42,0.95)', titleColor: '#f8fafc', bodyColor: '#94a3b8' }
                },
                scales: {
                    ...chartDefaults().scales,
                    x: { ...chartDefaults().scales.x, stacked: true },
                    y: { ...chartDefaults().scales.y, stacked: true },
                },
            }
        });
    }

    function renderSourceChart(dailyBySource) {
        const ctx = document.getElementById('chart-source-breakdown').getContext('2d');

        // Aggregate sources across all days
        const sourceMap = {};
        dailyBySource.forEach(d => {
            sourceMap[d.source] = (sourceMap[d.source] || 0) + d.total;
        });

        const sources = Object.keys(sourceMap);
        const values = sources.map(s => sourceMap[s]);

        chartInstances['source'] = new Chart(ctx, {
            type: 'doughnut',
            data: {
                labels: sources,
                datasets: [{
                    data: values,
                    backgroundColor: sources.map((_, i) => PALETTE[i % PALETTE.length] + 'cc'),
                    borderColor: 'rgba(15,23,42,0.9)',
                    borderWidth: 2,
                }]
            },
            options: {
                responsive: true,
                maintainAspectRatio: false,
                plugins: {
                    legend: { position: 'bottom', labels: { color: '#94a3b8', font: { size: 11 }, boxWidth: 12, padding: 10 } },
                    tooltip: { backgroundColor: 'rgba(15,23,42,0.95)', titleColor: '#f8fafc', bodyColor: '#94a3b8' },
                },
            }
        });
    }

    function renderCompanyBarChart(topCompanies) {
        const ctx = document.getElementById('chart-company-bar').getContext('2d');

        const top = topCompanies.slice(0, 12);
        const labels = top.map(c => c.company || 'Unknown');
        const jobCounts = top.map(c => c.total_jobs);
        const tailoredCounts = top.map(c => c.tailored_count);

        chartInstances['companyBar'] = new Chart(ctx, {
            type: 'bar',
            data: {
                labels,
                datasets: [
                    {
                        label: 'Total Jobs',
                        data: jobCounts,
                        backgroundColor: 'rgba(251,191,36,0.6)',
                        borderColor: '#fbbf24',
                        borderWidth: 1,
                    },
                    {
                        label: 'Tailored',
                        data: tailoredCounts,
                        backgroundColor: 'rgba(16,185,129,0.6)',
                        borderColor: '#10b981',
                        borderWidth: 1,
                    }
                ]
            },
            options: {
                ...chartDefaults(),
                indexAxis: 'y',
                plugins: {
                    ...chartDefaults().plugins,
                    legend: { position: 'bottom', labels: { color: '#94a3b8', font: { size: 10 }, boxWidth: 12 } },
                    tooltip: { backgroundColor: 'rgba(15,23,42,0.95)', titleColor: '#f8fafc', bodyColor: '#94a3b8' },
                },
                scales: {
                    x: { ...chartDefaults().scales.x, beginAtZero: true },
                    y: { ...chartDefaults().scales.y, ticks: { color: '#94a3b8', font: { size: 10 } } },
                },
            }
        });
    }

    function renderTopPayingChart(topPaying) {
        const ctx = document.getElementById('chart-top-paying').getContext('2d');
        const tableEl = document.getElementById('top-paying-table');

        if (!topPaying || topPaying.length === 0) {
            if (tableEl) tableEl.innerHTML = '<p style="color:#64748b;font-size:0.82rem;padding:1rem 0;">No salary data available yet. Salary is populated from scraped compensation fields and LLM market salary estimates on tailored jobs.</p>';
            return;
        }

        const labels = topPaying.map(c => c.company);
        const values = topPaying.map(c => c.max_salary);

        // Color-code bars: top tier (>= 400k) gold, mid (>= 250k) green, rest blue
        const barColors = values.map(v =>
            v >= 400 ? 'rgba(251,191,36,0.75)' :
            v >= 250 ? 'rgba(16,185,129,0.75)' :
                       'rgba(99,102,241,0.75)'
        );
        const borderColors = values.map(v =>
            v >= 400 ? '#fbbf24' :
            v >= 250 ? '#10b981' :
                       '#6366f1'
        );

        chartInstances['topPaying'] = new Chart(ctx, {
            type: 'bar',
            data: {
                labels,
                datasets: [{
                    label: 'Max Salary (thousands)',
                    data: values,
                    backgroundColor: barColors,
                    borderColor: borderColors,
                    borderWidth: 1,
                }]
            },
            options: {
                ...chartDefaults(),
                indexAxis: 'y',
                plugins: {
                    legend: { display: false },
                    tooltip: {
                        backgroundColor: 'rgba(15,23,42,0.95)',
                        titleColor: '#f8fafc',
                        bodyColor: '#94a3b8',
                        callbacks: {
                            label: ctx => ` $${ctx.raw}k / yr`
                        }
                    }
                },
                scales: {
                    x: {
                        ...chartDefaults().scales.x,
                        beginAtZero: true,
                        ticks: {
                            color: '#64748b',
                            font: { size: 10 },
                            callback: v => `$${v}k`
                        }
                    },
                    y: { ...chartDefaults().scales.y, ticks: { color: '#94a3b8', font: { size: 10 } } },
                },
            }
        });

        // Companion table
        if (tableEl) {
            tableEl.innerHTML = `
                <table style="width:100%; border-collapse: collapse;">
                    <thead>
                        <tr style="border-bottom: 1px solid rgba(255,255,255,0.08);">
                            <th style="text-align:left; padding: 0.4rem 0.5rem; color:#64748b; font-size:0.75rem; font-weight:600; text-transform:uppercase; letter-spacing:0.5px;">#</th>
                            <th style="text-align:left; padding: 0.4rem 0.5rem; color:#64748b; font-size:0.75rem; font-weight:600; text-transform:uppercase; letter-spacing:0.5px;">Company</th>
                            <th style="text-align:right; padding: 0.4rem 0.5rem; color:#64748b; font-size:0.75rem; font-weight:600; text-transform:uppercase; letter-spacing:0.5px;">Max Salary</th>
                            <th style="text-align:right; padding: 0.4rem 0.5rem; color:#64748b; font-size:0.75rem; font-weight:600; text-transform:uppercase; letter-spacing:0.5px;">Jobs</th>
                        </tr>
                    </thead>
                    <tbody>
                        ${topPaying.map((c, i) => `
                            <tr style="border-bottom: 1px solid rgba(255,255,255,0.04); transition: background 0.15s;" onmouseover="this.style.background='rgba(255,255,255,0.04)'" onmouseout="this.style.background=''">
                                <td style="padding: 0.45rem 0.5rem; color:#64748b; font-size:0.78rem;">${i + 1}</td>
                                <td style="padding: 0.45rem 0.5rem; font-weight:600; color:#f1f5f9; font-size:0.82rem;">${c.company}</td>
                                <td style="padding: 0.45rem 0.5rem; text-align:right; font-weight:700; color:${
                                    c.max_salary >= 400 ? '#fbbf24' :
                                    c.max_salary >= 250 ? '#10b981' : '#6366f1'
                                }; font-size:0.85rem;">$${c.max_salary}k</td>
                                <td style="padding: 0.45rem 0.5rem; text-align:right; color:#64748b; font-size:0.78rem;">${c.job_count}</td>
                            </tr>
                        `).join('')}
                    </tbody>
                </table>
            `;
        }
    }

    function renderTopPayingJobsTable(jobs) {
        const el = document.getElementById('top-paying-jobs-table');
        if (!el) return;
        if (!jobs || jobs.length === 0) {
            el.innerHTML = '<p style="color:#64748b;font-size:0.82rem;padding:0.5rem 0;">No individual salary data yet. Salary comes from scraped compensation fields and LLM market salary estimates on tailored jobs.</p>';
            return;
        }
        el.innerHTML = `
            <table style="width:100%; border-collapse: collapse;">
                <thead>
                    <tr style="border-bottom: 1px solid rgba(255,255,255,0.08);">
                        <th style="text-align:left; padding:0.4rem 0.5rem; color:#64748b; font-size:0.72rem; font-weight:600; text-transform:uppercase; letter-spacing:0.5px;">#</th>
                        <th style="text-align:left; padding:0.4rem 0.5rem; color:#64748b; font-size:0.72rem; font-weight:600; text-transform:uppercase; letter-spacing:0.5px;">Title</th>
                        <th style="text-align:left; padding:0.4rem 0.5rem; color:#64748b; font-size:0.72rem; font-weight:600; text-transform:uppercase; letter-spacing:0.5px;">Company</th>
                        <th style="text-align:right; padding:0.4rem 0.5rem; color:#64748b; font-size:0.72rem; font-weight:600; text-transform:uppercase; letter-spacing:0.5px;">Salary</th>
                        <th style="text-align:center; padding:0.4rem 0.5rem; color:#64748b; font-size:0.72rem; font-weight:600; text-transform:uppercase; letter-spacing:0.5px;">Source</th>
                    </tr>
                </thead>
                <tbody>
                    ${jobs.map((j, i) => `
                        <tr style="border-bottom: 1px solid rgba(255,255,255,0.04);" onmouseover="this.style.background='rgba(255,255,255,0.04)'" onmouseout="this.style.background=''">
                            <td style="padding:0.4rem 0.5rem; color:#64748b; font-size:0.75rem;">${i + 1}</td>
                            <td style="padding:0.4rem 0.5rem; color:#f1f5f9; font-size:0.8rem; font-weight:500; max-width:220px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap;" title="${j.title}">${j.title}</td>
                            <td style="padding:0.4rem 0.5rem; color:#94a3b8; font-size:0.78rem;">${j.company}</td>
                            <td style="padding:0.4rem 0.5rem; text-align:right; font-weight:700; font-size:0.85rem; color:${j.max_salary >= 400 ? '#fbbf24' : j.max_salary >= 250 ? '#34d399' : '#818cf8'};">$${j.max_salary}k</td>
                            <td style="padding:0.4rem 0.5rem; text-align:center;">
                                <span style="font-size:0.65rem; padding:0.1rem 0.4rem; border-radius:99px; font-weight:600; background:${j.source === 'market_salary' ? 'rgba(99,102,241,0.2)' : 'rgba(16,185,129,0.15)'}; color:${j.source === 'market_salary' ? '#818cf8' : '#34d399'};">
                                    ${j.source === 'market_salary' ? 'AI' : 'scraped'}
                                </span>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
    }

    function renderCategoryCharts(categories) {
        if (!categories || categories.length === 0) return;

        const labels = categories.map(c => c.category);
        const counts  = categories.map(c => c.count);
        const salaries = categories.map(c => c.avg_salary);

        // Volume chart
        const ctxVol = document.getElementById('chart-category-volume');
        if (ctxVol) {
            chartInstances['catVolume'] = new Chart(ctxVol.getContext('2d'), {
                type: 'bar',
                data: {
                    labels,
                    datasets: [{
                        label: 'Job Count',
                        data: counts,
                        backgroundColor: labels.map((_, i) => PALETTE[i % PALETTE.length] + 'aa'),
                        borderColor:     labels.map((_, i) => PALETTE[i % PALETTE.length]),
                        borderWidth: 1,
                    }]
                },
                options: {
                    ...chartDefaults(),
                    indexAxis: 'y',
                    plugins: {
                        legend: { display: false },
                        tooltip: { backgroundColor:'rgba(15,23,42,0.95)', titleColor:'#f8fafc', bodyColor:'#94a3b8' },
                    },
                    scales: {
                        x: { ...chartDefaults().scales.x, beginAtZero: true },
                        y: { ticks: { color: '#94a3b8', font: { size: 10 } }, grid: { color: 'rgba(255,255,255,0.04)' } },
                    }
                }
            });
        }

        // Avg salary chart (only categories with salary data)
        const salaryData = categories.filter(c => c.avg_salary > 0);
        const ctxSal = document.getElementById('chart-category-salary');
        if (ctxSal) {
            if (salaryData.length === 0) {
                ctxSal.parentElement.innerHTML = '<p style="color:#64748b;font-size:0.79rem;padding:2rem 0;text-align:center;">Avg salary data not yet available — tailor more jobs to populate.</p>';
            } else {
                chartInstances['catSalary'] = new Chart(ctxSal.getContext('2d'), {
                    type: 'bar',
                    data: {
                        labels: salaryData.map(c => c.category),
                        datasets: [{
                            label: 'Avg Salary ($k)',
                            data: salaryData.map(c => c.avg_salary),
                            backgroundColor: 'rgba(251,191,36,0.6)',
                            borderColor: '#fbbf24',
                            borderWidth: 1,
                        }]
                    },
                    options: {
                        ...chartDefaults(),
                        indexAxis: 'y',
                        plugins: {
                            legend: { display: false },
                            tooltip: {
                                backgroundColor:'rgba(15,23,42,0.95)', titleColor:'#f8fafc', bodyColor:'#94a3b8',
                                callbacks: { label: ctx => ` $${ctx.raw}k avg` }
                            },
                        },
                        scales: {
                            x: { ...chartDefaults().scales.x, beginAtZero: true, ticks: { color:'#64748b', font:{size:10}, callback: v => `$${v}k` } },
                            y: { ticks: { color: '#94a3b8', font: { size: 10 } }, grid: { color: 'rgba(255,255,255,0.04)' } },
                        }
                    }
                });
            }
        }
    }
})();
