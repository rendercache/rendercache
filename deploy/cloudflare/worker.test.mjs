import {test} from 'node:test';
import assert from 'node:assert/strict';
import {handle} from './worker.mjs';
const env={PUBLIC_HOST:'app.example.com',CACHE_ORIGIN:'https://cache.example.net',RENDERCACHE_EDGE_TOKEN:'edge-secret'};
test('bot routing preserves escaped path, query and validators; redirects are manual',async()=>{
 const incoming=new Request('https://app.example.com/a%2Fb?q=1&q=2',{headers:{'User-Agent':'GPTBot','If-None-Match':'"etag"','X-RenderCache-Edge-Key':'untrusted'}});
 const response=await handle(incoming,env,async(req,options)=>{
  assert.equal(req.url,'https://cache.example.net/a%2Fb?q=1&q=2');assert.equal(req.redirect,'manual');assert.equal(req.headers.get('X-RenderCache-Edge-Key'),'edge-secret');assert.equal(req.headers.get('If-None-Match'),'"etag"');assert.equal(options.cf.cacheTtl,0);
  return new Response(null,{status:302,headers:{Location:'https://app.example.com/new','X-RenderCache':'HIT'}});
 });assert.equal(response.status,302);assert.equal(response.headers.get('Location'),'https://app.example.com/new');assert.equal(response.headers.get('Cache-Control'),'private, no-store');
});
test('humans, private requests, POST and protocol requests retain origin behavior',async()=>{
 for(const settings of [{headers:{'User-Agent':'browser'}},{headers:{'User-Agent':'GPTBot',Cookie:'private=1'}},{headers:{'User-Agent':'GPTBot',Authorization:'Bearer visitor'}},{method:'POST',body:'form',headers:{'User-Agent':'GPTBot'}},{headers:{'User-Agent':'GPTBot',Upgrade:'websocket'}},{headers:{'User-Agent':'GPTBot',Range:'bytes=0-1'}}]){
  const incoming=new Request('https://app.example.com/path',{...settings,headers:{...settings.headers,'X-RenderCache-Edge-Key':'spoofed'}});
  await handle(incoming,env,async req=>{assert.equal(req.url,incoming.url);assert.equal(req.method,incoming.method);assert.equal(req.headers.get('X-RenderCache-Edge-Key'),null);assert.equal(req.headers.get('Cookie'),incoming.headers.get('Cookie'));return new Response('origin');});
 }
});
test('reject unexpected hosts, invalid backends and missing secrets',async()=>{
 let calls=0;const noFetch=async()=>{calls++;return new Response('unexpected');};
 assert.equal((await handle(new Request('https://evil.example/'),env,noFetch)).status,403);
 for(const replacement of [{CACHE_ORIGIN:'http://cache.example.net'},{CACHE_ORIGIN:'https://app.example.com'},{CACHE_ORIGIN:'https://cache.example.net/path'},{CACHE_ORIGIN:'https://user:password@cache.example.net'},{RENDERCACHE_EDGE_TOKEN:''}]){
 assert.equal((await handle(new Request('https://app.example.com/',{headers:{'User-Agent':'GPTBot'}}),{...env,...replacement},noFetch)).status,503);
 }assert.equal(calls,0);
});
test('backend failure is explicit and no edge secret is returned',async()=>{
 const response=await handle(new Request('https://app.example.com/',{headers:{'User-Agent':'GPTBot'}}),env,async()=>{throw new Error('secret upstream error');});assert.equal(response.status,502);assert.doesNotMatch(await response.text(),/secret/);
});
