const IRC_URL = 'wss://irc-ws.chat.twitch.tv:443';
const CHAT_VISIBILITY_KEY = 'jellych_chat_visible';
const RECONNECT_DELAYS_MS = [1000, 2000, 4000, 8000, 16000, 30000];
const MAX_CHAT_ROWS = 100;
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
    this.endpoint = options.endpoint || IRC_URL;

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
  }

  setState(state, details = {}){
    this.state = state;
    safeCallback(this.onState, { state, channel: this.channel, ...details });
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
      const suffix = Math.floor(10000 + this.random() * 90000);
      const nick = 'justinfan' + suffix;
      try{
        socket.send('PASS blah\r\n');
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

    if(message.command === 'NOTICE' && /login authentication failed/i.test(message.trailing)){
      this.authenticationFailed = true;
      this.setState('unavailable', { reason: 'authentication' });
      this.cancelConnection(false);
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

export function initTwitchChat({
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
  socketFactory,
}){
  const documentRef = panelEl.ownerDocument;
  let storageRef = storage;
  if(storageRef === undefined){
    try{ storageRef = window.localStorage; }catch(error){ storageRef = null; }
  }
  let visible = readChatVisibility(storageRef);
  let currentChannel = '';
  let unreadCount = 0;
  let destroyed = false;

  function resetUnread(){
    unreadCount = 0;
    newMessagesEl.textContent = 'New messages';
    newMessagesEl.classList.add('hidden');
  }

  function updateEmptyState(){
    const hasMessages = messagesEl.childElementCount > 0;
    emptyEl.classList.toggle('hidden', hasMessages);
    if(hasMessages) return;
    if(!currentChannel) emptyEl.textContent = 'Select a live channel to view chat';
    else if(client.state === 'connected') emptyEl.textContent = 'Waiting for chat messages…';
    else if(client.state === 'unavailable') emptyEl.textContent = 'Twitch chat is unavailable';
    else emptyEl.textContent = 'Connecting to #' + currentChannel + '…';
  }

  function clearMessages(){
    messagesEl.textContent = '';
    resetUnread();
    updateEmptyState();
  }

  function appendRow(row){
    const follow = isNearBottom(messagesEl);
    messagesEl.appendChild(row);
    while(messagesEl.childElementCount > MAX_CHAT_ROWS){
      messagesEl.firstElementChild.remove();
    }
    updateEmptyState();
    if(follow){
      resetUnread();
      requestAnimationFrame(()=>{ messagesEl.scrollTop = messagesEl.scrollHeight; });
    } else {
      unreadCount++;
      newMessagesEl.textContent = unreadCount === 1 ? '1 new message' : unreadCount + ' new messages';
      newMessagesEl.classList.remove('hidden');
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
    for(const child of Array.from(messagesEl.children)){
      if(predicate(child)) child.remove();
    }
    updateEmptyState();
  }

  const client = new TwitchIRCClient({
    socketFactory,
    onState: ({ state, delayMs })=>{
      setStatus(statusEl, state, delayMs);
      updateEmptyState();
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
  });

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
    if(!visible){
      client.setEnabled(false);
      clearMessages();
      return;
    }
    client.setEnabled(true);
    updateEmptyState();
  }

  function setChannel(channel){
    const normalized = normalizeChannel(channel);
    if(normalized === currentChannel) return;
    currentChannel = normalized;
    channelEl.textContent = currentChannel ? '#' + currentChannel : 'Chat';
    clearMessages();
    client.setChannel(currentChannel);
  }

  function jumpToLatest(){
    messagesEl.scrollTop = messagesEl.scrollHeight;
    resetUnread();
  }

  function handleKeydown(event){
    if(event.key !== 'Escape' || !visible) return;
    setVisible(false);
    toggleEl.focus();
  }

  function handleUnload(){
    client.destroy();
  }

  toggleEl.addEventListener('click', ()=>setVisible(!visible));
  newMessagesEl.addEventListener('click', jumpToLatest);
  documentRef.addEventListener('keydown', handleKeydown);
  window.addEventListener('beforeunload', handleUnload);

  updateToggle();
  client.setChannel(currentChannel);
  client.setEnabled(visible);
  updateEmptyState();

  return {
    setChannel,
    destroy(){
      if(destroyed) return;
      destroyed = true;
      documentRef.removeEventListener('keydown', handleKeydown);
      window.removeEventListener('beforeunload', handleUnload);
      client.destroy();
    },
  };
}
