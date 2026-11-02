/**
 * Alpine controller for the embedded web UI, built from the zwfm-encoder
 * frontend: requestJSON, the storage and secret helpers, keyboard handling,
 * test states and toasts are copied from its app.js. REST handles
 * configuration changes; polling /api/status supplies live data.
 */

const MS_PER_SECOND = 1000;
const MS_PER_MINUTE = 60 * MS_PER_SECOND;
const MS_PER_DAY = 24 * 60 * MS_PER_MINUTE;
const POLL_MS = 1000;
const STALE_MS = 3000; // no answer for this long shows the connection overlay
const TEST_FEEDBACK_MS = 2000;
const TOAST_DURATION_SUCCESS = 3000;
const TOAST_DURATION_ERROR = 5000;
const MAX_TOASTS = 3;

const STORAGE_KEYS = {
    SETTINGS_CARDS: 'settingsCardsOpen',
};

const RELATIVE_TIME_FORMATTER = new Intl.RelativeTimeFormat('en', { numeric: 'always', style: 'narrow' });

const STATE_LABELS = { disabled: 'Disabled', idle: 'Idle', low: 'Alert Low', high: 'Alert High', alarm: 'Alert' };
const ACTIVE = ['low', 'high', 'alarm'];

// Secrets the server never sends back: their name in the API, then channel and field.
const SECRETS = {
    webhook_url: ['webhook', 'url'],
    client_secret: ['email', 'client_secret'],
    heartbeat_url: ['heartbeat', 'url'],
};

const ALARM_DESCRIPTIONS = {
    forward_power: 'Alert when the forward power stays below or above a limit.',
    reflected_power: 'Alert when the reflected power stays above a limit.',
    temperature: 'Alert when the temperature of the TEX stays below or above a limit.',
    modulation: 'Alert when the total modulation stays below or above a limit.',
    pll_lock_loss: 'Alert when the PLL of the TEX loses lock for 3 s.',
    audio_loss: 'Alert when the TEX reports audio loss for 3 s.',
    swr: 'Alert when the TEX reports an SWR alarm for 3 s.',
    foldback: 'Alert when the TEX folds back its power for 3 s.',
    panel_access: 'Alert when someone uses the front panel of the TEX. There is no recovery message.',
    link_loss: 'Alert when the TEX does not answer for 3 s. Unlike the other alarms, this one also works in the first 15 s after the link comes up.',
};

/**
 * Performs an API request with an optional JSON body. Returns the parsed
 * response body, or null when there is none (e.g. 204 No Content). Throws
 * an Error with the server-supplied message on HTTP errors; the status is
 * exposed on err.status and validation error lists on err.errors. Mutations
 * must be same-origin and need the page's CSRF token. A signal can abort it.
 */
const requestJSON = async (url, { method = 'GET', body, signal } = {}) => {
    method = method.toUpperCase();
    const options = { method, signal };
    if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
        if (new URL(url, window.location.href).origin !== window.location.origin) {
            throw new Error('Admin requests must be same-origin');
        }
        const token = document.querySelector('meta[name="csrf-token"]')?.content;
        if (!token) throw new Error('Reload this page before making changes');
        options.headers = { 'X-CSRF-Token': token };
    }
    if (body !== undefined) {
        options.headers = { ...options.headers, 'Content-Type': 'application/json' };
        options.body = JSON.stringify(body);
    }
    const response = await fetch(url, options);
    // No body (204) or no JSON reads as null, but a timeout must stay an error.
    const result = await response.json().catch((err) => {
        if (signal?.aborted) throw err;
        return null;
    });
    if (!response.ok) {
        const error = new Error(result?.error || `HTTP ${response.status}`);
        error.status = response.status;
        error.errors = result?.errors;
        throw error;
    }
    return result;
};

const readStorageObject = (key, fallback) => {
    try {
        const value = localStorage.getItem(key);
        if (!value) return fallback;
        const parsed = JSON.parse(value);
        return parsed && typeof parsed === 'object' ? { ...fallback, ...parsed } : fallback;
    } catch {
        return fallback;
    }
};

