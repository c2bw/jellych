import assert from 'node:assert/strict';
import test from 'node:test';

globalThis.window = {
  location: { origin: 'http://localhost:8080', href: 'http://localhost:8080/watch' },
  localStorage: {
    store: new Map(),
    getItem(k){ return this.store.has(k) ? this.store.get(k) : null; },
    setItem(k, v){ this.store.set(k, String(v)); },
  },
  setTimeout: (fn, ms)=>setTimeout(fn, ms),
  addEventListener(){},
};

const { aggregateStreamStats, initStats } = await import('../html/js/stats.js');
const { initPlayerControls } = await import('../html/js/player_controls.js');
const { initManager } = await import('../html/js/manager.js');

function makeBufferedRange(start, end){
  return {
    length: 1,
    start(i){ return i === 0 ? start : 0; },
    end(i){ return i === 0 ? end : 0; },
  };
}

class FakeElement extends EventTarget {
  constructor(tagName = 'div'){
    super();
    this.tagName = tagName.toUpperCase();
    this.className = '';
    this.textContent = '';
    this.disabled = false;
    this.value = '';
    this.title = '';
    this.tabIndex = 0;
    this.dataset = {};
    this.attributes = new Map();
    this.children = [];
    this.parentNode = null;
    this.ownerDocument = null;
    this.style = {
      props: new Map(),
      order: '',
      setProperty(k, v){ this.props.set(k, v); },
    };
    const self = this;
    this.classList = {
      add(...tokens){
        const set = new Set(self.className.split(/\s+/).filter(Boolean));
        tokens.forEach((t)=>set.add(t));
        self.className = Array.from(set).join(' ');
      },
      remove(...tokens){
        const set = new Set(self.className.split(/\s+/).filter(Boolean));
        tokens.forEach((t)=>set.delete(t));
        self.className = Array.from(set).join(' ');
      },
      toggle(token, force){
        const has = this.contains(token);
        const next = force === undefined ? !has : !!force;
        if(next) this.add(token);
        else this.remove(token);
        return next;
      },
      contains(token){
        return self.className.split(/\s+/).filter(Boolean).includes(token);
      },
    };
  }

  get firstElementChild(){
    return this.children[0] || null;
  }

  setAttribute(name, val){
    this.attributes.set(name, String(val));
    if(name.startsWith('data-')){
      const camel = name.slice(5).replace(/-([a-z])/g, (_, c)=>c.toUpperCase());
      this.dataset[camel] = String(val);
    }
  }

  getAttribute(name){
    return this.attributes.has(name) ? this.attributes.get(name) : null;
  }

  appendChild(child){
    if(child && child.isFragment){
      for(const c of child.children){
        this.appendChild(c);
      }
      child.children = [];
      return child;
    }
    if(child.parentNode && child.parentNode !== this){
      child.remove();
    }
    child.parentNode = this;
    child.ownerDocument = this.ownerDocument;
    this.children.push(child);
    return child;
  }

  remove(){
    if(!this.parentNode) return;
    const idx = this.parentNode.children.indexOf(this);
    if(idx >= 0) this.parentNode.children.splice(idx, 1);
    this.parentNode = null;
  }

  contains(target){
    if(target === this) return true;
    return this.children.some((c)=>c.contains && c.contains(target));
  }

  querySelector(selector){
    return this.querySelectorAll(selector)[0] || null;
  }

  querySelectorAll(selector){
    const results = [];
    const match = (el)=>{
      if(selector === 'video') return el.tagName === 'VIDEO';
      const attrValMatch = selector.match(/^\[([a-z0-9_-]+)="([^"]+)"\]$/i);
      if(attrValMatch){
        return el.getAttribute(attrValMatch[1]) === attrValMatch[2];
      }
      const attrPresenceMatch = selector.match(/^\[([a-z0-9_-]+)\]$/i);
      if(attrPresenceMatch){
        return el.getAttribute(attrPresenceMatch[1]) !== null;
      }
      return false;
    };
    const visit = (node)=>{
      for(const child of node.children || []){
        if(match(child)) results.push(child);
        visit(child);
      }
    };
    visit(this);
    return results;
  }
}

