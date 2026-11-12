import assert from 'node:assert/strict';
import test from 'node:test';
import { readFile } from 'node:fs/promises';

import {
  appendVODConversionControl,
  appendVODDownloadPresetBadge,
  formatVODDuration,
  formatVODMediaInfo,
  formatVODRemainingTime,
  vodConversionRequest,
  vodDownloadRequest,
  vodPlaybackPath,
  vodPresetCommand,
} from '../html/js/vod_download.js';

let vodPageImport = 0;
async function loadVODPage(t, respond){
  function element(){
    return {
      value: 'all', children: [], listeners: {}, textContent: '',
      set innerHTML(value){ this.textContent = value; this.children = []; },
      classList: {add(){}, remove(){}, toggle(){}},
      appendChild(child){ this.children.push(child); },
      replaceChildren(...children){ this.children = children; },
      get options(){ return this.children; },
      add(child){ this.children.push(child); },
      setAttribute(){},
      addEventListener(event, callback){ this.listeners[event] = callback; },
    };
  }
  const elements = new Map();
  const timers = new Map();
  const requests = [];
  let timerID = 0;
  const globals = {
    document: {
      getElementById(id){
        if(!elements.has(id)) elements.set(id, element());
        return elements.get(id);
      },
      createElement: element,
      createDocumentFragment: element,
    },
    Option: function(text, value){ return {textContent: text, value}; },
    window: {setTimeout(callback){ timers.set(++timerID, callback); return timerID; }},
    clearTimeout(id){ timers.delete(id); },
    fetch: async (url, options)=>{
      requests.push({url, options});
      return respond(url, options);
    },
  };
  for(const [key, value] of Object.entries(globals)){
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, key);
    Object.defineProperty(globalThis, key, {value, configurable: true, writable: true});
    t.after(()=>{
      if(descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    });
  }
  await import('../html/js/vods.js?test=' + ++vodPageImport);
  await new Promise(resolve=>setImmediate(resolve));
  return {elements, timers, requests};
}

test('initial VOD load retries network and HTTP failures until recovery', async t=>{
  let attempts = 0;
  const page = await loadVODPage(t, url=>{
    if(url !== '/api/vods') return Response.json({});
    attempts++;
    if(attempts === 1) throw new Error('temporary network failure');
    if(attempts === 2) return new Response('temporary server failure', {status: 503});
    return Response.json([]);
  });
  assert.match(page.elements.get('vodList').textContent, /temporary network failure/);
  for(const expectedAttempt of [2, 3]){
    assert.equal(page.timers.size, 1, 'failed loads must leave exactly one retry scheduled');
    const [id, callback] = page.timers.entries().next().value;
    page.timers.delete(id);
    await callback();
    assert.equal(attempts, expectedAttempt);
    if(expectedAttempt === 2) assert.match(page.elements.get('vodList').textContent, /temporary server failure/);
  }
  assert.equal(page.timers.size, 0, 'successful loads without active downloads should stop polling');
  assert.doesNotMatch(page.elements.get('vodList').textContent, /Could not load/);
});

test('VOD progress polling recovers from an outage and stops when downloads finish', async (t)=>{
  let polls = 0;
  const page = await loadVODPage(t, (url)=>{
    if(url !== '/api/vods') return Response.json({});
    polls++;
    if(polls === 2) throw new Error('temporary outage');
    return Response.json(polls === 1 ? [{id: '123456789', downloadActive: true}] : []);
  });
  async function poll(){
    assert.equal(page.timers.size, 1);
    const [id, callback] = page.timers.entries().next().value;
    page.timers.delete(id);
    await callback();
  }
  await poll();
  assert.match(page.elements.get('vodList').textContent, /temporary outage/);
  assert.equal(page.timers.size, 1, 'an outage must leave a retry scheduled');
  await poll();
  assert.equal(polls, 3);
  assert.equal(page.timers.size, 0, 'completed downloads should stop polling');
});

test('VOD form submits numeric IDs of different lengths and rejects invalid IDs', async (t)=>{
  const page = await loadVODPage(t, (url, options)=>Response.json(
    url === '/api/vods' ? (options.method === 'POST' ? {} : []) : {},
  ));
  const submit = page.elements.get('vodForm').listeners.submit;
  const input = page.elements.get('vodId');
  const html = await readFile(new URL('../html/vods.html', import.meta.url), 'utf8');
  const inputHTML = html.match(/<input id="vodId"[^>]+>/)[0];
  const pattern = new RegExp('^(?:' + inputHTML.match(/pattern="([^"]+)"/)[1] + ')$');
  for(const id of ['123456789', '1234567890', '12345678901']){
    assert.match(id, pattern, 'native form validation must accept this ID');
    assert.ok(Number(inputHTML.match(/maxlength="(\d+)"/)[1]) >= id.length);
    input.value = id;
    await submit({preventDefault(){}});
    const posted = page.requests.filter(r=>r.options.method === 'POST');
    assert.equal(JSON.parse(posted.at(-1).options.body).id, id);
  }
  const requestsBefore = page.requests.length;
  for(const id of ['', 'not-an-id', '123/456', '1'.repeat(65)]){
    input.value = id;
    await submit({preventDefault(){}});
  }
  assert.equal(page.requests.length, requestsBefore, 'invalid IDs must not reach the API');
});

