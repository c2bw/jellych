import assert from 'node:assert/strict';
import test from 'node:test';

globalThis.window = {};

const { Player } = await import('../html/js/player.js');

class NativeHLSVideo extends EventTarget {
  constructor(){
    super();
    this.src = '';
    this.loadCalls = 0;
    this.playCalls = 0;
    this.error = null;
  }

  canPlayType(type){
    return type === 'application/vnd.apple.mpegurl' ? 'maybe' : '';
  }

  play(){
    this.playCalls++;
    return Promise.resolve();
  }

  pause(){}

  removeAttribute(name){
    if(name === 'src') this.src = '';
  }

  load(){
    this.loadCalls++;
  }
}

function fakeManagedHls(){
  return {
    startLoadCalls: 0,
    stopLoadCalls: 0,
    startLoad(){ this.startLoadCalls++; },
    stopLoad(){ this.stopLoadCalls++; },
    destroy(){},
  };
}

function wait(ms){
  return new Promise(resolve=>setTimeout(resolve, ms));
}

test('native HLS reloads the manifest after a media error', async (t)=>{
  const video = new NativeHLSVideo();
  const player = new Player(video);
  t.after(()=>player.stop());

  const url = '/live/testchannel/index.m3u8';
  player.play(url);
  const loadCallsBeforeError = video.loadCalls;
  video.error = { code: 2 };
  video.dispatchEvent(new Event('error'));

  await wait(600);

  assert.match(video.src, /^\/live\/testchannel\/index\.m3u8\?_jellych_live=\d+$/);
  assert.equal(video.loadCalls, loadCallsBeforeError + 1);
  assert.equal(video.playCalls, 2);
});

test('pausing cancels pending native HLS recovery', async (t)=>{
  const video = new NativeHLSVideo();
  const player = new Player(video);
  t.after(()=>player.stop());

  const url = '/live/testchannel/index.m3u8';
  player.play(url);
  video.error = { code: 2 };
  video.dispatchEvent(new Event('error'));
  player.pause();

  await wait(600);

  assert.equal(video.src, url);
  assert.equal(video.playCalls, 1);
});

test('native playback resuming cancels a pending recovery reload', async (t)=>{
  const video = new NativeHLSVideo();
  const player = new Player(video);
  t.after(()=>player.stop());

  const url = '/live/testchannel/index.m3u8';
  player.play(url);
  video.error = { code: 2 };
  video.dispatchEvent(new Event('error'));
  video.dispatchEvent(new Event('playing'));

  await wait(600);

  assert.equal(video.src, url);
  assert.equal(video.playCalls, 1);
});

test('native HLS recovery ignores non-network errors and stops at the retry cap', async (t)=>{
  const video = new NativeHLSVideo();
  const player = new Player(video);
  t.after(()=>player.stop());

  const url = '/live/testchannel/index.m3u8';
  player.play(url);

  video.error = { code: 3 };
  video.dispatchEvent(new Event('error'));
  assert.equal(player.nativeRecoveryTimer, null);

  player.nativeRecoveryAttempts = 5;
  video.error = { code: 2 };
  video.dispatchEvent(new Event('error'));
  assert.equal(player.nativeRecoveryTimer, null);
  assert.equal(video.src, url);
});

test('buffering events do not bypass Hls.js network recovery delay', (t)=>{
  const video = new NativeHLSVideo();
  const player = new Player(video);
  t.after(()=>player.stop());

  const hls = fakeManagedHls();
  player.hls = hls;
  player.currentUrl = '/live/testchannel/index.m3u8';
  player.wantsPlayback = true;
  player.recoverNetworkError(hls);

  for(let i = 0; i < 100; i++) player.resume();

  assert.equal(hls.stopLoadCalls, 1);
  assert.equal(hls.startLoadCalls, 0);
});

test('resuming Hls.js after a user pause restarts loading only once', (t)=>{
  const video = new NativeHLSVideo();
  const player = new Player(video);
  t.after(()=>player.stop());

  const hls = fakeManagedHls();
  player.hls = hls;
  player.wantsPlayback = true;
  player.pause();

  player.notePlaybackWanted();
  player.notePlaybackWanted();
  player.resume();

  assert.equal(hls.startLoadCalls, 1);
});
