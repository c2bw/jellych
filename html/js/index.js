import { Player } from './player.js';
import { initStats } from './stats.js';
import { initManager } from './manager.js';
import { initPlayerControls } from './player_controls.js';

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

const player = new Player(video);
const stats = initStats({ video, player, statsOverlay, statsState, statsGrid });
const manager = initManager({ listEl, removeSelect, addBtn, newNameEl, removeBtn, managerMsgEl, player, stats, playerTitleEl });
initPlayerControls({ video, stage: playerStageEl });

// Wire video events to stats update
['pause','play','waiting','playing','timeupdate','loadedmetadata'].forEach(ev => video.addEventListener(ev, ()=>stats.update()));
video.addEventListener('emptied', ()=>stats.clear());

// initial load
manager.fetchChannels();
stats.update();
