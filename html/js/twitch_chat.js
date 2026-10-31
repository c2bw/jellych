const IRC_URL = 'wss://irc-ws.chat.twitch.tv:443';
const CHAT_VISIBILITY_KEY = 'jellych_chat_visible';
const CHAT_AUTH_KEY = 'jellych_twitch_chat_identity';
const RECONNECT_DELAYS_MS = [1000, 2000, 4000, 8000, 16000, 30000];
const MAX_CHAT_ROWS = 100;
const MAX_CHAT_MESSAGE_BYTES = 450;
const SCROLL_FOLLOW_THRESHOLD_PX = 48;
const CHANNEL_PATTERN = /^[a-z0-9_]{1,25}$/;
const COLOR_PATTERN = /^#[0-9a-f]{6}$/i;
const EMOTE_ID_PATTERN = /^\d+$/;
const IRC_TAG_REPLACEMENTS = {
  s: ' ',
  n: '\n',
  r: '\r',
  ':': ';',
  '\\': '\\',
};
const FALLBACK_COLORS = [
  '#ff7f50',
  '#9acd32',
  '#00b5ad',
  '#5f9ea0',
  '#6495ed',
  '#da70d6',
  '#d2691e',
  '#b69dff',
  '#48d597',
  '#f472b6',
];

function defaultSocketFactory(url){
  return new WebSocket(url);
}

function defaultSetTimer(callback, delay){
  return setTimeout(callback, delay);
}

function defaultClearTimer(timer){
  clearTimeout(timer);
}

function safeCallback(callback, payload){
  if(typeof callback === 'function') callback(payload);
}

function socketCanSend(socket){
  return socket && socket.readyState === 1;
}