class FakeVideo extends FakeElement {
  constructor(){
    super('video');
    this.src = '';
    this.currentSrc = '';
    this.paused = true;
    this.ended = false;
    this.muted = false;
    this.volume = 1;
    this.readyState = 0;
    this.currentTime = 0;
    this.buffered = { length: 0 };
  }

  play(){
    this.paused = false;
    this.dispatchEvent(new Event('play'));
    return Promise.resolve();
  }

  pause(){
    this.paused = true;
    this.dispatchEvent(new Event('pause'));
  }
}

function buildStageDOM(doc){
  const stage = new FakeElement('div');
  stage.ownerDocument = doc;
  stage.className = 'player-stage';

  const titleBar = new FakeElement('div');
  titleBar.className = 'player-title hidden';
  titleBar.setAttribute('data-control', 'title-bar');

  const titleText = new FakeElement('div');
  titleText.setAttribute('data-control', 'title-text');
  titleBar.appendChild(titleText);

  const tileActions = new FakeElement('div');
  tileActions.className = 'tile-actions hidden';
  tileActions.setAttribute('data-control', 'tile-actions');

  for(const action of ['spotlight', 'move-prev', 'move-next', 'close']){
    const btn = new FakeElement('button');
    btn.setAttribute('data-tile-action', action);
    tileActions.appendChild(btn);
  }
  titleBar.appendChild(tileActions);
  stage.appendChild(titleBar);

  const video = new FakeVideo();
  stage.appendChild(video);

  const centerPlay = new FakeElement('button');
  centerPlay.setAttribute('data-control', 'center-play');
  stage.appendChild(centerPlay);

  const controls = new FakeElement('div');
  const liveBadge = new FakeElement('button');
  liveBadge.setAttribute('data-control', 'live-badge');
  const liveBadgeText = new FakeElement('span');
  liveBadgeText.setAttribute('data-control', 'live-badge-text');
  liveBadgeText.textContent = 'Live';
  liveBadge.appendChild(liveBadgeText);
  controls.appendChild(liveBadge);

  const timeControls = new FakeElement('div');
  timeControls.className = 'player-time-controls hidden';
  timeControls.setAttribute('data-control', 'time-controls');
  const seekBack = new FakeElement('button');
  seekBack.setAttribute('data-control', 'seek-back');
  const seekRange = new FakeElement('input');
  seekRange.setAttribute('data-control', 'seek-range');
  seekRange.value = '0';
  const seekForward = new FakeElement('button');
  seekForward.setAttribute('data-control', 'seek-forward');
  timeControls.appendChild(seekBack);
  timeControls.appendChild(seekRange);
  timeControls.appendChild(seekForward);
  controls.appendChild(timeControls);

  for(const controlName of ['play-toggle', 'low-latency-toggle', 'mute-toggle', 'pip-toggle', 'fullscreen-toggle']){
    const btn = new FakeElement('button');
    btn.setAttribute('data-control', controlName);
    controls.appendChild(btn);
  }
  const volume = new FakeElement('input');
  volume.setAttribute('data-control', 'volume-range');
  volume.value = '1';
  controls.appendChild(volume);
  stage.appendChild(controls);

  // Override innerHTML setter on dynamic stages so FakeElement tree is populated in Node tests
  Object.defineProperty(stage, 'innerHTML', {
    get(){ return ''; },
    set(){
      stage.children = [];
      stage.appendChild(titleBar);
      stage.appendChild(video);
      stage.appendChild(centerPlay);
      stage.appendChild(controls);
    },
  });

  return { stage, video, titleBar };
}

test('aggregateStreamStats returns Idle when no streams are active', ()=>{
  const res = aggregateStreamStats([]);
  assert.equal(res.activeCount, 0);
  assert.equal(res.stateText, 'Idle');
  assert.deepEqual(res.items, [
    { label: 'Current', value: '-' },
    { label: 'Buffered', value: '-' },
    { label: 'Dropped frames', value: '-' },
    { label: 'Bandwidth', value: '-' },
  ]);
});

