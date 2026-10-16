'use strict';
const $ = id => document.getElementById(id);
async function api(path, body) {
 const response = await fetch(path, body === undefined ? {cache:'no-store'} : {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body),cache:'no-store'});
 const data = await response.json().catch(()=>({error:'Unexpected server response'}));
 if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
 return data;
}
function cell(row, value) {const td=document.createElement('td');td.textContent=String(value);row.append(td);}
async function refresh() {
 try {
 const s=await api('/status'), w=s.warming, l=s.delivery_log;
 $('status-message').textContent=`Origin: ${s.origin} · Sitemap state: ${w.state} · Request page failures: ${w.extra_failed_pages??0} · Snapshot serving: ${s.snapshot_serving_enabled?'enabled':'disabled'}`;
 $('metrics').replaceChildren();
 for (const [label,value] of [['Sitemap cached',`${w.cached}/${w.discovered}`],['Queued',w.queued],['Rendering',w.rendering],['Request pages',w.extra_pages]]) {const card=document.createElement('div');const strong=document.createElement('strong');strong.textContent=value??0;const span=document.createElement('span');span.textContent=label;card.append(strong,span);$('metrics').append(card);}
 $('health').textContent=JSON.stringify(w,null,2);
 $('log-status').textContent=`Logging ${l.enabled?'enabled':'disabled'} · accepted ${l.accepted} · written ${l.written} · dropped ${l.dropped} · failures ${l.failures}${l.error?' · '+l.error:''}. Recent receipts are in memory; written counts track file writes, not fsync durability.`;
 $('logs').replaceChildren();
 for (const e of [...l.recent].reverse()) {const row=document.createElement('tr');cell(row,`${e.time} · ${e.crawler_claim}${e.simulated?' · SIMULATED':''}`);cell(row,`${e.method} ${e.path}`);cell(row,`${e.status} ${e.cache} · age ${e.snapshot_age_seconds}s · ${e.duration_ms}ms`);cell(row,e.delivered_bytes);cell(row,e.full_snapshot_html?'Yes':e.cached_response?'No · cached response':'No');$('logs').append(row);}
 } catch(e) {$('status-message').textContent=e.message;}
}
$('refresh').addEventListener('click',refresh);
$('compare-form').addEventListener('submit',async e=>{
 e.preventDefault();$('result-message').textContent='Comparing…';$('compare').disabled=true;
 for(const id of ['raw-html','snapshot-html','raw-text','rendered-text','heuristic']) $(id).textContent='';
 try {const d=await api('/diagnose',{url:$('url').value}),c=d.comparison;
 $('raw-html').textContent=d.raw_html;$('snapshot-html').textContent=d.snapshot_html;$('raw-text').textContent=c.raw_text;
 for (const p of c.parts) {const node=document.createElement(p.missing?'mark':'span');node.textContent=p.text+' ';$('rendered-text').append(node);}
 $('result-message').textContent=`${c.likely_shell?'Likely JavaScript shell':'No shell flagged'} · ${c.raw_words} raw / ${c.rendered_words} rendered words · ${c.missing_words} words absent from raw · Snapshot ${d.snapshot_at}`;
 $('heuristic').textContent=d.heuristic;
 }catch(e){$('result-message').textContent=e.message;}finally{$('compare').disabled=false;}
});
$('simulate').addEventListener('click',async()=>{
 $('simulate').disabled=true;$('simulation').textContent='Sending local simulation…';
 try {const d=await api('/simulate',{url:$('url').value});$('simulation').textContent=JSON.stringify(d,null,2);await refresh();}catch(e){$('simulation').textContent=e.message;}finally{$('simulate').disabled=false;}
});
refresh();
