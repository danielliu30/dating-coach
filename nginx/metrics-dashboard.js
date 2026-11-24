'use strict';
const TOKEN_KEY = 'dc_ops_token';
let token = localStorage.getItem(TOKEN_KEY) || '';

function saveTok() {
  token = document.getElementById('tok').value.trim();
  localStorage.setItem(TOKEN_KEY, token);
  load();
}

async function get(path) {
  const r = await fetch(path, { headers: { Authorization: 'Bearer ' + token } });
  if (r.status === 401 || r.status === 403) { gate(); throw new Error('auth'); }
  if (!r.ok) throw new Error(path + ' -> ' + r.status);
  return r.text();
}

function gate(msg) {
  document.getElementById('gate').style.display = 'block';
  document.getElementById('app').style.visibility = 'hidden';
  if (msg) document.getElementById('gatemsg').textContent = msg;
}

function lines(t) { return t.trim().split('\n').filter(Boolean).map(JSON.parse); }
function esc(s) { return String(s).replace(/[&<>]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c])); }
function short(name) { return name.replace(/^dating-coach_/, '').replace(/\.\d+\.[a-z0-9]+$/i, ''); }
function pct(s) { return parseFloat(s); }

function spark(vals, w = 90, h = 16) {
  const c = document.createElement('canvas'); c.width = w; c.height = h;
  const x = c.getContext('2d');
  if (vals.length > 1) {
    const max = Math.max(...vals, 1e-6);
    x.strokeStyle = '#8ab4f8'; x.beginPath();
    vals.forEach((v, i) => { const px = i * (w - 1) / (vals.length - 1), py = h - 1 - (v / max) * (h - 2); i ? x.lineTo(px, py) : x.moveTo(px, py); });
    x.stroke();
  }
  return c.outerHTML;
}

async function render() {
  document.getElementById('ts').textContent = new Date().toLocaleTimeString();

  const svcTxt = await get('/metrics/data/metrics-services.jsonl').catch(() => '');
  const statTxt = await get('/metrics/data/metrics-stats.jsonl').catch(() => '');
  const depTxt = await get('/metrics/data/deploy-history.jsonl').catch(() => '');

  // latest service snapshot per name
  const svcLatest = {};
  for (const s of lines(svcTxt)) svcLatest[s.Name] = s;
  document.getElementById('services').innerHTML =
    '<tr><th>service</th><th>replicas</th><th>image</th></tr>' +
    Object.values(svcLatest).sort((a, b) => a.Name.localeCompare(b.Name)).map(s => {
      const bad = s.Replicas && !s.Replicas.startsWith(s.Replicas.split('/')[1] + '/');
      const ok = s.Replicas && s.Replicas.split('/')[0] === s.Replicas.split('/')[1];
      return `<tr><td>${esc(s.Name)}</td><td class="${ok ? 'ok' : 'bad'}">${esc(s.Replicas)}</td><td class="dim">${esc(s.Image)}</td></tr>`;
    }).join('');

  // task samples, last 240 lines
  const samples = lines(statTxt).slice(-240);
  const byTask = {};
  for (const s of samples) (byTask[s.Name] = byTask[s.Name] || []).push(s);
  document.getElementById('tasks').innerHTML =
    '<tr><th>task</th><th>cpu</th><th>cpu hist</th><th>mem</th><th>mem%</th><th>net i/o</th><th>pids</th></tr>' +
    Object.keys(byTask).sort().map(n => {
      const arr = byTask[n], last = arr[arr.length - 1];
      return `<tr><td>${esc(short(n))}</td><td>${esc(last.CPUPerc)}</td><td>${spark(arr.map(s => pct(s.CPUPerc)))}</td>` +
        `<td>${esc(last.MemUsage.split(' / ')[0])}</td><td>${esc(last.MemPerc)}</td><td class="dim">${esc(last.NetIO)}</td><td>${esc(last.PIDs)}</td></tr>`;
    }).join('');

  // app /stats cards
  const cards = [];
  for (const svc of ['api', 'worker']) {
    try {
      const j = JSON.parse(await get(`/metrics/proxy/${svc}`));
      cards.push(`<div class="card"><div class="k">${svc} requests</div><div class="v">${j.requests_total}</div>` +
        `<div class="k">2xx ${j.responses_2xx} / 4xx ${j.responses_4xx} / 5xx ${j.responses_5xx}</div></div>`);
      cards.push(`<div class="card"><div class="k">${svc} uptime</div><div class="v">${Math.round(j.uptime_seconds / 60)}m</div>` +
        `<div class="k">${j.goroutines} goroutines, ${j.mem_alloc_mb} MiB${j.ws_connections !== undefined ? ', ' + j.ws_connections + ' ws' : ''}</div></div>`);
    } catch (e) { cards.push(`<div class="card"><div class="k">${svc}</div><div class="v bad">down</div></div>`); }
  }
  document.getElementById('stats').innerHTML = cards.join('');

  // deploy history, newest first
  const deps = lines(depTxt).slice(-30).reverse();
  document.getElementById('deploys').innerHTML =
    '<tr><th>time</th><th>sha</th><th>result</th><th>s</th></tr>' +
    deps.map(d => `<tr><td class="dim">${esc(d.ts)}</td><td>${esc(String(d.sha).slice(0, 12))}</td>` +
      `<td class="${d.result === 'ok' ? 'ok' : 'bad'}">${esc(d.result)}</td><td>${d.seconds}</td></tr>`).join('');
}

async function load() {
  if (!token) return gate();
  document.getElementById('gate').style.display = 'none';
  document.getElementById('app').style.visibility = 'visible';
  try { await render(); } catch (e) { if (e.message !== 'auth') console.error(e); }
}

load();
setInterval(load, 15000);
