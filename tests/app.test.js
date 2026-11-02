import { expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

const source = readFileSync(new URL('../web/app.js', import.meta.url), 'utf8');

const reply = (data) => ({ ok: true, status: 200, json: async () => data });
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

// Runs app.js in a sandbox, as zwfm-encoder's tests do, and returns its
// requestJSON and a fresh txmonApp component.
function load(fetch) {
    let factory;
    const focused = [];
    const context = {
        URL,
        AbortSignal,
        setTimeout,
        clearTimeout,
        console,
        confirm: () => true,
        window: { location: { href: 'http://txmon.local/', origin: 'http://txmon.local', assign() {} }, icons: {} },
        document: {
            addEventListener(type, listener) {
                if (type === 'alpine:init') listener();
            },
            querySelector: () => ({ content: 'session-token' }),
            getElementById: (id) => ({ focus: () => focused.push(id) }),
        },
        localStorage: { getItem: () => null, setItem() {} },
        Alpine: {
            data(_name, make) {
                factory = make;
            },
        },
        fetch,
    };
    const request = runInNewContext(`${source}\nrequestJSON;`, context);
    const app = factory();
    app.$nextTick = (callback) => callback();
    return { request, app, focused };
}

// A fetch that answers only when the test says so.
function heldFetch() {
    const calls = [];
    const fetch = (url, options) => new Promise((resolve) => calls.push({ url, options, resolve }));
    return { calls, fetch };
}

test('a body that times out is an error, while a body without JSON is null', async () => {
    const aborted = { ok: true, status: 200, json: async () => Promise.reject(new Error('The operation was aborted')) };
    const timedOut = load(async () => aborted);
    await expect(timedOut.request('/api/events', { signal: AbortSignal.abort() })).rejects.toThrow('aborted');

    const empty = load(async () => ({ ok: true, status: 204, json: async () => Promise.reject(new SyntaxError('no body')) }));
    expect(await empty.request('/api/rf', { method: 'POST', body: { on: true } })).toBeNull();
});

test('a control waits for the status in flight and then reads it again', async () => {
    const { calls, fetch } = heldFetch();
    const { app } = load(fetch);
    const poll = app.refresh();
    app.tick(); // the timer must not start a second request
    const afterControl = app.refresh();
    await flush();
    expect(calls).toHaveLength(1);

    calls[0].resolve(reply({ link: true, alarms: [], telemetry: { on_air: true } }));
    await poll;
    await flush();
    expect(calls).toHaveLength(2);

    calls[1].resolve(reply({ link: true, alarms: [], telemetry: { on_air: false } }));
    await afterControl;
    expect(app.status.telemetry.on_air).toBe(false);
});

test('controls send nothing without a connection to txmon', async () => {
    const posts = [];
    const { app } = load(async (url, options) => {
        posts.push(url);
        return reply(options.method === 'POST' ? {} : { link: true, alarms: [], telemetry: { on_air: false } });
    });
    app.status = { link: true, alarms: [], telemetry: { on_air: true, firmware: 210 } };
    app.now = Date.now();
    app.lastOk = 0;
    app.control.power = 50;
    app.setPower();
    app.setRF(false);
    expect(posts).toHaveLength(0);

    app.lastOk = app.now;
    app.setRF(false);
    await flush();
    expect(posts[0]).toBe('/api/rf');
});

test('events load one request at a time, with a deadline', async () => {
    const { calls, fetch } = heldFetch();
    const { app } = load(fetch);
    const first = app.loadEvents();
    app.loadEvents(false);
    expect(calls).toHaveLength(1);
    expect(calls[0].options.signal).toBeDefined();

    calls[0].resolve(reply([]));
    await first;
    app.loadEvents(false);
    expect(calls).toHaveLength(2);
});

test('Home and End select and focus the first and last tab', () => {
    const { app, focused } = load(async () => reply({}));
    app.view = 'settings';
    const key = (name) => ({ key: name, target: { tagName: 'BUTTON' }, preventDefault() {} });
    app.handleGlobalKeydown(key('End'));
    expect([app.settingsTab, focused.at(-1)]).toEqual(['about', 'tab-about']);
    app.handleGlobalKeydown(key('Home'));
    expect([app.settingsTab, focused.at(-1)]).toEqual(['alarms', 'tab-alarms']);
});