const writeStorage = (key, value) => {
    try {
        localStorage.setItem(key, value);
    } catch {
        // Storage is a non-critical UI preference.
    }
};

/**
 * Stages an undoable secret removal. The template disables the input while
 * cleared to prevent a value/clear-flag conflict at the backend.
 */
const clearSecretField = (form, field, flag, message) => {
    if (form[flag] || !confirm(message)) return false;
    form[`_preClear_${field}`] = form[field];
    form[field] = '';
    form[flag] = true;
    return true;
};

/** Cancels a pending secret clear and restores the prior input. */
const undoClearSecretField = (form, field, flag) => {
    if (!form[flag]) return false;
    form[field] = form[`_preClear_${field}`] ?? '';
    form[flag] = false;
    form[`_preClear_${field}`] = null;
    return true;
};

// The status request in flight. It lives outside Alpine, whose proxies break promises.
let statusRequest = null;

// Alpine wraps state in proxies that structuredClone refuses; the data is JSON.
const clone = (v) => JSON.parse(JSON.stringify(v));

document.addEventListener('alpine:init', () => {
    Alpine.data('txmonApp', () => ({
        icons: window.icons,
        STATE_LABELS,
        view: 'dashboard',
        settingsTab: 'alarms',
        settingsTabs: [
            { id: 'alarms', label: 'Alarms', icon: 'bell' },
            { id: 'notifications', label: 'Notifications', icon: 'email' },
            { id: 'events', label: 'Events', icon: 'list' },
            { id: 'about', label: 'About', icon: 'info' },
        ],

        status: { link: false, alarms: [], telemetry: null },
        now: Date.now(),
        lastOk: 0,
        _eventsPolling: false,

        server: null, // settings as last loaded
        defs: [],
        version: {},

        events: [],
        eventsLoading: false,
        eventsError: '',

        form: null,
        secrets: {},
        settingsDirty: false,
        saving: false,
        cardsOpen: readStorageObject(STORAGE_KEYS.SETTINGS_CARDS, {}),
        testStates: {
            webhook: { pending: false, text: 'Test' },
            email: { pending: false, text: 'Test' },
            zabbix: { pending: false, text: 'Test' },
        },
        _testResetTimeouts: {},

        control: { power: '', freq: '', busy: false },

        banner: { visible: false, message: '', type: 'info' },
        toasts: [],
        _toastIdCounter: 0,

        get tx() {
            return this.status.telemetry;
        },

        get connected() {
            return this.now - this.lastOk < STALE_MS;
        },

        init() {
            this.loadSettings();
            this.tick();
            setInterval(() => this.tick(), POLL_MS);
            document.addEventListener('keydown', (e) => this.handleGlobalKeydown(e));
        },

        // One clock drives the polling and the age of the readings, so a
        // hanging request makes the data go stale instead of looking live.
        tick() {
            this.now = Date.now();
            if (!statusRequest) this.refresh();
        },

        // One status request at a time, so an old answer cannot overwrite a
        // newer one. A caller after a change waits for the request in flight,
        // which may predate the change, and then asks again.
        async refresh() {
            while (statusRequest) await statusRequest;
            statusRequest = this.fetchStatus();
            try {
                await statusRequest;
            } finally {
                statusRequest = null;
            }
        },

        async fetchStatus() {
            try {
                const status = await requestJSON('/api/status', { signal: AbortSignal.timeout(STALE_MS) });
                // Clear stale toasts from before a disconnect, as the encoder does.
                if (!this.connected) this.clearToasts();
                this.status = status;
                this.lastOk = Date.now();
                if (this.view === 'settings' && this.settingsTab === 'events') this.loadEvents(false);
            } catch (err) {
                if (err.status === 401) window.location.assign('/login');
            }
        },

        async loadSettings() {
            try {
                this.server = await requestJSON('/api/settings');
                this.defs = this.server.defs;
                this.version = this.server.version || {};
                this.banner.visible = false;
                return true;
            } catch (err) {
                console.error('Failed to load settings:', err);
                this.banner = { visible: true, message: 'Failed to load configuration', type: 'danger' };
                return false;
            }
        },

        // --- Dashboard ---

        fmt(v, digits) {
            return Number(v).toFixed(digits);
        },

        ago(iso) {
            const s = Math.max(0, Math.round((this.now - Date.parse(iso)) / MS_PER_SECOND));
            return s < 2 ? 'just now' : `${s} s ago`;
        },

        onAirLabel() {
            if (!this.status.link) return 'No link';
            return this.tx?.on_air ? 'On air' : 'RF off';
        },

        // Link loss outranks the alarms it causes.
        alertMessage() {
            if (!this.status.link) return `No link to the transmitter${this.status.error ? `: ${this.status.error}` : ''}`;
            const active = this.activeAlarms();
            if (active.length === 0) return '';
            return `Alarm: ${active.map(a => `${this.defLabel(a.key)} (${STATE_LABELS[a.state]})`).join(', ')}`;
        },

        alarmState(key) {
            return this.status.alarms?.find(a => a.key === key) || { key, state: 'disabled', count: 0 };
        },

        enabledAlarms() {
            return (this.status.alarms || []).filter(a => a.state !== 'disabled');
        },

        activeAlarms() {
            return (this.status.alarms || []).filter(a => ACTIVE.includes(a.state));
        },

        stateClass(state) {
            if (ACTIVE.includes(state)) return 'state-danger';
            return state === 'idle' ? 'state-success' : 'state-stopped';
        },

        defLabel(key) {
            return this.defs.find(d => d.key === key)?.label || key;
        },

        measurements() {
            const t = this.tx;
            if (!t) return [];
            return [
                { label: 'PA voltage', value: `${this.fmt(t.pa_voltage_v, 1)} V` },
                { label: 'PA current', value: `${this.fmt(t.pa_current_a, 1)} A` },
                { label: 'Efficiency', value: `${t.efficiency_pct} %` },
                { label: 'Modulation', value: `${t.modulation_khz} kHz` },
                { label: 'Deviation left', value: `${t.deviation_left_khz} kHz` },
                { label: 'Deviation right', value: `${t.deviation_right_khz} kHz` },
            ];
        },

        // Status bits of the TEX, worded by their current state.
        flags() {
            const t = this.tx;
            if (!t) return [];
            const ok = 'state-success';
            const bad = 'state-danger';
            return [
                t.pll_locked ? { label: 'PLL locked', cls: ok } : { label: 'PLL unlocked', cls: bad },
                t.power_set_ok ? { label: 'Power set OK', cls: ok } : { label: 'Power not set', cls: bad },
                t.foldback ? { label: 'Foldback', cls: bad } : { label: 'No foldback', cls: ok },
                t.interlock ? { label: 'Interlock', cls: bad } : { label: 'No interlock', cls: ok },
                t.audio_loss ? { label: 'Audio loss', cls: bad } : { label: 'Audio present', cls: ok },
                t.swr_alarm ? { label: 'SWR alarm', cls: bad } : { label: 'SWR OK', cls: ok },
                t.panel_access ? { label: 'Front panel in use', cls: 'state-warning' } : { label: 'Front panel idle', cls: 'state-stopped' },
            ];
        },

        canControl() {
            return this.connected && this.status.link && this.tx && !this.control.busy;
        },

        async runControl(path, body, done) {
            this.control.busy = true;
            try {
                await requestJSON(path, { method: 'POST', body });
                this.showToast(done, 'success');
                await this.refresh();
            } catch (err) {
                this.showToast(err.message, 'error');
            } finally {
                this.control.busy = false;
            }
        },

        setRF(on) {
            if (!this.canControl() || this.tx.on_air === on) return;
            if (!on && !confirm('Switch the RF output off?')) return;
            this.runControl('/api/rf', { on }, on ? 'RF output switched on' : 'RF output switched off');
        },

        setPower() {
            if (this.control.power === '' || !this.canControl()) return;
            const percent = Number(this.control.power);
            this.control.power = '';
            this.runControl('/api/power', { percent }, `Target power set to ${percent} %`);
        },

        setFrequency() {
            const mhz = String(this.control.freq).trim();
            if (!mhz || !this.canControl()) return;
            if (!confirm(`Change the frequency to ${mhz} MHz?`)) return;
            this.control.freq = '';
            this.runControl('/api/frequency', { mhz }, `Frequency set to ${mhz} MHz`);
        },

        // --- Navigation ---

        showDashboard() {
            this.clearToasts();
            this.view = 'dashboard';
            this.settingsDirty = false;
        },

        /**
         * Navigates to settings view and creates form copy from fresh config.
         * Always fetches fresh data from server before opening.
         */
        async showSettings() {
            this.clearToasts();
            if (!(await this.loadSettings())) return;
            const s = clone(this.server);
            for (const def of this.defs) s.alarms[def.key] ??= {};
            for (const ch of ['webhook', 'email', 'zabbix', 'heartbeat']) s.notifications[ch] ??= {};
            for (const ch of ['webhook', 'email', 'zabbix']) {
                const n = s.notifications[ch];
                n.events ??= {};
                n.events.alarms ??= [];
                n.events.recoveries ??= false;
            }
            for (const [ch, field] of Object.values(SECRETS)) s.notifications[ch][`clear_${field}`] = false;
            this.form = { alarms: s.alarms, notifications: s.notifications };
            this.secrets = s.secrets || {};
            this.settingsDirty = false;
            this.view = 'settings';
        },

        cancelSettings() {
            this.showDashboard();
        },

        showTab(tabId) {
            this.settingsTab = tabId;
        },

        /**
         * Navigates to adjacent tab in settings and focuses the tab button.
         * @param {number} direction - 1 for next, -1 for previous
         */
        navigateTab(direction) {
            const currentIndex = this.settingsTabs.findIndex(t => t.id === this.settingsTab);
            const newIndex = (currentIndex + direction + this.settingsTabs.length) % this.settingsTabs.length;
            this.focusTab(this.settingsTabs[newIndex].id);
        },

        // Shows a tab and focuses its button for continued keyboard navigation.
        focusTab(tabId) {
            this.showTab(tabId);
            this.$nextTick(() => {
                document.getElementById(`tab-${tabId}`)?.focus();
            });
        },

        /** Global keyboard: Escape closes settings, Enter saves, arrows navigate tabs. */
        handleGlobalKeydown(event) {
            const isInput = ['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target.tagName);

            // Escape: close secondary views.
            if (event.key === 'Escape') {
                if (this.view === 'settings') {
                    this.cancelSettings();
                    event.preventDefault();
                }
                return;
            }

            // Enter: Save settings (works from input fields, but not textarea/select)
            if (event.key === 'Enter' && this.view === 'settings' && this.settingsDirty) {
                const isTextareaOrSelect = ['TEXTAREA', 'SELECT'].includes(event.target.tagName);
                if (!isTextareaOrSelect) {
                    this.saveSettings();
                    event.preventDefault();
                    return;
                }
            }

            // Arrow keys: Navigate tabs in settings view
            if (this.view === 'settings' && !isInput) {
                if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
                    this.navigateTab(event.key === 'ArrowRight' ? 1 : -1);
                    event.preventDefault();
                } else if (event.key === 'Home') {
                    this.focusTab(this.settingsTabs[0].id);
                    event.preventDefault();
                } else if (event.key === 'End') {
                    this.focusTab(this.settingsTabs[this.settingsTabs.length - 1].id);
                    event.preventDefault();
                }
            }
        },

        // --- Settings ---

        markSettingsDirty() {
            this.settingsDirty = true;
        },

        // Toggles a collapsible settings card open/closed and persists the layout.
        toggleCard(id) {
            this.cardsOpen[id] = !this.cardsOpen[id];
            writeStorage(STORAGE_KEYS.SETTINGS_CARDS, JSON.stringify(this.cardsOpen));
        },

        alarmIcon(def) {
            if (def.key === 'link_loss') return 'link';
            if (def.unit === 'W') return 'power';
            return def.analog ? 'metering' : 'warning';
        },

        alarmDescription(def) {
            return ALARM_DESCRIPTIONS[def.key] || '';
        },

        // Analog alarms watch a low limit, a high limit or both; the others are on or off.
        alarmModes(def) {
            if (!def.analog) return [{ id: 'off', label: 'Off' }, { id: 'on', label: 'On' }];
            const modes = [{ id: 'off', label: 'Off' }, { id: 'low', label: 'Low' }, { id: 'high', label: 'High' }, { id: 'both', label: 'Both' }];
            return def.high_only ? modes.filter(m => m.id === 'off' || m.id === 'high') : modes;
        },

        alarmMode(def) {
            const s = this.form.alarms[def.key];
            if (!def.analog) return s.enabled ? 'on' : 'off';
            if (s.low && s.high) return 'both';
            if (s.low) return 'low';
            return s.high ? 'high' : 'off';
        },

        setAlarmMode(def, mode) {
            const s = this.form.alarms[def.key];
            if (def.analog) {
                s.low = mode === 'low' || mode === 'both';
                s.high = mode === 'high' || mode === 'both';
            } else {
                s.enabled = mode === 'on';
            }
            this.markSettingsDirty();
        },

        // Use the same { summary, pill } shape; only disabled alarms and live alerts get a pill.
        alarmCardStatus(def) {
            const s = this.form.alarms[def.key];
            if (this.alarmMode(def) === 'off') return { summary: 'Not monitored', pill: { cls: 'state-stopped', label: 'Off' } };
            const limits = [];
            if (def.analog && s.low) limits.push(`below ${s.low_limit} ${def.unit}`);
            if (def.analog && s.high) limits.push(`above ${s.high_limit} ${def.unit}`);
            const text = limits.join(', ');
            const summary = def.analog ? text.charAt(0).toUpperCase() + text.slice(1) : 'Monitored';
            const live = this.alarmState(def.key).state;
            return { summary, pill: ACTIVE.includes(live) ? { cls: 'state-danger', label: STATE_LABELS[live] } : null };
        },

        firedText(key) {
            const n = this.alarmState(key).count;
            if (n === 0) return 'Not fired.';
            return `Fired ${n === 1 ? 'once' : `${n} times`}.`;
        },

        async resetAlarm(def) {
            try {
                await requestJSON(`/api/alarms/${def.key}/reset`, { method: 'POST' });
                this.showToast(`${def.label} counter cleared`, 'success');
                await this.refresh();
            } catch (err) {
                this.showToast(err.message, 'error');
            }
        },

        isClearing(name) {
            const [ch, field] = SECRETS[name];
            return this.form.notifications[ch][`clear_${field}`];
        },

        clearSecret(name, message) {
            const [ch, field] = SECRETS[name];
            if (clearSecretField(this.form.notifications[ch], field, `clear_${field}`, message)) this.markSettingsDirty();
        },

        undoClearSecret(name) {
            const [ch, field] = SECRETS[name];
            if (undoClearSecretField(this.form.notifications[ch], field, `clear_${field}`)) this.markSettingsDirty();
        },

        secretPlaceholder(name, fallback) {
            if (this.isClearing(name)) return '(will be cleared on save)';
            return this.secrets[name] ? '••••••••' : fallback;
        },

        // Return { summary, pill }; only off/warning states get a { cls, label } pill.
        notifChannelStatus(name) {
            const n = this.form.notifications;
            const off = { cls: 'state-stopped', label: 'Off' };
            const hasSecret = (secret) => {
                const [ch, field] = SECRETS[secret];
                return (this.secrets[secret] || !!n[ch][field]) && !this.isClearing(secret);
            };
            const events = (ch) => {
                const k = n[ch].events.alarms.length + (n[ch].events.recoveries ? 1 : 0);
                return `${k} event${k === 1 ? '' : 's'}`;
            };
            if (name === 'webhook') {
                if (!hasSecret('webhook_url')) return { summary: 'No URL set', pill: off };
                return { summary: events('webhook'), pill: null };
            }
            if (name === 'email') {
                const e = n.email;
                if (!hasSecret('client_secret') || !e.tenant_id || !e.client_id || !e.from_address || !e.recipients) {
                    return { summary: 'Not configured', pill: off };
                }
                const k = e.recipients.split(',').map(s => s.trim()).filter(Boolean).length;
                return { summary: `${k} recipient${k === 1 ? '' : 's'}`, pill: null };
            }
            if (name === 'zabbix') {
                if (!n.zabbix.server) return { summary: 'No server set', pill: off };
                return { summary: n.zabbix.server, pill: null };
            }
            if (!hasSecret('heartbeat_url')) return { summary: 'No URL set', pill: off };
            return { summary: 'Every minute', pill: null };
        },

        // The form as the API takes it: clear flags become a list, and empty
        // number fields, which the server cannot read as numbers, are left out.
        settingsPayload() {
            const f = clone(this.form);
            const clear = [];
            for (const [name, [ch, field]] of Object.entries(SECRETS)) {
                const n = f.notifications[ch];
                if (n[`clear_${field}`]) clear.push(name);
                delete n[`clear_${field}`];
                delete n[`_preClear_${field}`];
            }
            for (const a of Object.values(f.alarms)) {
                for (const k of ['low_limit', 'high_limit', 'trigger', 'release']) if (a[k] === '') delete a[k];
            }
            if (f.notifications.zabbix.port === '') delete f.notifications.zabbix.port;
            return { ...f, clear };
        },

        /**
         * Persists all settings to backend via REST API.
         * Sends single atomic PUT to /api/settings.
         */
        async saveSettings() {
            if (!this.form || this.saving) return;
            this.saving = true;
            try {
                await requestJSON('/api/settings', { method: 'PUT', body: this.settingsPayload() });

                // Reload config in background to sync state
                this.loadSettings();
                this.showDashboard();
                this.showToast('Settings saved', 'success');
            } catch (err) {
                this.showToast(`Failed to save settings: ${err.message}`, 'error');
            } finally {
                this.saving = false;
            }
        },

        // Test unsaved form values; empty secrets fall back to saved values server-side.
        async sendTest(type) {
            if (!this.testStates[type]) return;
            this.startTestState(type);
            try {
                await requestJSON('/api/notifications/test', {
                    method: 'POST',
                    body: { channel: type, notifications: this.settingsPayload().notifications },
                });
                this.finishTestState(type, 'Sent!');
            } catch (err) {
                this.finishTestState(type, 'Failed');
                const typeName = type.charAt(0).toUpperCase() + type.slice(1);
                this.showToast(`${typeName} test failed: ${err.message}`, 'error');
            }
        },

        startTestState(type) {
            if (this._testResetTimeouts[type]) {
                clearTimeout(this._testResetTimeouts[type]);
                delete this._testResetTimeouts[type];
            }
            this.testStates[type].pending = true;
            this.testStates[type].text = 'Testing...';
        },

        finishTestState(type, text, resetText = 'Test') {
            this.testStates[type].pending = false;
            this.testStates[type].text = text;
            this._testResetTimeouts[type] = setTimeout(() => {
                if (this.testStates[type]) this.testStates[type].text = resetText;
                delete this._testResetTimeouts[type];
            }, TEST_FEEDBACK_MS);
        },

        // --- Events ---

        /**
         * Loads the events, one request at a time; a reset while one is in
         * flight shows the loading state until that request answers.
         * @param {boolean} reset - If true, shows the loading state and replaces events
         */
        async loadEvents(reset = true) {
            if (reset) {
                this.events = [];
                this.eventsError = '';
                this.eventsLoading = true;
            }
            if (this._eventsPolling) return;
            this._eventsPolling = true;
            try {
                this.events = await requestJSON('/api/events', { signal: AbortSignal.timeout(STALE_MS) });
                this.eventsError = '';
            } catch (error) {
                console.error('Failed to load events:', error);
                this.eventsError = `Could not load events: ${error?.message || 'request failed'}`;
            } finally {
                this._eventsPolling = false;
                this.eventsLoading = false;
            }
        },

        // Active alarms, with the event that raised them.
        attentionItems() {
            return this.activeAlarms().map(a => {
                const raised = this.events.find(e => e.alarm === a.key && e.state !== 'ok');
                return { key: a.key, title: this.defLabel(a.key), statusText: STATE_LABELS[a.state], detail: raised?.value || '', time: raised?.timestamp };
            });
        },

        eventSummaryState() {
            if (this.eventsError) return 'error';
            return this.activeAlarms().length > 0 ? 'attention' : 'clear';
        },

        eventSummaryText() {
            if (this.eventsError) return 'History unavailable';
            const n = this.activeAlarms().length;
            return n > 0 ? `${n} need${n === 1 ? 's' : ''} attention` : 'All clear';
        },

        eventSummaryMutedText() {
            if (this.eventsError) return 'Could not load events';
            const n = this.events.length;
            return `${n} event${n === 1 ? '' : 's'} since txmon started`;
        },

        eventRowStateClass(event) {
            return event.state === 'ok' ? 'state-success' : 'state-danger';
        },

        formatEventTime(ts) {
            if (!ts) return '';
            return new Date(ts).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' });
        },

        formatRelativeEventTime(value) {
            const time = typeof value === 'number' ? value : new Date(value).getTime();
            if (!Number.isFinite(time)) return '';

            const diff = Math.max(0, this.now - time);
            if (diff < MS_PER_MINUTE) return 'just now';
            if (diff < 60 * MS_PER_MINUTE) return RELATIVE_TIME_FORMATTER.format(-Math.floor(diff / MS_PER_MINUTE), 'minute');
            if (diff < MS_PER_DAY) return RELATIVE_TIME_FORMATTER.format(-Math.floor(diff / (60 * MS_PER_MINUTE)), 'hour');
            return RELATIVE_TIME_FORMATTER.format(-Math.floor(diff / MS_PER_DAY), 'day');
        },

        // --- Toasts and sign-out ---

        // Auto-dismiss toasts, keeping errors longer; evict the oldest at MAX_TOASTS.
        showToast(message, type = 'success') {
            const id = ++this._toastIdCounter;
            const duration = type === 'error' ? TOAST_DURATION_ERROR : TOAST_DURATION_SUCCESS;

            const toast = {
                id,
                message,
                type,
                timeoutId: setTimeout(() => this.removeToast(id), duration)
            };

            this.toasts.unshift(toast);

            while (this.toasts.length > MAX_TOASTS) {
                const oldest = this.toasts.pop();
                if (oldest.timeoutId) clearTimeout(oldest.timeoutId);
            }
        },

        removeToast(id) {
            const index = this.toasts.findIndex(t => t.id === id);
            if (index !== -1) {
                const toast = this.toasts[index];
                if (toast.timeoutId) clearTimeout(toast.timeoutId);
                this.toasts.splice(index, 1);
            }
        },

        // Cancel toast timers as well as messages on navigation and reconnect.
        clearToasts() {
            for (const toast of this.toasts) {
                if (toast.timeoutId) clearTimeout(toast.timeoutId);
            }
            this.toasts = [];
        },

        async signOut() {
            try {
                await requestJSON('/logout', { method: 'POST' });
                window.location.assign('/login');
            } catch (error) {
                // An expired session is already signed out.
                if (error.status === 401) {
                    window.location.assign('/login');
                    return;
                }
                this.showToast(error.message, 'error');
            }
        },
    }));
});
