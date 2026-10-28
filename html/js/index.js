import { Player } from './player.js';
import { initStats } from './stats.js';
import { initManager } from './manager.js';
import { initPlayerControls } from './player_controls.js';
import { initTwitchChat } from './twitch_chat.js';
import { parseTwitchChannelInput } from './utils.js';

const listEl = document.getElementById('channelsList');
const video = document.getElementById('player');
const statsOverlay = document.getElementById('statsOverlay');
const statsState = document.getElementById('statsState');
const statsGrid = document.getElementById('statsGrid');
const playerTitleEl = document.getElementById('playerTitle');
const removeSelect = document.getElementById('removeSelect');
const addBtn = document.getElementById('addBtn');
const newNameEl = document.getElementById('newName');
const removeBtn = document.getElementById('removeBtn');
const managerMsgEl = document.getElementById('managerMsg');
const playerStageEl = document.getElementById('playerStage');
const watchLayoutEl = document.querySelector('.watch-layout');
const chatToggleEl = document.getElementById('chatToggle');
const chatToggleLabelEl = document.getElementById('chatToggleLabel');
const chatPanelEl = document.getElementById('chatPanel');
const chatMessagesEl = document.getElementById('chatMessages');
const chatEmptyEl = document.getElementById('chatEmpty');
const chatStatusEl = document.getElementById('chatStatus');
const chatChannelEl = document.getElementById('chatChannel');
const chatNewMessagesEl = document.getElementById('chatNewMessages');
const chatAuthButtonEl = document.getElementById('chatAuthButton');
const chatIdentityEl = document.getElementById('chatIdentity');
const chatComposerEl = document.getElementById('chatComposer');
const chatInputEl = document.getElementById('chatInput');
const chatSendButtonEl = document.getElementById('chatSendButton');

const player = new Player(video);
const stats = initStats({ video, player, statsOverlay, statsState, statsGrid });
const chat = initTwitchChat({
  toggleEl: chatToggleEl,
  toggleLabelEl: chatToggleLabelEl,
  panelEl: chatPanelEl,
  messagesEl: chatMessagesEl,
  emptyEl: chatEmptyEl,
  statusEl: chatStatusEl,
  channelEl: chatChannelEl,
  newMessagesEl: chatNewMessagesEl,
  layoutEl: watchLayoutEl,
  authButtonEl: chatAuthButtonEl,
  identityEl: chatIdentityEl,
  composerEl: chatComposerEl,
  inputEl: chatInputEl,
  sendButtonEl: chatSendButtonEl,
});
const manager = initManager({
  listEl,
  removeSelect,
  addBtn,
  newNameEl,
  removeBtn,
  managerMsgEl,
  player,
  stats,
  playerTitleEl,
  onPlaybackChannelChange: (channel)=>chat.setChannel(channel),
});
initPlayerControls({ video, stage: playerStageEl, player });

// Wire video events to stats update
['pause','play','waiting','playing','timeupdate','loadedmetadata'].forEach(ev => video.addEventListener(ev, ()=>stats.update()));
video.addEventListener('emptied', ()=>stats.clear());

// initial load
manager.fetchChannels();
stats.update();

const directViewChannel = parseTwitchChannelInput(new URLSearchParams(window.location.search).get('view') || '');
if(directViewChannel){
  manager.playDirectChannel(directViewChannel);
}
