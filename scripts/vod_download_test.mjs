import assert from 'node:assert/strict';
import test from 'node:test';

import { appendVODDownloadPresetBadge, vodDownloadRequest } from '../html/js/vod_download.js';

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
