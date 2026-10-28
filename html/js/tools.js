import { parseTwitchChannelInput } from './utils.js';

const viewForm = document.getElementById('viewStreamForm');
const inputEl = document.getElementById('streamChannelInput');
const viewSubmitBtn = document.getElementById('viewStreamBtn');
const extractBtn = document.getElementById('extractUrlBtn');
const msgEl = document.getElementById('viewStreamMsg');
const extractResultEl = document.getElementById('extractUrlResult');
const extractedUrlEl = document.getElementById('extractedStreamUrl');
const copyUrlBtn = document.getElementById('copyStreamUrlBtn');

function setMessage(message, isError){
  if(!msgEl) return;
  msgEl.textContent = message || '';
  msgEl.className = isError ? 'form-message text-red-400' : 'form-message';
}

function setButtonsDisabled(disabled){
  if(viewSubmitBtn) viewSubmitBtn.disabled = disabled;
  if(extractBtn) extractBtn.disabled = disabled;
}

async function handleViewStream(event){
  event.preventDefault();
  const channel = parseTwitchChannelInput(inputEl ? inputEl.value : '');
  if(!channel){
    setMessage('Enter a valid Twitch channel name or URL', true);
    return;
  }

  setButtonsDisabled(true);
  setMessage('Checking livestream…', false);
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
    setMessage(err && err.message ? err.message : 'Failed to load livestream', true);
    setButtonsDisabled(false);
  }
}

async function handleExtractStreamURL(){
  const channel = parseTwitchChannelInput(inputEl ? inputEl.value : '');
  if(!channel){
    setMessage('Enter a valid Twitch channel name or URL', true);
    if(extractResultEl) extractResultEl.classList.add('hidden');
    return;
  }

  setButtonsDisabled(true);
  if(extractResultEl) extractResultEl.classList.add('hidden');
  if(extractedUrlEl) extractedUrlEl.textContent = '';
  if(copyUrlBtn) copyUrlBtn.textContent = 'Copy URL';
  setMessage('Resolving backend stream URL…', false);

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
    setMessage('Resolved backend HLS URL for ' + channel, false);
  }catch(err){
    setMessage(err && err.message ? err.message : 'Failed to resolve stream URL', true);
  }finally{
    setButtonsDisabled(false);
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
  setMessage('Select the URL below to copy it manually', false);
}

if(viewForm) viewForm.addEventListener('submit', handleViewStream);
if(extractBtn) extractBtn.addEventListener('click', handleExtractStreamURL);
if(copyUrlBtn) copyUrlBtn.addEventListener('click', handleCopyStreamURL);
