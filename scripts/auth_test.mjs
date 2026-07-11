import assert from 'node:assert/strict';
import test from 'node:test';

const values = new Map();
globalThis.sessionStorage = {
  getItem(key){ return values.get(key) || null; },
  setItem(key, value){ values.set(key, value); },
  clear(){ values.clear(); },
};
globalThis.window = { prompt: ()=>null };

const { apiFetch, getControlSecret } = await import('../html/js/auth.js');

test.beforeEach(()=>sessionStorage.clear());

test('retries a challenged request with the entered session secret', async ()=>{
  const requests = [];
  window.prompt = ()=> '  top-secret  ';
  globalThis.fetch = async (_input, options)=>{
    requests.push(options);
    if(requests.length === 1){
      return new Response('unauthorized', {
        status: 401,
        headers: {'WWW-Authenticate': 'Bearer realm="jellych-control"'},
      });
    }
    return new Response('ok');
  };

  const response = await apiFetch('/api/stream/example', {method: 'POST'});

  assert.equal(response.status, 200);
  assert.equal(requests.length, 2);
  assert.equal(requests[1].headers.get('X-Jellych-API-Secret'), 'top-secret');
  assert.equal(getControlSecret(), 'top-secret');
});

test('adds a stored secret without prompting', async ()=>{
  sessionStorage.setItem('jellych_api_secret', 'stored-secret');
  let promptCalls = 0;
  window.prompt = ()=>{ promptCalls++; return ''; };
  globalThis.fetch = async (_input, options)=>{
    assert.equal(options.headers.get('X-Jellych-API-Secret'), 'stored-secret');
    return new Response('ok');
  };

  const response = await apiFetch('/api/vods', {method: 'POST'});

  assert.equal(response.status, 200);
  assert.equal(promptCalls, 0);
});

test('does not prompt for unrelated unauthorized responses', async ()=>{
  let promptCalls = 0;
  window.prompt = ()=>{ promptCalls++; return 'secret'; };
  globalThis.fetch = async ()=>new Response('unauthorized', {status: 401});

  const response = await apiFetch('/unrelated');

  assert.equal(response.status, 401);
  assert.equal(promptCalls, 0);
});
