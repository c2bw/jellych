import assert from 'node:assert/strict';
import test from 'node:test';

import { parseTwitchChannelInput } from '../html/js/utils.js';

test('parseTwitchChannelInput extracts channel names from plain logins and handles', ()=>{
  assert.equal(parseTwitchChannelInput('Caedrel'), 'caedrel');
  assert.equal(parseTwitchChannelInput('  @shroud  '), 'shroud');
  assert.equal(parseTwitchChannelInput('#jankos'), 'jankos');
  assert.equal(parseTwitchChannelInput('some_user_123'), 'some_user_123');
});

test('parseTwitchChannelInput extracts channel names from Twitch URLs', ()=>{
  assert.equal(parseTwitchChannelInput('https://www.twitch.tv/Caedrel'), 'caedrel');
  assert.equal(parseTwitchChannelInput('https://twitch.tv/shroud/'), 'shroud');
  assert.equal(parseTwitchChannelInput('twitch.tv/jankos?sr=a'), 'jankos');
  assert.equal(parseTwitchChannelInput('www.twitch.tv/lck'), 'lck');
  assert.equal(parseTwitchChannelInput('https://player.twitch.tv/?channel=Caedrel&parent=localhost'), 'caedrel');
});

test('parseTwitchChannelInput rejects VODs, clips, and invalid inputs', ()=>{
  assert.equal(parseTwitchChannelInput(''), '');
  assert.equal(parseTwitchChannelInput('   '), '');
  assert.equal(parseTwitchChannelInput('https://www.twitch.tv/videos/1234567890'), '');
  assert.equal(parseTwitchChannelInput('https://www.twitch.tv/shroud/clip/SomeClip'), '');
  assert.equal(parseTwitchChannelInput('https://clips.twitch.tv/SomeClip'), '');
  assert.equal(parseTwitchChannelInput('https://example.com/shroud'), '');
  assert.equal(parseTwitchChannelInput('invalid channel name'), '');
  assert.equal(parseTwitchChannelInput('videos'), '');
});
