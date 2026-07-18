import assert from 'node:assert/strict';
import test from 'node:test';

import { createPollingLoop } from '../html/js/polling.js';

function fakeTimers(){
  const pending = [];
  return {
    pending,
    setTimer(callback, delay){
      pending.push({ callback, delay });
      return pending.length;
    },
    runNext(){
      const timer = pending.shift();
      assert.ok(timer, 'expected a pending timer');
      return timer.callback();
    },
  };
}

test('polling waits for the active refresh before scheduling another cycle', async ()=>{
  const timers = fakeTimers();
  let releaseFirst;
  const firstRefresh = new Promise((resolve)=>{ releaseFirst = resolve; });
  let calls = 0;
  const start = createPollingLoop(()=>{
    calls++;
    return calls === 1 ? firstRefresh : Promise.resolve();
  }, { delayMs: 5000, setTimer: timers.setTimer });

  start();
  start();
  assert.equal(timers.pending.length, 1, 'start should schedule only one timer');
  assert.equal(timers.pending[0].delay, 5000);

  const activeCycle = timers.runNext();
  assert.equal(calls, 1);
  assert.equal(timers.pending.length, 0, 'no timer should be scheduled while refresh is active');

  releaseFirst();
  await activeCycle;
  assert.equal(timers.pending.length, 1, 'next timer should be scheduled after refresh completes');

  await timers.runNext();
  assert.equal(calls, 2);
  assert.equal(timers.pending.length, 1, 'each completed refresh should schedule exactly one successor');
});

test('polling continues after a failed refresh', async ()=>{
  const timers = fakeTimers();
  const errors = [];
  const expected = new Error('refresh failed');
  const start = createPollingLoop(async ()=>{ throw expected; }, {
    delayMs: 5000,
    setTimer: timers.setTimer,
    onError: (error)=>errors.push(error),
  });

  start();
  await timers.runNext();

  assert.deepEqual(errors, [expected]);
  assert.equal(timers.pending.length, 1, 'failed refresh should still schedule the next cycle');
});