test('aggregateStreamStats sums Buffered, Dropped frames, and Bandwidth while keeping max Current duration', ()=>{
  const stream1 = {
    video: {
      readyState: 4,
      paused: false,
      currentSrc: 'blob:1',
      currentTime: 65, // 1:05
      buffered: makeBufferedRange(60, 73), // 8s ahead
      getVideoPlaybackQuality(){
        return { droppedVideoFrames: 2, totalVideoFrames: 1800 };
      },
    },
    player: {
      getBandwidthEstimate(){ return 4_000_000; }, // 4.00 Mbps
    },
  };

  const stream2 = {
    video: {
      readyState: 4,
      paused: false,
      currentSrc: 'blob:2',
      currentTime: 125, // 2:05 (longer duration)
      buffered: makeBufferedRange(120, 129), // 4s ahead
      getVideoPlaybackQuality(){
        return { droppedVideoFrames: 3, totalVideoFrames: 3600 };
      },
    },
    player: {
      getBandwidthEstimate(){ return 6_000_000; }, // 6.00 Mbps
    },
  };

  const res = aggregateStreamStats([stream1, stream2]);
  assert.equal(res.activeCount, 2);
  assert.equal(res.stateText, 'Playing (2)');
  assert.deepEqual(res.items, [
    { label: 'Current', value: '2:05' },        // max(65, 125) = 125s (NOT summed 190s)
    { label: 'Buffered', value: '12.0s' },      // 8s + 4s = 12s
    { label: 'Dropped frames', value: '5/5400' }, // (2+3)/(1800+3600)
    { label: 'Bandwidth', value: '10 Mbps' },   // 4 Mbps + 6 Mbps
  ]);
});

test('initPlayerControls scopes mute, volume, and low-latency controls to each tile stage independently', ()=>{
  const doc = new EventTarget();
  doc.pictureInPictureEnabled = true;

  const tile1 = buildStageDOM(doc);
  const tile2 = buildStageDOM(doc);

  let lowLatency1 = false;
  let lowLatency2 = false;
  const player1 = {
    isLowLatencyMode: ()=>lowLatency1,
    setLowLatencyMode: (v)=>{ lowLatency1 = !!v; },
  };
  const player2 = {
    isLowLatencyMode: ()=>lowLatency2,
    setLowLatencyMode: (v)=>{ lowLatency2 = !!v; },
  };

  const ctrl1 = initPlayerControls({ video: tile1.video, stage: tile1.stage, player: player1 });
  const ctrl2 = initPlayerControls({ video: tile2.video, stage: tile2.stage, player: player2 });

  // Both start unmuted
  assert.equal(tile1.video.muted, false);
  assert.equal(tile2.video.muted, false);

  // Mute tile 1 only
  const muteBtn1 = tile1.stage.querySelector('[data-control="mute-toggle"]');
  muteBtn1.dispatchEvent(new Event('click'));

  assert.equal(tile1.video.muted, true);
  assert.equal(tile1.stage.classList.contains('is-muted'), true);
  assert.equal(tile2.video.muted, false);
  assert.equal(tile2.stage.classList.contains('is-muted'), false);

  // Toggle low latency on tile 2 only
  const llBtn2 = tile2.stage.querySelector('[data-control="low-latency-toggle"]');
  llBtn2.dispatchEvent(new Event('click'));
  assert.equal(lowLatency2, true);
  assert.equal(lowLatency1, false);

  ctrl1.destroy();
  ctrl2.destroy();
});