function renderBadge(status){
  const container = {children: [], appendChild(child){ this.children.push(child); }};
  const documentRef = {createElement(tagName){ return {tagName, className: '', textContent: ''}; }};
  const appended = appendVODDownloadPresetBadge(documentRef, container, status);
  return {appended, children: container.children};
}

test('codec badge renders for downloaded and active VODs', ()=>{
  for(const status of [
    {downloaded: true, active: false, preset: 'hevc'},
    {downloaded: false, active: true, preset: 'vp9'},
  ]){
    const result = renderBadge(status);
    assert.equal(result.appended, true);
    assert.equal(result.children.length, 2);
    assert.equal(result.children[0].textContent, '-');
    assert.equal(result.children[1].textContent, status.preset === 'hevc' ? 'HEVC' : 'VP9');
  }
});

test('codec badge is omitted when a VOD has no download or known preset', ()=>{
  assert.deepEqual(renderBadge({downloaded: false, active: false, preset: 'h264'}), {appended: false, children: []});
  assert.deepEqual(renderBadge({downloaded: true, active: false, preset: ''}), {appended: false, children: []});
});

test('download request sends the selected preset as JSON', ()=>{
  const request = vodDownloadRequest('vp9');
  assert.equal(request.method, 'POST');
  assert.equal(request.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(request.body), {preset: 'vp9'});
});

test('download request falls back to original', ()=>{
  assert.deepEqual(JSON.parse(vodDownloadRequest().body), {preset: 'original'});
});

test('playback path uses MKV only for completed downloads', ()=>{
  assert.equal(vodPlaybackPath('123456789', true), '/vod/123456789/file.mkv');
  assert.equal(vodPlaybackPath('123456789', false), '/vod/123456789/index.m3u8');
  assert.equal(vodPlaybackPath('123456789', true, true), '/vod/123456789/index.m3u8');
  assert.equal(vodPlaybackPath('id/with slash', true), '/vod/id%2Fwith%20slash/file.mkv');
});

test('conversion request sends the selected preset as JSON', ()=>{
  const request = vodConversionRequest('h264');
  assert.equal(request.method, 'POST');
  assert.equal(request.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(request.body), {preset: 'h264'});
});

test('conversion ETA is formatted for the VOD status line', ()=>{
  assert.equal(formatVODRemainingTime(90), '~2m remaining');
  assert.equal(formatVODRemainingTime(4350), '~1h 13m remaining');
});

test('Twitch VOD duration is formatted for display', ()=>{
  assert.equal(formatVODDuration('2h3m4s'), '2h 3m 4s');
  assert.equal(formatVODDuration('45m12s'), '45m 12s');
  assert.equal(formatVODDuration(''), '');
});

test('preset command preview matches the FFmpeg codec settings', ()=>{
  assert.equal(vodPresetCommand('original'), '-c copy');
  assert.match(vodPresetCommand('h264'), /libx264.*-crf 23.*aac/);
  assert.match(vodPresetCommand('hevc'), /libx265.*-crf 25.*aac/);
  assert.match(vodPresetCommand('vp9'), /libvpx-vp9.*-crf 32.*libopus/);
});

test('preset command preview uses server-resolved settings', ()=>{
  const commands = {
    original: '-c copy',
    hevc: '-c:v libx265 -x265-params pools=24:frame-threads=6',
  };
  assert.equal(vodPresetCommand('hevc', commands), commands.hevc);
  assert.match(vodPresetCommand('vp9', commands), /libvpx-vp9/);
});

test('original media codec and quality are formatted for display', ()=>{
  assert.equal(formatVODMediaInfo('h264', 1080, 6_200_000), 'H.264 · 1080p · 6.20 Mbps');
  assert.equal(formatVODMediaInfo('hevc', 720), 'HEVC · 720p');
  assert.equal(formatVODMediaInfo('', 1080), '1080p');
});

test('conversion control reveals its button and invokes conversion after selecting a compressed preset', ()=>{
  const listeners = new Map();
  const makeElement = (tagName)=>({
    tagName,
    children: [],
    className: '',
    value: tagName === 'select' ? 'original' : '',
    classList: {toggle(name, hidden){
      const classes = new Set(this.owner.className.split(/\s+/).filter(Boolean));
      if(hidden) classes.add(name); else classes.delete(name);
      this.owner.className = [...classes].join(' ');
    }},
    appendChild(child){ this.children.push(child); },
    setAttribute(){},
    addEventListener(name, listener){ listeners.set(tagName + ':' + name, listener); },
  });
  const documentRef = {createElement(tagName){
    const element = makeElement(tagName);
    element.classList.owner = element;
    return element;
  }};
  const container = makeElement('div');
  let converted = null;
  const {select, button} = appendVODConversionControl(documentRef, container, {
    title: 'Test VOD',
    onConvert(preset){ converted = preset; },
  });

  assert.match(button.className, /\bhidden\b/);
  select.value = 'hevc';
  listeners.get('select:change')();
  assert.doesNotMatch(button.className, /\bhidden\b/);
  listeners.get('button:click')();
  assert.equal(converted, 'hevc');
});