function normalizeChannel(channel){
  const normalized = String(channel || '').trim().replace(/^#/, '').toLowerCase();
  return CHANNEL_PATTERN.test(normalized) ? normalized : '';
}

function normalizeIdentity(identity){
  const login = normalizeChannel(identity && identity.login);
  const accessToken = String(identity && identity.accessToken || '').trim();
  const expiresAt = Number(identity && identity.expiresAt);
  if(!login || !accessToken || accessToken.length > 512 || /[\s\x00-\x1f\x7f]/.test(accessToken)) return null;
  if(!Number.isFinite(expiresAt) || expiresAt <= Date.now()) return null;
  return { login, accessToken, expiresAt };
}

function usernameFromPrefix(prefix){
  const match = String(prefix || '').match(/^([^!@\s]+)/);
  return match ? match[1] : '';
}

function channelFromParam(param){
  return normalizeChannel(String(param || '').replace(/^#/, ''));
}

export function unescapeIRCTag(value){
  const source = String(value || '');
  let result = '';
  for(let index = 0; index < source.length; index++){
    const character = source[index];
    if(character !== '\\' || index + 1 >= source.length){
      result += character;
      continue;
    }
    const escaped = source[index + 1];
    if(Object.prototype.hasOwnProperty.call(IRC_TAG_REPLACEMENTS, escaped)){
      result += IRC_TAG_REPLACEMENTS[escaped];
      index++;
    } else {
      result += character;
    }
  }
  return result;
}

export function parseIRCLine(rawLine){
  const raw = String(rawLine || '').replace(/[\r\n]+$/, '');
  if(!raw) return null;

  let cursor = raw;
  const tags = {};
  let prefix = '';

  if(cursor.startsWith('@')){
    const end = cursor.indexOf(' ');
    if(end < 0) return null;
    const tagSource = cursor.slice(1, end);
    cursor = cursor.slice(end + 1).replace(/^ +/, '');
    for(const entry of tagSource.split(';')){
      if(!entry) continue;
      const separator = entry.indexOf('=');
      const key = separator < 0 ? entry : entry.slice(0, separator);
      const value = separator < 0 ? '' : entry.slice(separator + 1);
      if(key) tags[key] = unescapeIRCTag(value);
    }
  }

  if(cursor.startsWith(':')){
    const end = cursor.indexOf(' ');
    if(end < 0) return null;
    prefix = cursor.slice(1, end);
    cursor = cursor.slice(end + 1).replace(/^ +/, '');
  }

  let trailing = '';
  const trailingIndex = cursor.indexOf(' :');
  if(trailingIndex >= 0){
    trailing = cursor.slice(trailingIndex + 2);
    cursor = cursor.slice(0, trailingIndex);
  } else if(cursor.startsWith(':')){
    trailing = cursor.slice(1);
    cursor = '';
  }

  const parts = cursor.split(/ +/).filter(Boolean);
  const command = (parts.shift() || '').toUpperCase();
  if(!command) return null;

  return {
    raw,
    tags,
    prefix,
    command,
    params: parts,
    trailing,
  };
}

export function tokenizeChatMessage(message, emoteTag){
  const text = String(message || '');
  const characters = Array.from(text);
  const ranges = [];

  for(const group of String(emoteTag || '').split('/')){
    if(!group) continue;
    const separator = group.indexOf(':');
    if(separator < 1) continue;
    const id = group.slice(0, separator);
    if(!EMOTE_ID_PATTERN.test(id)) continue;
    for(const rawRange of group.slice(separator + 1).split(',')){
      const match = rawRange.match(/^(\d+)-(\d+)$/);
      if(!match) continue;
      const start = Number(match[1]);
      const end = Number(match[2]);
      if(!Number.isSafeInteger(start) || !Number.isSafeInteger(end)) continue;
      if(start < 0 || end < start || end >= characters.length) continue;
      ranges.push({ id, start, end });
    }
  }

  ranges.sort((left, right)=>left.start - right.start || left.end - right.end);
  const tokens = [];
  let offset = 0;
  for(const range of ranges){
    if(range.start < offset) continue;
    if(range.start > offset){
      tokens.push({ type: 'text', text: characters.slice(offset, range.start).join('') });
    }
    tokens.push({
      type: 'emote',
      id: range.id,
      text: characters.slice(range.start, range.end + 1).join(''),
    });
    offset = range.end + 1;
  }
  if(offset < characters.length){
    tokens.push({ type: 'text', text: characters.slice(offset).join('') });
  }
  if(!tokens.length) tokens.push({ type: 'text', text });
  return tokens;
}

export function resolveUsernameColor(username, requestedColor){
  if(COLOR_PATTERN.test(String(requestedColor || ''))) return requestedColor;
  let hash = 0;
  for(const character of String(username || '')){
    hash = ((hash * 31) + character.codePointAt(0)) >>> 0;
  }
  return FALLBACK_COLORS[hash % FALLBACK_COLORS.length];
}

export function readChatVisibility(storage){
  try{
    return storage && storage.getItem(CHAT_VISIBILITY_KEY) === 'true';
  }catch(error){
    return false;
  }
}

export function writeChatVisibility(storage, visible){
  try{
    if(storage) storage.setItem(CHAT_VISIBILITY_KEY, visible ? 'true' : 'false');
  }catch(error){ /* local storage can be unavailable in privacy modes */ }
}

export function readChatIdentity(storage, now = Date.now()){
  try{
    const parsed = JSON.parse(storage && storage.getItem(CHAT_AUTH_KEY) || 'null');
    const identity = normalizeIdentity(parsed);
    if(!identity || identity.expiresAt <= now + 30000) return null;
    return identity;
  }catch(error){
    return null;
  }
}

export function writeChatIdentity(storage, identity){
  const normalized = normalizeIdentity(identity);
  try{
    if(!storage) return false;
    if(!normalized){
      storage.removeItem(CHAT_AUTH_KEY);
      return false;
    }
    storage.setItem(CHAT_AUTH_KEY, JSON.stringify(normalized));
    return true;
  }catch(error){
    return false;
  }
}

export function clearChatIdentity(storage){
  try{
    if(storage) storage.removeItem(CHAT_AUTH_KEY);
  }catch(error){ /* session storage can be unavailable */ }
}

export function chatIdentityFromOAuthPayload(payload, now = Date.now()){
  const expiresIn = Number(payload && payload.expiresIn);
  if(!Number.isFinite(expiresIn) || expiresIn <= 0 || expiresIn > 31 * 24 * 60 * 60) return null;
  return normalizeIdentity({
    accessToken: payload && payload.accessToken,
    login: payload && payload.login,
    expiresAt: now + Math.floor(expiresIn * 1000),
  });
}

export class TwitchIRCClient {
  constructor(options = {}){
    this.socketFactory = options.socketFactory || defaultSocketFactory;
    this.setTimer = options.setTimer || defaultSetTimer;
    this.clearTimer = options.clearTimer || defaultClearTimer;
    this.random = options.random || Math.random;
    this.onState = options.onState;
    this.onMessage = options.onMessage;
    this.onClearMessage = options.onClearMessage;
    this.onClearChat = options.onClearChat;
    this.onSystem = options.onSystem;
    this.onAuthenticationFailed = options.onAuthenticationFailed;
    this.endpoint = options.endpoint || IRC_URL;
    const identity = normalizeIdentity(options.identity);

    this.channel = '';
    this.enabled = false;
    this.destroyed = false;
    this.socket = null;
    this.retryTimer = null;
    this.retryIndex = 0;
    this.generation = 0;
    this.buffer = '';
    this.authenticationFailed = false;
    this.metadataAvailable = true;
    this.state = 'idle';
    this.login = identity ? identity.login : '';
    this.accessToken = identity ? identity.accessToken : '';
    this.expiresAt = identity ? identity.expiresAt : 0;
    this.localMessageSequence = 0;
  }

  setState(state, details = {}){
    this.state = state;
    safeCallback(this.onState, {
      state,
      channel: this.channel,
      authenticated: this.isAuthenticated(),
      login: this.login,
      ...details,
    });
  }

  isAuthenticated(){
    return !!(this.login && this.accessToken && this.expiresAt > Date.now());
  }

  setIdentity(identity){
    const normalized = normalizeIdentity(identity);
    const nextLogin = normalized ? normalized.login : '';
    const nextToken = normalized ? normalized.accessToken : '';
    const nextExpiresAt = normalized ? normalized.expiresAt : 0;
    if(nextLogin === this.login && nextToken === this.accessToken && nextExpiresAt === this.expiresAt) return;

    this.cancelConnection(true);
    this.login = nextLogin;
    this.accessToken = nextToken;
    this.expiresAt = nextExpiresAt;
    this.authenticationFailed = false;
    this.retryIndex = 0;
    if(this.enabled && this.channel) this.connectNow(false);
    else this.setState('idle');
  }

  setChannel(channel){
    const normalized = normalizeChannel(channel);
    if(normalized === this.channel) return;
    this.cancelConnection(true);
    this.channel = normalized;
    this.retryIndex = 0;
    this.authenticationFailed = false;
    if(!this.enabled){
      this.setState('idle');
      return;
    }
    if(!this.channel){
      this.setState(channel ? 'unavailable' : 'idle');
      return;
    }
    this.connectNow(false);
  }

  setEnabled(enabled){
    const next = !!enabled;
    if(this.destroyed || next === this.enabled) return;
    this.enabled = next;
    if(!next){
      this.authenticationFailed = false;
      this.cancelConnection(true);
      this.setState('idle');
      return;
    }
    if(!this.channel){
      this.setState('idle');
      return;
    }
    this.authenticationFailed = false;
    this.retryIndex = 0;
    this.connectNow(false);
  }

  connectNow(isRetry){
    if(this.destroyed || !this.enabled || !this.channel || this.authenticationFailed) return;
    this.clearRetry();
    this.cancelSocket(false);
    const generation = ++this.generation;
    this.buffer = '';
    this.metadataAvailable = true;
    this.setState(isRetry ? 'reconnecting' : 'connecting');

    let socket;
    try{
      socket = this.socketFactory(this.endpoint);
    }catch(error){
      this.scheduleReconnect();
      return;
    }
    this.socket = socket;

    socket.addEventListener('open', ()=>{
      if(!this.isCurrentSocket(socket, generation)) return;
      const authenticated = this.isAuthenticated();
      const suffix = Math.floor(10000 + this.random() * 90000);
      const nick = authenticated ? this.login : 'justinfan' + suffix;
      const password = authenticated ? 'oauth:' + this.accessToken : 'blah';
      try{
        socket.send('PASS ' + password + '\r\n');
        socket.send('NICK ' + nick + '\r\n');
        socket.send('CAP REQ :twitch.tv/tags twitch.tv/commands\r\n');
        socket.send('JOIN #' + this.channel + '\r\n');
      }catch(error){
        this.restartConnection();
      }
    });

    socket.addEventListener('message', (event)=>{
      if(!this.isCurrentSocket(socket, generation)) return;
      this.handleSocketData(String(event && event.data || ''), socket);
    });

    socket.addEventListener('close', ()=>{
      if(!this.isCurrentSocket(socket, generation)) return;
      this.socket = null;
      if(this.enabled && this.channel && !this.destroyed && !this.authenticationFailed){
        this.scheduleReconnect();
      }
    });

    socket.addEventListener('error', ()=>{
      if(!this.isCurrentSocket(socket, generation)) return;
      this.cancelSocket(false);
      this.scheduleReconnect();
    });
  }

  isCurrentSocket(socket, generation){
    return !this.destroyed && this.socket === socket && this.generation === generation;
  }

  handleSocketData(data, socket){
    this.buffer += data;
    const lines = this.buffer.split('\r\n');
    this.buffer = lines.pop() || '';
    for(const line of lines){
      const message = parseIRCLine(line);
      if(message) this.handleIRCMessage(message, socket);
    }
  }

  handleIRCMessage(message, socket){
    if(message.command === 'PING'){
      const payload = message.trailing || message.params.join(' ');
      if(socketCanSend(socket)){
        try{ socket.send('PONG :' + payload + '\r\n'); }catch(error){ /* close will retry */ }
      }
      return;
    }

    if(message.command === '001'){
      this.retryIndex = 0;
      this.setState('connected');
      return;
    }

    if(message.command === 'RECONNECT'){
      this.restartConnection();
      return;
    }

    if(message.command === 'CAP' && message.params.includes('NAK')){
      this.metadataAvailable = false;
      safeCallback(this.onSystem, { text: 'Enhanced chat metadata is unavailable; showing plain text.' });
      return;
    }

    if(message.command === 'NOTICE' && /(login authentication failed|improperly formatted auth)/i.test(message.trailing)){
      const authenticated = this.isAuthenticated();
      this.authenticationFailed = true;
      this.setState('unavailable', { reason: 'authentication' });
      this.cancelConnection(false);
      safeCallback(this.onAuthenticationFailed, { authenticated, login: this.login });
      return;
    }

    if(message.command === 'PRIVMSG'){
      if(channelFromParam(message.params[0]) !== this.channel) return;
      let text = message.trailing;
      let isAction = false;
      if(text.startsWith('\u0001ACTION ') && text.endsWith('\u0001')){
        text = text.slice(8, -1);
        isAction = true;
      }
      const username = usernameFromPrefix(message.prefix);
      safeCallback(this.onMessage, {
        id: message.tags.id || '',
        userId: message.tags['user-id'] || '',
        username,
        displayName: message.tags['display-name'] || username,
        color: message.tags.color || '',
        text,
        isAction,
        emotes: this.metadataAvailable ? message.tags.emotes || '' : '',
        timestamp: Number(message.tags['tmi-sent-ts']) || Date.now(),
      });
      return;
    }

    if(message.command === 'CLEARMSG'){
      if(channelFromParam(message.params[0]) !== this.channel) return;
      safeCallback(this.onClearMessage, { messageId: message.tags['target-msg-id'] || '' });
      return;
    }

    if(message.command === 'CLEARCHAT'){
      if(channelFromParam(message.params[0]) !== this.channel) return;
      safeCallback(this.onClearChat, {
        userId: message.tags['target-user-id'] || '',
        username: message.trailing || '',
      });
      return;
    }

    if(message.command === 'NOTICE' || message.command === 'USERNOTICE'){
      const target = message.params[0];
      if(target && target !== '*' && channelFromParam(target) !== this.channel) return;
      const text = message.tags['system-msg'] || message.trailing;
      if(text) safeCallback(this.onSystem, { text });
    }
  }

  restartConnection(){
    if(!this.enabled || !this.channel || this.destroyed) return;
    this.cancelSocket(true);
    this.setState('reconnecting', { delayMs: 0 });
    this.retryTimer = this.setTimer(()=>{
      this.retryTimer = null;
      this.connectNow(true);
    }, 0);
  }

  scheduleReconnect(){
    if(this.retryTimer || !this.enabled || !this.channel || this.destroyed || this.authenticationFailed) return;
    const delayIndex = Math.min(this.retryIndex, RECONNECT_DELAYS_MS.length - 1);
    const delayMs = RECONNECT_DELAYS_MS[delayIndex];
    this.retryIndex++;
    this.setState('reconnecting', { delayMs });
    this.retryTimer = this.setTimer(()=>{
      this.retryTimer = null;
      this.connectNow(true);
    }, delayMs);
  }

  clearRetry(){
    if(this.retryTimer == null) return;
    this.clearTimer(this.retryTimer);
    this.retryTimer = null;
  }

  sendMessage(message){
    if(!this.isAuthenticated()) return { ok: false, error: 'login-required' };
    if(this.state !== 'connected' || !socketCanSend(this.socket) || !this.channel){
      return { ok: false, error: 'not-connected' };
    }
    const text = String(message || '').replace(/[\r\n\x00]+/g, ' ').trim();
    if(!text) return { ok: false, error: 'empty' };
    const byteLength = new TextEncoder().encode(text).byteLength;
    if(byteLength > MAX_CHAT_MESSAGE_BYTES) return { ok: false, error: 'too-long' };
    try{
      this.socket.send('PRIVMSG #' + this.channel + ' :' + text + '\r\n');
      const actionMatch = text.match(/^\/me\s+(.+)$/i);
      safeCallback(this.onMessage, {
        id: 'local-' + Date.now().toString(36) + '-' + (++this.localMessageSequence),
        userId: '',
        username: this.login,
        displayName: this.login,
        color: '',
        text: actionMatch ? actionMatch[1] : text,
        isAction: !!actionMatch,
        emotes: '',
        timestamp: Date.now(),
        local: true,
      });
      return { ok: true };
    }catch(error){
      return { ok: false, error: 'send-failed' };
    }
  }

  cancelSocket(sendPart){
    const socket = this.socket;
    this.socket = null;
    this.buffer = '';
    this.generation++;
    if(!socket) return;
    if(sendPart && socketCanSend(socket) && this.channel){
      try{ socket.send('PART #' + this.channel + '\r\n'); }catch(error){ /* close below */ }
    }
    try{ socket.close(1000, 'chat disabled'); }catch(error){ /* already closed */ }
  }

  cancelConnection(sendPart){
    this.clearRetry();
    this.cancelSocket(sendPart);
  }

  destroy(){
    if(this.destroyed) return;
    this.destroyed = true;
    this.enabled = false;
    this.cancelConnection(true);
  }
}

function isNearBottom(element){
  return element.scrollHeight - element.scrollTop - element.clientHeight <= SCROLL_FOLLOW_THRESHOLD_PX;
}

function setStatus(statusEl, state, delayMs){
  if(!statusEl) return;
  const labels = {
    idle: 'Idle',
    connecting: 'Connecting…',
    connected: 'Connected',
    reconnecting: delayMs > 0 ? 'Retrying in ' + Math.ceil(delayMs / 1000) + 's' : 'Reconnecting…',
    unavailable: 'Unavailable',
  };
  statusEl.textContent = labels[state] || 'Idle';
  statusEl.dataset.state = state;
}

function createMessageBody(documentRef, payload){
  const body = documentRef.createElement('span');
  body.className = 'chat-message-body';
  for(const token of tokenizeChatMessage(payload.text, payload.emotes)){
    if(token.type === 'text'){
      body.appendChild(documentRef.createTextNode(token.text));
      continue;
    }
    const image = documentRef.createElement('img');
    image.className = 'chat-emote';
    image.src = 'https://static-cdn.jtvnw.net/emoticons/v2/' + token.id + '/default/dark/1.0';
    image.alt = token.text;
    image.loading = 'lazy';
    image.decoding = 'async';
    body.appendChild(image);
  }
  return body;
}

function insertAfterHeader(paneEl, headerEl, authBarEl){
  if(!paneEl || !authBarEl) return;
  if(typeof paneEl.insertBefore === 'function' && headerEl && headerEl.parentNode === paneEl){
    if(authBarEl.parentNode !== paneEl || headerEl.nextSibling !== authBarEl){
      paneEl.insertBefore(authBarEl, headerEl.nextSibling || null);
    }
    return;
  }
  if(Array.isArray(paneEl.children)){
    if(authBarEl.parentNode && typeof authBarEl.remove === 'function') authBarEl.remove();
    const headerIdx = paneEl.children.indexOf(headerEl);
    authBarEl.parentNode = paneEl;
    if(headerIdx >= 0) paneEl.children.splice(headerIdx + 1, 0, authBarEl);
    else paneEl.children.unshift(authBarEl);
  }
}

function insertBeforeStatus(headerActionsEl, statusEl, multiToggleEl){
  if(!headerActionsEl || !multiToggleEl) return;
  if(typeof headerActionsEl.insertBefore === 'function' && statusEl && statusEl.parentNode === headerActionsEl){
    if(multiToggleEl.parentNode !== headerActionsEl || multiToggleEl.nextSibling !== statusEl){
      headerActionsEl.insertBefore(multiToggleEl, statusEl);
    }
    return;
  }
  if(Array.isArray(headerActionsEl.children)){
    if(multiToggleEl.parentNode && typeof multiToggleEl.remove === 'function') multiToggleEl.remove();
    const statusIdx = headerActionsEl.children.indexOf(statusEl);
    multiToggleEl.parentNode = headerActionsEl;
    if(statusIdx >= 0) headerActionsEl.children.splice(statusIdx, 0, multiToggleEl);
    else headerActionsEl.children.unshift(multiToggleEl);
  }
}

function createDynamicChatPaneDOM(documentRef){
  const paneEl = documentRef.createElement('div');
  paneEl.className = 'chat-pane';

  const headerEl = documentRef.createElement('header');
  headerEl.className = 'chat-header';
  if(headerEl.setAttribute) headerEl.setAttribute('data-chat', 'header');

  const headerTitleWrap = documentRef.createElement('div');
  const eyebrowEl = documentRef.createElement('span');
  eyebrowEl.className = 'eyebrow';
  eyebrowEl.textContent = 'Twitch IRC';
  const channelEl = documentRef.createElement('h2');
  channelEl.textContent = 'Chat';
  if(channelEl.setAttribute) channelEl.setAttribute('data-chat', 'channel');
  headerTitleWrap.appendChild(eyebrowEl);
  headerTitleWrap.appendChild(channelEl);

  const headerActionsEl = documentRef.createElement('div');
  headerActionsEl.className = 'chat-header-actions';
  if(headerActionsEl.setAttribute) headerActionsEl.setAttribute('data-chat', 'header-actions');

  const statusEl = documentRef.createElement('span');
  statusEl.className = 'chat-status';
  statusEl.textContent = 'Idle';
  if(statusEl.dataset) statusEl.dataset.state = 'idle';
  if(statusEl.setAttribute){
    statusEl.setAttribute('data-chat', 'status');
    statusEl.setAttribute('data-state', 'idle');
    statusEl.setAttribute('aria-live', 'polite');
  }
  headerActionsEl.appendChild(statusEl);

  headerEl.appendChild(headerTitleWrap);
  headerEl.appendChild(headerActionsEl);

  const feedEl = documentRef.createElement('div');
  feedEl.className = 'chat-feed';

  const messagesEl = documentRef.createElement('div');
  messagesEl.className = 'chat-messages';
  if(messagesEl.setAttribute){
    messagesEl.setAttribute('data-chat', 'messages');
    messagesEl.setAttribute('role', 'log');
    messagesEl.setAttribute('aria-label', 'Twitch chat messages');
  }

  const emptyEl = documentRef.createElement('div');
  emptyEl.className = 'chat-empty';
  emptyEl.textContent = 'Select a live channel to view chat';
  if(emptyEl.setAttribute) emptyEl.setAttribute('data-chat', 'empty');

  const newMessagesEl = documentRef.createElement('button');
  newMessagesEl.className = 'chat-new-messages hidden';
  if(newMessagesEl.classList) newMessagesEl.classList.add('chat-new-messages', 'hidden');
  newMessagesEl.type = 'button';
  newMessagesEl.textContent = 'New messages';
  if(newMessagesEl.setAttribute) newMessagesEl.setAttribute('data-chat', 'new-messages');

  feedEl.appendChild(messagesEl);
  feedEl.appendChild(emptyEl);
  feedEl.appendChild(newMessagesEl);

  const composerEl = documentRef.createElement('form');
  composerEl.className = 'chat-composer is-disabled';
  if(composerEl.classList) composerEl.classList.add('chat-composer', 'is-disabled');
  if(composerEl.setAttribute){
    composerEl.setAttribute('data-chat', 'composer');
    composerEl.setAttribute('autocomplete', 'off');
  }

  const inputEl = documentRef.createElement('input');
  inputEl.type = 'text';
  inputEl.maxLength = 450;
  inputEl.placeholder = 'Log in to send messages';
  inputEl.disabled = true;
  if(inputEl.setAttribute){
    inputEl.setAttribute('data-chat', 'input');
    inputEl.setAttribute('aria-label', 'Chat message');
  }

  const sendButtonEl = documentRef.createElement('button');
  sendButtonEl.type = 'submit';
  sendButtonEl.disabled = true;
  sendButtonEl.textContent = 'Send';
  if(sendButtonEl.setAttribute) sendButtonEl.setAttribute('data-chat', 'send-button');

  composerEl.appendChild(inputEl);
  composerEl.appendChild(sendButtonEl);

  paneEl.appendChild(headerEl);
  paneEl.appendChild(feedEl);
  paneEl.appendChild(composerEl);

  return {
    paneEl,
    headerEl,
    headerActionsEl,
    channelEl,
    statusEl,
    messagesEl,
    emptyEl,
    newMessagesEl,
    composerEl,
    inputEl,
    sendButtonEl,
  };
}

export function initTwitchChat({
  toggleEl,
  toggleLabelEl,
  panelEl,
  stackEl,
  primaryPaneEl,
  authBarEl,
  multiToggleEl,
  messagesEl,
  emptyEl,
  statusEl,
  channelEl,
  newMessagesEl,
  layoutEl,
  authButtonEl,
  identityEl,
  composerEl,
  inputEl,
  sendButtonEl,
  storage,
  authStorage,
  socketFactory,
  openOAuthWindow,
}){
  const documentRef = panelEl.ownerDocument;
  let storageRef = storage;
  if(storageRef === undefined){
    try{ storageRef = window.localStorage; }catch(error){ storageRef = null; }
  }
  let authStorageRef = authStorage;
  if(authStorageRef === undefined){
    try{ authStorageRef = window.sessionStorage; }catch(error){ authStorageRef = null; }
  }

  const queryInPanel = (selector)=>
    (panelEl && typeof panelEl.querySelector === 'function') ? panelEl.querySelector(selector) : null;

  const resolvedStackEl = stackEl || queryInPanel('#chatStack') || panelEl;
  const resolvedPrimaryPaneEl = primaryPaneEl || queryInPanel('#chatPrimaryPane') || null;
  const resolvedAuthBarEl = authBarEl || queryInPanel('#chatAuthBar') || queryInPanel('[data-chat="auth-bar"]') || null;
  const resolvedMultiToggleEl = multiToggleEl || queryInPanel('#chatMultiToggle') || null;
  const primaryHeaderEl = (resolvedPrimaryPaneEl && typeof resolvedPrimaryPaneEl.querySelector === 'function' && resolvedPrimaryPaneEl.querySelector('[data-chat="header"]')) || queryInPanel('[data-chat="header"]') || null;
  const primaryHeaderActionsEl = (resolvedPrimaryPaneEl && typeof resolvedPrimaryPaneEl.querySelector === 'function' && resolvedPrimaryPaneEl.querySelector('[data-chat="header-actions"]')) || queryInPanel('[data-chat="header-actions"]') || null;

  let currentIdentity = readChatIdentity(authStorageRef);
  let visible = readChatVisibility(storageRef);
  let streamMultiMode = false;
  let chatMultiMode = false;
  let openChannels = [];
  let focusedChannel = '';
  let destroyed = false;
  let authExpiryTimer = null;
  let oauthPopup = null;

  const activePanes = new Map();
  let paneOrder = [];
  let primaryPaneOwner = null;

  function isAuthenticated(){
    return !!(currentIdentity && currentIdentity.login && currentIdentity.accessToken && currentIdentity.expiresAt > Date.now());
  }

  function updateGlobalAuthHeader(){
    const authenticated = isAuthenticated();
    if(identityEl) identityEl.textContent = authenticated ? '@' + currentIdentity.login : 'Anonymous · read only';
    if(authButtonEl){
      authButtonEl.textContent = authenticated ? 'Log out' : 'Log in';
      authButtonEl.setAttribute('aria-label', authenticated ? 'Log out of Twitch chat' : 'Log in to Twitch chat');
    }
  }

  function createPaneInstance({
    isPrimary = false,
    paneEl = null,
    headerEl = null,
    headerActionsEl = null,
    paneChannelEl,
    paneStatusEl,
    paneMessagesEl,
    paneEmptyEl,
    paneNewMessagesEl,
    paneComposerEl,
    paneInputEl,
    paneSendButtonEl,
  }){
    let currentChannel = '';
    let unreadCount = 0;
    let client;

    function resetUnread(){
      unreadCount = 0;
      if(paneNewMessagesEl){
        paneNewMessagesEl.textContent = 'New messages';
        if(paneNewMessagesEl.classList) paneNewMessagesEl.classList.add('hidden');
      }
    }

    function updateEmptyState(){
      if(!paneEmptyEl || !paneMessagesEl) return;
      const hasMessages = paneMessagesEl.childElementCount > 0;
      if(paneEmptyEl.classList) paneEmptyEl.classList.toggle('hidden', hasMessages);
      if(hasMessages) return;
      if(!currentChannel) paneEmptyEl.textContent = 'Select a live channel to view chat';
      else if(client && client.state === 'connected') paneEmptyEl.textContent = 'Waiting for chat messages…';
      else if(client && client.state === 'unavailable') paneEmptyEl.textContent = 'Twitch chat is unavailable';
      else paneEmptyEl.textContent = 'Connecting to #' + currentChannel + '…';
    }

    function clearMessages(){
      if(paneMessagesEl) paneMessagesEl.textContent = '';
      resetUnread();
      updateEmptyState();
    }

    function appendRow(row){
      if(!paneMessagesEl) return;
      const follow = isNearBottom(paneMessagesEl);
      paneMessagesEl.appendChild(row);
      while(paneMessagesEl.childElementCount > MAX_CHAT_ROWS){
        paneMessagesEl.firstElementChild.remove();
      }
      updateEmptyState();
      if(follow){
        resetUnread();
        if(typeof requestAnimationFrame === 'function'){
          requestAnimationFrame(()=>{ paneMessagesEl.scrollTop = paneMessagesEl.scrollHeight; });
        } else {
          paneMessagesEl.scrollTop = paneMessagesEl.scrollHeight;
        }
      } else if(paneNewMessagesEl){
        unreadCount++;
        paneNewMessagesEl.textContent = unreadCount === 1 ? '1 new message' : unreadCount + ' new messages';
        if(paneNewMessagesEl.classList) paneNewMessagesEl.classList.remove('hidden');
      }
    }

    function appendMessage(payload){
      const row = documentRef.createElement('div');
      row.className = payload.isAction ? 'chat-message is-action' : 'chat-message';
      if(payload.id) row.dataset.messageId = payload.id;
      if(payload.userId) row.dataset.userId = payload.userId;
      if(payload.username) row.dataset.username = payload.username.toLowerCase();

      const name = documentRef.createElement('span');
      name.className = 'chat-message-name';
      name.textContent = payload.displayName || payload.username || 'unknown';
      if(!name.style) name.style = {};
      name.style.color = resolveUsernameColor(payload.username, payload.color);

      row.appendChild(name);
      row.appendChild(documentRef.createTextNode(payload.isAction ? ' ' : ': '));
      row.appendChild(createMessageBody(documentRef, payload));
      appendRow(row);
    }

    function appendSystem({ text }){
      if(!text) return;
      const row = documentRef.createElement('div');
      row.className = 'chat-system-message';
      row.textContent = text;
      appendRow(row);
    }

    function removeMatching(predicate){
      if(!paneMessagesEl) return;
      for(const child of Array.from(paneMessagesEl.children)){
        if(predicate(child)) child.remove();
      }
      updateEmptyState();
    }

    function updatePaneAuthUI(){
      const authenticated = !!(client && client.isAuthenticated());
      const connected = !!(client && client.state === 'connected');
      if(paneComposerEl && paneComposerEl.classList) paneComposerEl.classList.toggle('is-disabled', !authenticated);
      if(paneInputEl){
        paneInputEl.disabled = !authenticated || !connected || !currentChannel;
        paneInputEl.placeholder = authenticated ? (connected ? 'Send a message' : 'Connecting to chat…') : 'Log in to send messages';
      }
      if(paneSendButtonEl) paneSendButtonEl.disabled = !authenticated || !connected || !currentChannel;
    }

    function jumpToLatest(){
      if(paneMessagesEl) paneMessagesEl.scrollTop = paneMessagesEl.scrollHeight;
      resetUnread();
    }

    function handleComposerSubmit(event){
      event.preventDefault();
      const result = client.sendMessage(paneInputEl && paneInputEl.value);
      if(result.ok){
        paneInputEl.value = '';
        if(typeof paneInputEl.focus === 'function') paneInputEl.focus();
        return;
      }
      const errors = {
        'login-required': 'Log in to Twitch before sending messages.',
        'not-connected': 'Chat is not connected yet.',
        'empty': 'Enter a message first.',
        'too-long': 'That message is too long for Twitch chat.',
        'send-failed': 'The message could not be sent.',
      };
      appendSystem({ text: errors[result.error] || 'The message could not be sent.' });
    }

    client = new TwitchIRCClient({
      socketFactory,
      identity: currentIdentity,
      onState: ({ state, delayMs })=>{
        setStatus(paneStatusEl, state, delayMs);
        updateEmptyState();
        updatePaneAuthUI();
        updateGlobalAuthHeader();
      },
      onMessage: appendMessage,
      onSystem: appendSystem,
      onClearMessage: ({ messageId })=>{
        if(messageId) removeMatching((row)=>row.dataset.messageId === messageId);
      },
      onClearChat: ({ userId, username })=>{
        if(!userId && !username){
          clearMessages();
          return;
        }
        const normalizedUsername = String(username || '').toLowerCase();
        removeMatching((row)=>
          (userId && row.dataset.userId === userId) ||
          (normalizedUsername && row.dataset.username === normalizedUsername)
        );
      },
      onAuthenticationFailed: handleAuthenticationFailed,
    });

    if(paneNewMessagesEl && typeof paneNewMessagesEl.addEventListener === 'function'){
      paneNewMessagesEl.addEventListener('click', jumpToLatest);
    }
    if(paneComposerEl && typeof paneComposerEl.addEventListener === 'function'){
      paneComposerEl.addEventListener('submit', handleComposerSubmit);
    }

    function setPaneChannel(channel){
      const normalized = normalizeChannel(channel);
      if(normalized === currentChannel){
        if(paneChannelEl) paneChannelEl.textContent = currentChannel ? '#' + currentChannel : 'Chat';
        updateEmptyState();
        updatePaneAuthUI();
        return;
      }
      currentChannel = normalized;
      if(paneEl && paneEl.dataset){
        if(currentChannel) paneEl.dataset.channel = currentChannel;
        else delete paneEl.dataset.channel;
      }
      if(paneChannelEl) paneChannelEl.textContent = currentChannel ? '#' + currentChannel : 'Chat';
      clearMessages();
      client.setChannel(currentChannel);
      updatePaneAuthUI();
    }

    function setPaneEnabled(enabled){
      client.setEnabled(enabled);
      if(!enabled){
        clearMessages();
      } else {
        updateEmptyState();
      }
      updatePaneAuthUI();
    }

    function setPaneIdentity(identity){
      client.setIdentity(identity);
      updatePaneAuthUI();
    }

    setStatus(paneStatusEl, client.state, 0);
    client.setChannel(currentChannel);
    client.setEnabled(visible);
    updatePaneAuthUI();
    updateEmptyState();

    return {
      isPrimary,
      paneEl,
      headerEl,
      headerActionsEl,
      statusEl: paneStatusEl,
      channelEl: paneChannelEl,
      messagesEl: paneMessagesEl,
      emptyEl: paneEmptyEl,
      newMessagesEl: paneNewMessagesEl,
      composerEl: paneComposerEl,
      inputEl: paneInputEl,
      sendButtonEl: paneSendButtonEl,
      client,
      getChannel: ()=>currentChannel,
      setChannel: setPaneChannel,
      setEnabled: setPaneEnabled,
      setIdentity: setPaneIdentity,
      appendSystem,
      clearMessages,
      updateEmptyState,
      updateAuthUI: updatePaneAuthUI,
      destroy(){
        if(paneNewMessagesEl && typeof paneNewMessagesEl.removeEventListener === 'function'){
          paneNewMessagesEl.removeEventListener('click', jumpToLatest);
        }
        if(paneComposerEl && typeof paneComposerEl.removeEventListener === 'function'){
          paneComposerEl.removeEventListener('submit', handleComposerSubmit);
        }
        client.destroy();
        if(!isPrimary && paneEl && typeof paneEl.remove === 'function'){
          paneEl.remove();
        }
      },
    };
  }

  const primaryPane = createPaneInstance({
    isPrimary: true,
    paneEl: resolvedPrimaryPaneEl,
    headerEl: primaryHeaderEl,
    headerActionsEl: primaryHeaderActionsEl,
    paneChannelEl: channelEl,
    paneStatusEl: statusEl,
    paneMessagesEl: messagesEl,
    paneEmptyEl: emptyEl,
    paneNewMessagesEl: newMessagesEl,
    paneComposerEl: composerEl,
    paneInputEl: inputEl,
    paneSendButtonEl: sendButtonEl,
  });

  function getVisiblePanes(){
    if(activePanes.size === 0) return [primaryPane];
    return paneOrder.map((ch)=>activePanes.get(ch)).filter(Boolean);
  }

  function appendSystemToVisiblePanes(payload){
    for(const pane of getVisiblePanes()){
      pane.appendSystem(payload);
    }
  }

  function updateAllAuthUI(){
    updateGlobalAuthHeader();
    primaryPane.updateAuthUI();
    for(const pane of activePanes.values()){
      pane.updateAuthUI();
    }
  }

  function clearIdentityExpiry(){
    if(authExpiryTimer == null) return;
    clearTimeout(authExpiryTimer);
    authExpiryTimer = null;
  }

  function scheduleIdentityExpiry(){
    clearIdentityExpiry();
    if(!isAuthenticated()) return;
    const remaining = currentIdentity.expiresAt - Date.now();
    if(remaining <= 0){
      logout('Your Twitch chat login expired. Log in again to send messages.');
      return;
    }
    authExpiryTimer = setTimeout(()=>{
      authExpiryTimer = null;
      if(!isAuthenticated()) logout('Your Twitch chat login expired. Log in again to send messages.');
      else scheduleIdentityExpiry();
    }, Math.min(remaining, 2147483647));
  }

  function applyIdentityToPanes(identity){
    currentIdentity = normalizeIdentity(identity);
    primaryPane.setIdentity(currentIdentity);
    for(const pane of activePanes.values()){
      if(pane !== primaryPane) pane.setIdentity(currentIdentity);
    }
    updateAllAuthUI();
  }

  function logout(message){
    clearIdentityExpiry();
    clearChatIdentity(authStorageRef);
    applyIdentityToPanes(null);
    if(message) appendSystemToVisiblePanes({ text: message });
  }

  function handleAuthenticationFailed({ authenticated }){
    if(!authenticated) return;
    logout('Your Twitch chat login is no longer valid. Log in again to send messages.');
  }

  function startOAuthLogin(){
    const openPopup = openOAuthWindow || ((url, name, features)=>window.open(url, name, features));
    oauthPopup = openPopup(
      '/api/chat/oauth/start',
      'jellych-twitch-chat-oauth',
      'popup=yes,width=560,height=720,resizable=yes,scrollbars=yes'
    );
    if(!oauthPopup){
      appendSystemToVisiblePanes({ text: 'The Twitch login window was blocked. Allow popups for Jellych and try again.' });
      return;
    }
    if(typeof oauthPopup.focus === 'function') oauthPopup.focus();
  }

  function handleAuthButton(){
    if(isAuthenticated()) logout('Logged out of Twitch chat.');
    else startOAuthLogin();
  }

  function handleOAuthMessage(event){
    if(event.origin !== window.location.origin) return;
    const payload = event.data;
    if(!payload || payload.type !== 'jellych-twitch-chat-oauth') return;
    if(payload.error){
      appendSystemToVisiblePanes({ text: String(payload.error) });
      return;
    }
    const identity = chatIdentityFromOAuthPayload(payload);
    if(!identity){
      appendSystemToVisiblePanes({ text: 'Twitch returned an invalid chat login. Please try again.' });
      return;
    }
    writeChatIdentity(authStorageRef, identity);
    applyIdentityToPanes(identity);
    scheduleIdentityExpiry();
    appendSystemToVisiblePanes({ text: 'Logged in to Twitch chat as @' + identity.login + '.' });
    oauthPopup = null;
  }

  function updateMultiToggleUI(){
    if(!resolvedMultiToggleEl) return;
    const active = streamMultiMode && chatMultiMode;
    if(resolvedMultiToggleEl.classList){
      resolvedMultiToggleEl.classList.toggle('hidden', !streamMultiMode);
      resolvedMultiToggleEl.classList.toggle('is-active', active);
    }
    if(typeof resolvedMultiToggleEl.setAttribute === 'function'){
      resolvedMultiToggleEl.setAttribute('aria-pressed', active ? 'true' : 'false');
    }
  }

  function acquirePaneForChannel(channelKey){
    const existing = activePanes.get(channelKey);
    if(existing) return existing;

    if(!primaryPaneOwner){
      primaryPaneOwner = channelKey;
      if(resolvedPrimaryPaneEl && resolvedPrimaryPaneEl.classList){
        resolvedPrimaryPaneEl.classList.remove('hidden');
      }
      primaryPane.setChannel(channelKey);
      activePanes.set(channelKey, primaryPane);
      return primaryPane;
    }

    const dom = createDynamicChatPaneDOM(documentRef);
    if(resolvedStackEl && typeof resolvedStackEl.appendChild === 'function'){
      resolvedStackEl.appendChild(dom.paneEl);
    }
    const pane = createPaneInstance({
      isPrimary: false,
      paneEl: dom.paneEl,
      headerEl: dom.headerEl,
      headerActionsEl: dom.headerActionsEl,
      paneChannelEl: dom.channelEl,
      paneStatusEl: dom.statusEl,
      paneMessagesEl: dom.messagesEl,
      paneEmptyEl: dom.emptyEl,
      paneNewMessagesEl: dom.newMessagesEl,
      paneComposerEl: dom.composerEl,
      paneInputEl: dom.inputEl,
      paneSendButtonEl: dom.sendButtonEl,
    });
    pane.setChannel(channelKey);
    activePanes.set(channelKey, pane);
    return pane;
  }

  function releasePaneForChannel(channelKey){
    const pane = activePanes.get(channelKey);
    if(!pane) return;
    activePanes.delete(channelKey);
    if(pane.isPrimary){
      primaryPaneOwner = null;
      pane.setChannel('');
    } else {
      pane.destroy();
    }
  }

  function getTargetChannels(){
    if(streamMultiMode && chatMultiMode && openChannels.length > 0){
      return openChannels.slice();
    }
    const single = focusedChannel || openChannels[0] || '';
    return single ? [single] : [];
  }

  function syncChatPanes(){
    const targets = getTargetChannels();
    const targetSet = new Set(targets);

    for(const existingKey of Array.from(activePanes.keys())){
      if(!targetSet.has(existingKey)){
        releasePaneForChannel(existingKey);
      }
    }

    for(const channelKey of targets){
      acquirePaneForChannel(channelKey);
    }

    paneOrder = targets.slice();
    const count = paneOrder.length;
    const isStacked = streamMultiMode && chatMultiMode && count > 1;

    if(panelEl){
      if(panelEl.classList) panelEl.classList.toggle('is-stacked', isStacked);
      if(panelEl.dataset) panelEl.dataset.count = String(count);
    }

    if(resolvedPrimaryPaneEl && resolvedPrimaryPaneEl.classList){
      resolvedPrimaryPaneEl.classList.toggle('hidden', count > 0 && !primaryPaneOwner);
    }

    paneOrder.forEach((channelKey, index)=>{
      const pane = activePanes.get(channelKey);
      if(!pane || !pane.paneEl) return;
      if(pane.paneEl.style) pane.paneEl.style.order = String(index);
      if(resolvedStackEl && typeof resolvedStackEl.appendChild === 'function' && pane.paneEl.parentNode === resolvedStackEl){
        resolvedStackEl.appendChild(pane.paneEl);
      }
    });

    const topPane = (paneOrder[0] && activePanes.get(paneOrder[0])) || primaryPane;
    insertBeforeStatus(topPane.headerActionsEl, topPane.statusEl, resolvedMultiToggleEl);
    insertAfterHeader(topPane.paneEl, topPane.headerEl, resolvedAuthBarEl);
    updateMultiToggleUI();
  }

  function updateToggle(){
    panelEl.hidden = !visible;
    layoutEl.classList.toggle('chat-is-visible', visible);
    toggleEl.setAttribute('aria-pressed', visible ? 'true' : 'false');
    toggleEl.setAttribute('aria-expanded', visible ? 'true' : 'false');
    toggleEl.setAttribute('aria-label', visible ? 'Hide chat' : 'Show chat');
    toggleEl.title = visible ? 'Hide chat' : 'Show chat';
    toggleLabelEl.textContent = visible ? 'Hide chat' : 'Show chat';
  }

  function setVisible(nextVisible, persist = true){
    const next = !!nextVisible;
    if(next === visible){
      updateToggle();
      return;
    }
    visible = next;
    if(persist) writeChatVisibility(storageRef, visible);
    updateToggle();
    primaryPane.setEnabled(visible);
    for(const pane of activePanes.values()){
      if(pane !== primaryPane) pane.setEnabled(visible);
    }
  }

  function setChannel(channel){
    const normalized = normalizeChannel(channel);
    focusedChannel = normalized;
    if(!streamMultiMode){
      openChannels = normalized ? [normalized] : [];
    } else if(normalized && !openChannels.includes(normalized)){
      openChannels = [normalized];
    }
    syncChatPanes();
  }

  function setChannels(channels){
    const normalizedList = [];
    if(Array.isArray(channels)){
      for(const ch of channels){
        const norm = normalizeChannel(ch);
        if(norm && !normalizedList.includes(norm)) normalizedList.push(norm);
      }
    }
    openChannels = normalizedList;
    if(!focusedChannel || !openChannels.includes(focusedChannel)){
      focusedChannel = openChannels[0] || '';
    }
    syncChatPanes();
  }

  function setPlaybackState({ channels = [], focusedChannel: nextFocused = null, multiMode = false } = {}){
    const normalizedList = [];
    if(Array.isArray(channels)){
      for(const ch of channels){
        const norm = normalizeChannel(ch);
        if(norm && !normalizedList.includes(norm)) normalizedList.push(norm);
      }
    }
    openChannels = normalizedList;
    const normFocus = normalizeChannel(nextFocused);
    if(normFocus && openChannels.includes(normFocus)){
      focusedChannel = normFocus;
    } else if(openChannels.length > 0){
      focusedChannel = openChannels[0];
    } else {
      focusedChannel = normFocus || '';
    }
    streamMultiMode = !!multiMode;
    if(!streamMultiMode){
      chatMultiMode = false;
    }
    syncChatPanes();
  }

  function setChatMultiMode(enabled){
    const next = streamMultiMode ? !!enabled : false;
    if(chatMultiMode === next){
      updateMultiToggleUI();
      return;
    }
    chatMultiMode = next;
    syncChatPanes();
  }

  function handleKeydown(event){
    if(event.key !== 'Escape' || !visible) return;
    setVisible(false);
    toggleEl.focus();
  }

  function handleUnload(){
    primaryPane.client.destroy();
    for(const pane of activePanes.values()){
      if(pane !== primaryPane) pane.destroy();
    }
  }

  toggleEl.addEventListener('click', ()=>setVisible(!visible));
  if(resolvedMultiToggleEl && typeof resolvedMultiToggleEl.addEventListener === 'function'){
    resolvedMultiToggleEl.addEventListener('click', ()=>{
      if(!streamMultiMode) return;
      setChatMultiMode(!chatMultiMode);
    });
  }
  if(authButtonEl) authButtonEl.addEventListener('click', handleAuthButton);
  documentRef.addEventListener('keydown', handleKeydown);
  window.addEventListener('beforeunload', handleUnload);
  window.addEventListener('message', handleOAuthMessage);

  updateToggle();
  updateMultiToggleUI();
  scheduleIdentityExpiry();
  updateAllAuthUI();
  syncChatPanes();

  return {
    setChannel,
    setChannels,
    setPlaybackState,
    setChatMultiMode,
    isChatMultiMode: ()=>streamMultiMode && chatMultiMode,
    getActiveChannels: ()=>paneOrder.slice(),
    destroy(){
      if(destroyed) return;
      destroyed = true;
      clearIdentityExpiry();
      documentRef.removeEventListener('keydown', handleKeydown);
      window.removeEventListener('beforeunload', handleUnload);
      window.removeEventListener('message', handleOAuthMessage);
      for(const pane of Array.from(activePanes.values())){
        if(pane !== primaryPane) pane.destroy();
      }
      activePanes.clear();
      primaryPane.destroy();
    },
  };
}