test('initManager Multi mode adds tiles, supports layout/spotlight/reordering, and keeps focused stream when Multi is turned off', ()=>{
  const doc = new EventTarget();
  doc.createElement = (tag)=>{
    if(tag === 'div'){
      const built = buildStageDOM(doc);
      return built.stage;
    }
    return new FakeElement(tag);
  };
  doc.createDocumentFragment = ()=>({ isFragment: true, children: [], appendChild(c){ this.children.push(c); } });
  globalThis.document = doc;

  const primary = buildStageDOM(doc);
  const gridEl = new FakeElement('div');
  gridEl.ownerDocument = doc;
  gridEl.appendChild(primary.stage);

  const multiToggleEl = new FakeElement('button');
  const multiToolbarEl = new FakeElement('div');
  for(const layout of ['auto', 'spotlight', 'columns', 'stacked']){
    const btn = new FakeElement('button');
    btn.setAttribute('data-layout-option', layout);
    multiToolbarEl.appendChild(btn);
  }
  const multiCountEl = new FakeElement('span');

  const createMockPlayer = (video)=>({
    video,
    currentUrl: '',
    stopped: false,
    timeShifted: false,
    liveEdge: 100,
    minTime: 70,
    play(url){
      this.currentUrl = url;
      this.stopped = false;
      this.timeShifted = false;
      video.src = url;
      video.currentSrc = url;
      video.readyState = 4;
      video.paused = false;
      video.currentTime = this.liveEdge;
    },
    stop(){
      this.stopped = true;
      this.timeShifted = false;
      this.currentUrl = '';
      video.src = '';
      video.currentSrc = '';
      video.readyState = 0;
      video.paused = true;
    },
    pause(){ video.paused = true; },
    resume(){ video.paused = false; },
    getStartupSegmentCount(){ return 4; },
    getBandwidthEstimate(){ return 3_000_000; },
    isLowLatencyMode(){ return false; },
    setLowLatencyMode(){},
    isTimeShifted(){ return this.timeShifted; },
    getLiveSeekWindow(){
      const behind = Math.max(0, this.liveEdge - video.currentTime);
      return {
        minTime: this.minTime,
        maxTime: this.liveEdge,
        liveEdge: this.liveEdge,
        currentTime: video.currentTime,
        windowSeconds: this.liveEdge - this.minTime,
        behindLiveSeconds: behind,
        isLive: !this.timeShifted || behind <= 0.25,
      };
    },
    seekToLive(){
      this.timeShifted = false;
      video.currentTime = this.liveEdge;
      video.dispatchEvent(new Event('timeupdate'));
      return this.liveEdge;
    },
    seekToLiveOffset(offset){
      const clamped = Math.min(this.liveEdge - this.minTime, Math.max(0, Number(offset) || 0));
      if(clamped <= 0.25) return this.seekToLive();
      this.timeShifted = true;
      video.currentTime = this.liveEdge - clamped;
      video.dispatchEvent(new Event('timeupdate'));
      return this.getLiveSeekWindow();
    },
    seekBy(delta){
      const behind = Math.max(0, this.liveEdge - video.currentTime);
      return this.seekToLiveOffset(behind - delta);
    },
  });

  const primaryPlayer = createMockPlayer(primary.video);
  const statsOverlay = new FakeElement('section');
  const statsState = new FakeElement('div');
  const statsGrid = new FakeElement('div');

  let managerRef = null;
  const stats = initStats({
    video: primary.video,
    player: primaryPlayer,
    getStreams: ()=>managerRef ? managerRef.getActiveStreams() : [],
    statsOverlay,
    statsState,
    statsGrid,
  });

  const chatChannels = [];
  const playbackStates = [];
  managerRef = initManager({
    listEl: new FakeElement('div'),
    removeSelect: new FakeElement('select'),
    addBtn: new FakeElement('button'),
    newNameEl: new FakeElement('input'),
    removeBtn: new FakeElement('button'),
    managerMsgEl: new FakeElement('div'),
    player: primaryPlayer,
    stats,
    playerTitleEl: primary.titleBar,
    playerStageEl: primary.stage,
    playerGridEl: gridEl,
    multiToggleEl,
    multiToolbarEl,
    multiCountEl,
    createPlayer: createMockPlayer,
    setupPlayerControls: ()=>({ destroy(){} }),
    onPlaybackChannelChange: (ch)=>chatChannels.push(ch),
    onPlaybackStateChange: (state)=>playbackStates.push(state),
  });

  // Enable Multi mode via toggle click
  multiToggleEl.dispatchEvent(new Event('click'));
  assert.equal(managerRef.isMultiMode(), true);
  assert.equal(multiToggleEl.getAttribute('aria-pressed'), 'true');
  assert.equal(multiToolbarEl.classList.contains('hidden'), false);
  assert.deepEqual(playbackStates.at(-1), {
    channels: [],
    focusedChannel: null,
    multiMode: true,
  });

  // Play first stream in Multi mode -> single stream keeps full-page single stage layout (no .is-multi)
  managerRef.playDirectChannel('alpha');
  assert.equal(gridEl.classList.contains('is-multi'), false);

  // Play two more streams in Multi mode -> activates .is-multi grid tiling
  managerRef.playDirectChannel('bravo');
  managerRef.playDirectChannel('charlie');
  assert.equal(gridEl.classList.contains('is-multi'), true);

  const active = managerRef.getActiveStreams();
  assert.equal(active.length, 3);
  assert.deepEqual(active.map((s)=>s.channel), ['alpha', 'bravo', 'charlie']);
  assert.equal(multiCountEl.textContent, '3 streams');
  assert.equal(statsState.textContent, 'Playing (3)');

  // All three streams start unmuted
  for(const s of active){
    assert.equal(s.video.muted, false);
  }

  // Promote 'charlie' to spotlight via its tile spotlight button
  const charlieStage = gridEl.children.find((el)=>el.dataset.channel === 'charlie');
  assert.ok(charlieStage);
  const spotlightBtn = charlieStage.querySelector('[data-tile-action="spotlight"]');
  spotlightBtn.dispatchEvent(new Event('click'));

  assert.equal(managerRef.getTilingLayout(), 'spotlight');
  assert.equal(gridEl.dataset.layout, 'spotlight');
  assert.deepEqual(managerRef.getActiveStreams().map((s)=>s.channel), ['charlie', 'alpha', 'bravo']);
  assert.equal(charlieStage.classList.contains('is-spotlight'), true);
  assert.equal(chatChannels.at(-1), 'charlie');

  // Move 'bravo' earlier (from index 2 to index 1)
  const bravoStage = gridEl.children.find((el)=>el.dataset.channel === 'bravo');
  const movePrevBtn = bravoStage.querySelector('[data-tile-action="move-prev"]');
  movePrevBtn.dispatchEvent(new Event('click'));
  assert.deepEqual(managerRef.getActiveStreams().map((s)=>s.channel), ['charlie', 'bravo', 'alpha']);
  assert.deepEqual(playbackStates.at(-1), {
    channels: ['charlie', 'bravo', 'alpha'],
    focusedChannel: 'charlie',
    multiMode: true,
  });

  // Time controls are visible on tiles while Multi mode is active
  const charlieTimeControls = charlieStage.querySelector('[data-control="time-controls"]');
  assert.equal(charlieTimeControls.classList.contains('hidden'), false);
  // Time-shift 'charlie' back by 5 seconds before turning Multi mode off
  const charlieStream = managerRef.getActiveStreams().find((s)=>s.channel === 'charlie');
  charlieStream.player.seekToLiveOffset(5);
  assert.equal(charlieStream.video.currentTime, 95);
  assert.equal(charlieStream.player.isTimeShifted(), true);

  // Toggle Multi mode OFF -> keeps focused stream ('charlie'), hides time-controls, and snaps 'charlie' back to live
  multiToggleEl.dispatchEvent(new Event('click'));
  assert.equal(managerRef.isMultiMode(), false);
  assert.equal(charlieTimeControls.classList.contains('hidden'), true);
  assert.equal(charlieStream.player.isTimeShifted(), false);
  assert.equal(charlieStream.video.currentTime, 100);
  const remaining = managerRef.getActiveStreams();
  assert.equal(remaining.length, 1);
  assert.equal(remaining[0].channel, 'charlie');
  assert.equal(statsState.textContent, 'Playing');

  managerRef.stopAllPlayback(true);
  assert.equal(managerRef.getActiveStreams().length, 0);
  assert.equal(statsState.textContent, 'Idle');
});

