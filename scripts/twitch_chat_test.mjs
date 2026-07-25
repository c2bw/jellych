import assert from 'node:assert/strict';
import test from 'node:test';

import {
  TwitchIRCClient,
  initTwitchChat,
  parseIRCLine,
  readChatVisibility,
  resolveUsernameColor,
  tokenizeChatMessage,
  unescapeIRCTag,
  writeChatVisibility,
} from '../html/js/twitch_chat.js';

class FakeClassList {
  constructor(){
    this.values = new Set();
  }

  add(value){ this.values.add(value); }
  remove(value){ this.values.delete(value); }
  contains(value){ return this.values.has(value); }
  toggle(value, force){
    const shouldAdd = force === undefined ? !this.values.has(value) : !!force;
    if(shouldAdd) this.values.add(value);
    else this.values.delete(value);
    return shouldAdd;
  }
}

class FakeElement {
  constructor(ownerDocument){
    this.ownerDocument = ownerDocument;
    this.classList = new FakeClassList();
    this.listeners = new Map();
    this.attributes = new Map();
    this.children = [];
    this.dataset = {};
    this.hidden = false;
    this.scrollHeight = 0;
    this.scrollTop = 0;
    this.clientHeight = 0;
    this._textContent = '';
  }

  addEventListener(type, listener){ this.listeners.set(type, listener); }
  removeEventListener(type){ this.listeners.delete(type); }
  setAttribute(name, value){ this.attributes.set(name, String(value)); }
  getAttribute(name){ return this.attributes.get(name); }
  focus(){ this.focused = true; }
  click(){ this.listeners.get('click')?.({ type: 'click' }); }
  get childElementCount(){ return this.children.length; }
  get firstElementChild(){ return this.children[0] || null; }
  get textContent(){ return this._textContent; }
  set textContent(value){
    this._textContent = String(value);
    this.children = [];
  }
}

class FakeDocument {
  constructor(){
    this.listeners = new Map();
  }

  addEventListener(type, listener){ this.listeners.set(type, listener); }
  removeEventListener(type){ this.listeners.delete(type); }
}

class FakeSocket {
  constructor(url){
    this.url = url;
    this.readyState = 0;
    this.sent = [];
    this.closeCalls = [];
    this.listeners = new Map();
  }

