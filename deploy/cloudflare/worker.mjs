// SPDX-License-Identifier: Apache-2.0
// Keep routing patterns aligned with your configured RenderCache crawler list.
export const bots = ['Googlebot','bingbot','DuckDuckBot','OAI-SearchBot','GPTBot','ChatGPT-User','ClaudeBot','Claude-SearchBot','Claude-User','PerplexityBot','Perplexity-User','Meta-ExternalAgent','Bytespider','Applebot','facebookexternalhit','Twitterbot','LinkedInBot','Slackbot','Discordbot','Google-InspectionTool','GoogleOther','Amazonbot','YandexBot','Baiduspider','CCBot'];
const keyHeader='X-RenderCache-Edge-Key';
export async function handle(request, env, fetcher=fetch) {
 const incoming=new URL(request.url);
 if(incoming.host!==env.PUBLIC_HOST) return new Response('Unexpected host',{status:403});
 const headers=new Headers(request.headers);
 headers.delete(keyHeader); // Never forward a visitor-supplied edge key.
 const claimedBot=bots.some(bot=>(headers.get('User-Agent')||'').toLowerCase().includes(bot.toLowerCase()));
 const route=claimedBot&&['GET','HEAD'].includes(request.method)&&!['Cookie','Authorization','Proxy-Authorization','Upgrade','Range'].some(h=>headers.has(h));
 if(!route) return fetcher(new Request(request,{headers}));
 let backend;
 try {
  backend=new URL(env.CACHE_ORIGIN);
  if(backend.protocol!=='https:'||backend.username||backend.password||backend.search||backend.hash||backend.pathname!=='/'||backend.host===incoming.host||!env.RENDERCACHE_EDGE_TOKEN) throw new Error();
 } catch {return new Response('Invalid render backend configuration',{status:503});}
 backend.pathname=incoming.pathname;backend.search=incoming.search;
 headers.set(keyHeader,env.RENDERCACHE_EDGE_TOKEN);
 for(const name of ['Host','Forwarded','X-Forwarded-Host','X-Forwarded-For','X-Forwarded-Proto']) headers.delete(name);
 // RenderCache trusts these only together with the edge token. The origin then
 // sees the visitor's address and the public HTTPS scheme instead of this hop.
 const client=request.headers.get('CF-Connecting-IP');
 if(client) headers.set('X-Forwarded-For',client);
 headers.set('X-Forwarded-Proto',incoming.protocol.replace(':',''));
 headers.set('X-Forwarded-Host',incoming.host);
 try {
  // Manual redirects keep the edge secret at the configured backend. The public
  // browser/crawler receives the saved Location and original status instead.
  const response=await fetcher(new Request(backend,{method:request.method,headers,redirect:'manual',signal:AbortSignal.timeout(10000)}),{cf:{cacheTtl:0,cacheEverything:false}});
  const result=new Response(response.body,response);
  result.headers.set('Cache-Control','private, no-store');
  result.headers.delete(keyHeader);
  return result;
 }catch {return new Response('Render backend unavailable',{status:502,headers:{'Cache-Control':'private, no-store'}});}
}
export default {fetch(request,env){return handle(request,env);}};