test('initPlayerControls time-shift buttons (-1s / +1s), range slider, and live badge sync two streams independently', ()=>{
  const doc = new EventTarget();
  doc.pictureInPictureEnabled = true;

  const tile1 = buildStageDOM(doc);
  const tile2 = buildStageDOM(doc);
  tile1.video.src = '/live/one/index.m3u8';
  tile2.video.src = '/live/two/index.m3u8';
  tile1.video.currentTime = 100;
  tile2.video.currentTime = 100;

  const makePlayer = (video)=>{
    let timeShifted = false;
    const liveEdge = 100;
    const minTime = 70;
    return {
      isLowLatencyMode: ()=>false,
      setLowLatencyMode(){},
      isTimeShifted: ()=>timeShifted,
      getLiveSeekWindow(){
        const behind = Math.max(0, liveEdge - video.currentTime);
        return {
          minTime,
          maxTime: liveEdge,
          liveEdge,
          currentTime: video.currentTime,
          windowSeconds: liveEdge - minTime,
          behindLiveSeconds: behind,
          isLive: !timeShifted || behind <= 0.25,
        };
      },
      seekToLive(){
        timeShifted = false;
        video.currentTime = liveEdge;
      },
      seekToLiveOffset(offset){
        const clamped = Math.min(liveEdge - minTime, Math.max(0, Number(offset) || 0));
        if(clamped <= 0.25){
          this.seekToLive();
          return this.getLiveSeekWindow();
        }
        timeShifted = true;
        video.currentTime = liveEdge - clamped;
        return this.getLiveSeekWindow();
      },
      seekBy(delta){
        const behind = Math.max(0, liveEdge - video.currentTime);
        return this.seekToLiveOffset(behind - delta);
      },
    };
  };

  const ctrl1 = initPlayerControls({ video: tile1.video, stage: tile1.stage, player: makePlayer(tile1.video) });
  const ctrl2 = initPlayerControls({ video: tile2.video, stage: tile2.stage, player: makePlayer(tile2.video) });

  const seekBack1 = tile1.stage.querySelector('[data-control="seek-back"]');
  const seekForward1 = tile1.stage.querySelector('[data-control="seek-forward"]');
  const seekRange1 = tile1.stage.querySelector('[data-control="seek-range"]');
  const liveBadge1 = tile1.stage.querySelector('[data-control="live-badge"]');
  const liveBadgeText1 = tile1.stage.querySelector('[data-control="live-badge-text"]');
  const liveBadgeText2 = tile2.stage.querySelector('[data-control="live-badge-text"]');

  assert.equal(liveBadgeText1.textContent, 'Live');
  assert.equal(seekForward1.disabled, true);

  // Click -1s twice on tile 1 to rewind by 2 seconds
  seekBack1.dispatchEvent(new Event('click'));
  assert.equal(tile1.video.currentTime, 99);
  assert.equal(liveBadgeText1.textContent, '-1s · Live');
  assert.equal(liveBadge1.classList.contains('is-behind-live'), true);

  seekBack1.dispatchEvent(new Event('click'));
  assert.equal(tile1.video.currentTime, 98);
  assert.equal(liveBadgeText1.textContent, '-2s · Live');

  // Tile 2 is untouched at Live
  assert.equal(tile2.video.currentTime, 100);
  assert.equal(liveBadgeText2.textContent, 'Live');

  // Click +1s once on tile 1 -> moves to -1s behind live
  seekForward1.dispatchEvent(new Event('click'));
  assert.equal(tile1.video.currentTime, 99);
  assert.equal(liveBadgeText1.textContent, '-1s · Live');

  // Drag range slider on tile 1 to -12s
  seekRange1.value = '-12';
  seekRange1.dispatchEvent(new Event('input'));
  assert.equal(tile1.video.currentTime, 88);
  assert.equal(liveBadgeText1.textContent, '-12s · Live');

  // Click Live badge on tile 1 -> jumps right back to live edge (100)
  liveBadge1.dispatchEvent(new Event('click'));
  assert.equal(tile1.video.currentTime, 100);
  assert.equal(liveBadgeText1.textContent, 'Live');
  assert.equal(liveBadge1.classList.contains('is-behind-live'), false);

  ctrl1.destroy();
  ctrl2.destroy();
});
