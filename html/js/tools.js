import { parseTwitchChannelInput } from './utils.js';

const viewForm = document.getElementById('viewStreamForm');
const viewInputEl = document.getElementById('streamChannelInput');
const viewSubmitBtn = document.getElementById('viewStreamBtn');
const viewMsgEl = document.getElementById('viewStreamMsg');

const extractForm = document.getElementById('extractUrlForm');
const extractInputEl = document.getElementById('extractChannelInput');
const extractSubmitBtn = document.getElementById('extractUrlBtn');
const extractMsgEl = document.getElementById('extractUrlMsg');
const extractResultEl = document.getElementById('extractUrlResult');
const extractedUrlEl = document.getElementById('extractedStreamUrl');
const copyUrlBtn = document.getElementById('copyStreamUrlBtn');

function setFormMessage(targetEl, message, isError){
  if(!targetEl) return;
  targetEl.textContent = message || '';
  targetEl.className = isError ? 'form-message text-red-400' : 'form-message';
}

async function handleViewStream(event){
  event.preventDefault();
  const channel = parseTwitchChannelInput(viewInputEl ? viewInputEl.value : '');
  if(!channel){
    setFormMessage(viewMsgEl, 'Enter a valid Twitch channel name or URL', true);
    return;
  }

  if(viewSubmitBtn) viewSubmitBtn.disabled = true;
  setFormMessage(viewMsgEl, 'Checking livestream…', false);
  try{
    const playlistURL = '/api/live-proxy/' + encodeURIComponent(channel) + '/index.m3u8';
    const res = await fetch(playlistURL, { method: 'HEAD', cache: 'no-store' });
    if(!res.ok){
      if(res.status === 404){
        throw new Error('Stream is offline or unavailable');
      }
      throw new Error(res.statusText || 'Could not load livestream');
    }
    window.location.assign('/watch?view=' + encodeURIComponent(channel));
  }catch(err){
    setFormMessage(viewMsgEl, err && err.message ? err.message : 'Failed to load livestream', true);
    if(viewSubmitBtn) viewSubmitBtn.disabled = false;
  }
}

async function handleExtractStreamURL(event){
  event.preventDefault();
  const channel = parseTwitchChannelInput(extractInputEl ? extractInputEl.value : '');
  if(!channel){
    setFormMessage(extractMsgEl, 'Enter a valid Twitch channel name or URL', true);
    if(extractResultEl) extractResultEl.classList.add('hidden');
    return;
  }

  if(extractSubmitBtn) extractSubmitBtn.disabled = true;
  if(extractResultEl) extractResultEl.classList.add('hidden');
  if(extractedUrlEl) extractedUrlEl.textContent = '';
  if(copyUrlBtn) copyUrlBtn.textContent = 'Copy URL';
  setFormMessage(extractMsgEl, 'Resolving backend stream URL…', false);

  try{
    const res = await fetch('/api/live-url/' + encodeURIComponent(channel), { cache: 'no-store' });
    if(!res.ok){
      if(res.status === 404){
        throw new Error('Stream is offline or unavailable');
      }
      const text = await res.text();
      throw new Error(text.trim() || res.statusText || 'Could not resolve stream URL');
    }
    const payload = await res.json();
    const url = payload && typeof payload.url === 'string' ? payload.url.trim() : '';
    if(!url){
      throw new Error('No playable stream URL returned');
    }
    if(extractedUrlEl) extractedUrlEl.textContent = url;
    if(extractResultEl) extractResultEl.classList.remove('hidden');
    setFormMessage(extractMsgEl, 'Resolved backend HLS URL for ' + channel, false);
  }catch(err){
    setFormMessage(extractMsgEl, err && err.message ? err.message : 'Failed to resolve stream URL', true);
  }finally{
    if(extractSubmitBtn) extractSubmitBtn.disabled = false;
  }
}

async function handleCopyStreamURL(){
  const url = extractedUrlEl ? extractedUrlEl.textContent.trim() : '';
  if(!url) return;
  try{
    if(navigator.clipboard && typeof navigator.clipboard.writeText === 'function'){
      await navigator.clipboard.writeText(url);
      if(copyUrlBtn) copyUrlBtn.textContent = 'Copied';
      return;
    }
  }catch{ /* fall through */ }
  setFormMessage(extractMsgEl, 'Select the URL below to copy it manually', false);
}

if(viewForm) viewForm.addEventListener('submit', handleViewStream);
if(extractForm) extractForm.addEventListener('submit', handleExtractStreamURL);
if(copyUrlBtn) copyUrlBtn.addEventListener('click', handleCopyStreamURL);
