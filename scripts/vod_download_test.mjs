import assert from 'node:assert/strict';
import test from 'node:test';

import {
  appendVODConversionControl,
  appendVODDownloadPresetBadge,
  formatVODDuration,
  formatVODMediaInfo,
  formatVODRemainingTime,
  vodConversionRequest,
  vodDownloadRequest,
  vodPresetCommand,
} from '../html/js/vod_download.js';

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