  addEventListener(type, listener){
    const listeners = this.listeners.get(type) || [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  emit(type, payload = {}){
    for(const listener of this.listeners.get(type) || []){
      listener({ type, ...payload });
    }
  }

  open(){
    this.readyState = 1;
    this.emit('open');
  }

  message(data){
    this.emit('message', { data });
  }

  serverClose(){
    this.readyState = 3;
    this.emit('close');
  }

  send(value){
    assert.equal(this.readyState, 1, 'socket must be open before sending');
    this.sent.push(value);
  }

  close(code, reason){
    this.closeCalls.push({ code, reason });
    this.readyState = 3;
  }
}

function fakeSockets(){
  const sockets = [];
  return {
    sockets,
    factory(url){
      const socket = new FakeSocket(url);
      sockets.push(socket);
      return socket;
    },
  };
}

function fakeTimers(){
  const timers = [];
  let nextId = 1;
  return {
    timers,
    setTimer(callback, delay){
      const timer = { id: nextId++, callback, delay, cancelled: false };
      timers.push(timer);
      return timer.id;
    },
    clearTimer(id){
      const timer = timers.find((candidate)=>candidate.id === id);
      if(timer) timer.cancelled = true;
    },
    runNext(){
      const timer = timers.find((candidate)=>!candidate.cancelled && !candidate.ran);
      assert.ok(timer, 'expected a pending timer');
      timer.ran = true;
      timer.callback();
      return timer;
    },
    pending(){
      return timers.filter((timer)=>!timer.cancelled && !timer.ran);
    },
  };
}

test('IRC parser handles tags, escapes, prefixes, params, and trailing text', ()=>{
  const line = '@display-name=Foo\\sBar;system-msg=semi\\:colon\\\\ok :foo!foo@foo.tmi.twitch.tv PRIVMSG #channel :hello world';
  const parsed = parseIRCLine(line);

  assert.equal(parsed.command, 'PRIVMSG');
  assert.equal(parsed.prefix, 'foo!foo@foo.tmi.twitch.tv');
  assert.deepEqual(parsed.params, ['#channel']);
  assert.equal(parsed.trailing, 'hello world');
  assert.equal(parsed.tags['display-name'], 'Foo Bar');
  assert.equal(parsed.tags['system-msg'], 'semi;colon\\ok');
  assert.equal(unescapeIRCTag('line\\nnext\\rrow'), 'line\nnext\rrow');
});

test('chat tokenization validates native emote ranges and preserves unicode text', ()=>{
  assert.deepEqual(tokenizeChatMessage('😀 Kappa!', '25:2-6'), [
    { type: 'text', text: '😀 ' },
    { type: 'emote', id: '25', text: 'Kappa' },
    { type: 'text', text: '!' },
  ]);
  assert.deepEqual(tokenizeChatMessage('hello', 'bad:0-2/25:8-10'), [
    { type: 'text', text: 'hello' },
  ]);
  assert.equal(resolveUsernameColor('foo', '#12AbEF'), '#12AbEF');
  assert.match(resolveUsernameColor('foo', 'red'), /^#[0-9a-f]{6}$/i);
});

test('client authenticates anonymously, joins the channel, answers PING, and parses partial batches', ()=>{
  const sockets = fakeSockets();
  const received = [];
  const states = [];
  const client = new TwitchIRCClient({
    socketFactory: sockets.factory,
    random: ()=>0,
    onMessage: (message)=>received.push(message),
    onState: (state)=>states.push(state),
  });

  client.setChannel('Test_Channel');
  client.setEnabled(true);
  assert.equal(sockets.sockets.length, 1);
  const socket = sockets.sockets[0];
  assert.equal(socket.url, 'wss://irc-ws.chat.twitch.tv:443');

  socket.open();
  assert.deepEqual(socket.sent, [
    'PASS blah\r\n',
    'NICK justinfan10000\r\n',
    'CAP REQ :twitch.tv/tags twitch.tv/commands\r\n',
    'JOIN #test_channel\r\n',
  ]);

  socket.message('PING :tmi.twitch.tv\r\n');
  assert.equal(socket.sent.at(-1), 'PONG :tmi.twitch.tv\r\n');

  socket.message(':tmi.twitch.tv 001 justinfan10000 :Welcome, GLHF!\r\n@id=m1;user-id=u1;display-name=Foo;color=#123456;');
  assert.equal(received.length, 0, 'partial IRC lines must be buffered');
  socket.message('emotes=25:0-4;tmi-sent-ts=123 :foo!foo@foo.tmi.twitch.tv PRIVMSG #test_channel :Kappa hello\r\n');

  assert.equal(states.at(-1).state, 'connected');
  assert.deepEqual(received, [{
    id: 'm1',
    userId: 'u1',
    username: 'foo',
    displayName: 'Foo',
    color: '#123456',
    text: 'Kappa hello',
    isAction: false,
    emotes: '25:0-4',
    timestamp: 123,
  }]);
});

test('client emits actions, system messages, and moderation updates only for the active channel', ()=>{
  const sockets = fakeSockets();
  const messages = [];
  const systems = [];
  const clearedMessages = [];
  const clearedChats = [];
  const client = new TwitchIRCClient({
    socketFactory: sockets.factory,
    onMessage: (payload)=>messages.push(payload),
    onSystem: (payload)=>systems.push(payload),
    onClearMessage: (payload)=>clearedMessages.push(payload),
    onClearChat: (payload)=>clearedChats.push(payload),
  });
  client.setChannel('channel');
  client.setEnabled(true);
  const socket = sockets.sockets[0];
  socket.open();

  socket.message(
    '@display-name=Foo :foo!foo@foo PRIVMSG #other :ignored\r\n' +
    '@id=action;display-name=Foo :foo!foo@foo PRIVMSG #channel :\u0001ACTION waves\u0001\r\n' +
    '@target-msg-id=gone :tmi.twitch.tv CLEARMSG #channel :deleted\r\n' +
    '@target-user-id=user-1 :tmi.twitch.tv CLEARCHAT #channel :foo\r\n' +
    '@system-msg=Foo\\ssubscribed! :tmi.twitch.tv USERNOTICE #channel\r\n'
  );

  assert.equal(messages.length, 1);
  assert.equal(messages[0].text, 'waves');
  assert.equal(messages[0].isAction, true);
  assert.deepEqual(clearedMessages, [{ messageId: 'gone' }]);
  assert.deepEqual(clearedChats, [{ userId: 'user-1', username: 'foo' }]);
  assert.deepEqual(systems, [{ text: 'Foo subscribed!' }]);
});

test('unexpected closes use capped backoff and hiding cancels the pending retry', ()=>{
  const sockets = fakeSockets();
  const timers = fakeTimers();
  const client = new TwitchIRCClient({
    socketFactory: sockets.factory,
    setTimer: timers.setTimer,
    clearTimer: timers.clearTimer,
  });
  client.setChannel('channel');
  client.setEnabled(true);

  const expectedDelays = [1000, 2000, 4000, 8000, 16000, 30000, 30000];
  for(let index = 0; index < expectedDelays.length; index++){
    sockets.sockets[index].serverClose();
    assert.equal(timers.pending()[0].delay, expectedDelays[index]);
    timers.runNext();
  }

  sockets.sockets.at(-1).serverClose();
  assert.equal(timers.pending().length, 1);
  client.setEnabled(false);
  assert.equal(timers.pending().length, 0);
  assert.equal(sockets.sockets.length, expectedDelays.length + 1);
});

test('socket errors enter backoff even if the browser does not emit close', ()=>{
  const sockets = fakeSockets();
  const timers = fakeTimers();
  const client = new TwitchIRCClient({
    socketFactory: sockets.factory,
    setTimer: timers.setTimer,
    clearTimer: timers.clearTimer,
  });
  client.setChannel('channel');
  client.setEnabled(true);
  sockets.sockets[0].emit('error');

  assert.equal(sockets.sockets[0].closeCalls.length, 1);
  assert.equal(timers.pending()[0].delay, 1000);
});

test('channel changes close the old socket, send PART, and ignore stale messages', ()=>{
  const sockets = fakeSockets();
  const received = [];
  const client = new TwitchIRCClient({
    socketFactory: sockets.factory,
    onMessage: (payload)=>received.push(payload),
  });
  client.setChannel('first');
  client.setEnabled(true);
  const first = sockets.sockets[0];
  first.open();

  client.setChannel('second');
  assert.equal(first.sent.at(-1), 'PART #first\r\n');
  assert.equal(first.closeCalls.length, 1);
  assert.equal(sockets.sockets.length, 2);

  first.message(':foo!foo@foo PRIVMSG #first :stale\r\n');
  assert.deepEqual(received, []);

  const second = sockets.sockets[1];
  second.open();
  assert.equal(second.sent.at(-1), 'JOIN #second\r\n');
});

test('RECONNECT restarts immediately while authentication failures stop retries', ()=>{
  const sockets = fakeSockets();
  const timers = fakeTimers();
  const states = [];
  const client = new TwitchIRCClient({
    socketFactory: sockets.factory,
    setTimer: timers.setTimer,
    clearTimer: timers.clearTimer,
    onState: (state)=>states.push(state),
  });
  client.setChannel('channel');
  client.setEnabled(true);
  const first = sockets.sockets[0];
  first.open();

  first.message(':tmi.twitch.tv RECONNECT\r\n');
  assert.equal(timers.pending()[0].delay, 0);
  timers.runNext();
  assert.equal(sockets.sockets.length, 2);

  const second = sockets.sockets[1];
  second.open();
  second.message(':tmi.twitch.tv NOTICE * :Login authentication failed\r\n');
  assert.equal(states.at(-1).state, 'unavailable');
  assert.equal(timers.pending().length, 0);
  second.serverClose();
  assert.equal(timers.pending().length, 0);
});

test('visibility defaults hidden and persists explicit choices', ()=>{
  const values = new Map();
  const storage = {
    getItem(key){ return values.get(key) ?? null; },
    setItem(key, value){ values.set(key, value); },
  };

  assert.equal(readChatVisibility(storage), false);
  writeChatVisibility(storage, true);
  assert.equal(readChatVisibility(storage), true);
  writeChatVisibility(storage, false);
  assert.equal(readChatVisibility(storage), false);

  const blockedStorage = {
    getItem(){ throw new Error('blocked'); },
    setItem(){ throw new Error('blocked'); },
  };
  assert.equal(readChatVisibility(blockedStorage), false);
  assert.doesNotThrow(()=>writeChatVisibility(blockedStorage, true));
});

test('UI controller stays disconnected while hidden and remembers toggle changes', (t)=>{
  const previousWindow = globalThis.window;
  const windowListeners = new Map();
  globalThis.window = {
    addEventListener(type, listener){ windowListeners.set(type, listener); },
    removeEventListener(type){ windowListeners.delete(type); },
  };

  const documentRef = new FakeDocument();
  const toggleEl = new FakeElement(documentRef);
  const toggleLabelEl = new FakeElement(documentRef);
  const panelEl = new FakeElement(documentRef);
  const messagesEl = new FakeElement(documentRef);
  const emptyEl = new FakeElement(documentRef);
  const statusEl = new FakeElement(documentRef);
  const channelEl = new FakeElement(documentRef);
  const newMessagesEl = new FakeElement(documentRef);
  const layoutEl = new FakeElement(documentRef);
  const sockets = fakeSockets();
  const values = new Map();
  const storage = {
    getItem(key){ return values.get(key) ?? null; },
    setItem(key, value){ values.set(key, value); },
  };

  const controller = initTwitchChat({
    toggleEl,
    toggleLabelEl,
    panelEl,
    messagesEl,
    emptyEl,
    statusEl,
    channelEl,
    newMessagesEl,
    layoutEl,
    storage,
    socketFactory: sockets.factory,
  });
  t.after(()=>{
    controller.destroy();
    if(previousWindow === undefined) delete globalThis.window;
    else globalThis.window = previousWindow;
  });

  assert.equal(panelEl.hidden, true);
  assert.equal(toggleEl.getAttribute('aria-pressed'), 'false');
  controller.setChannel('channel');
  assert.equal(sockets.sockets.length, 0, 'selecting a channel must not connect hidden chat');

  toggleEl.click();
  assert.equal(panelEl.hidden, false);
  assert.equal(toggleEl.getAttribute('aria-pressed'), 'true');
  assert.equal(readChatVisibility(storage), true);
  assert.equal(sockets.sockets.length, 1);

  toggleEl.click();
  assert.equal(panelEl.hidden, true);
  assert.equal(readChatVisibility(storage), false);
  assert.equal(sockets.sockets[0].closeCalls.length, 1);
});
