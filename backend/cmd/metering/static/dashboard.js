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
  return r.json();
}

function gate(msg) {
  document.getElementById('gate').style.display = 'block';
  document.getElementById('app').style.visibility = 'hidden';
  if (msg) document.getElementById('gatemsg').textContent = msg;
}

function esc(s) { return String(s).replace(/[&<>]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c])); }
function short(name) { return name.replace(/^dating-coach_/, '').replace(/\.\d+\.[a-z0-9]+$/i, ''); }
function mib(b) { return (b / 1048576).toFixed(1) + ' MiB'; }
function netio(rx, tx) { return mib(rx) + ' / ' + mib(tx); }
function dur(s) { const m = Math.round(s / 60); return m >= 60 ? (m / 60).toFixed(1) + 'h' : m + 'm'; }

function spark(vals, w = 90, h = 16) {
  const c = document.createElement('canvas'); c.width = w; c.height = h;
  const x = c.getContext('2d');
  if (vals && vals.length > 1) {
    const max = Math.max(...vals, 1e-6);
    x.strokeStyle = '#8ab4f8'; x.beginPath();
    vals.forEach((v, i) => { const px = i * (w - 1) / (vals.length - 1), py = h - 1 - (v / max) * (h - 2); i ? x.lineTo(px, py) : x.moveTo(px, py); });
    x.stroke();
  }
  return c.outerHTML;
}

async function render() {
  document.getElementById('ts').textContent = new Date().toLocaleTimeString();
  const snap = await get('api/snapshot');

  document.getElementById('services').innerHTML =
    '<tr><th>service</th><th>replicas</th><th>uptime</th><th>image</th></tr>' +
    (snap.services || []).map(s => {
      const ok = s.desired > 0 && s.running === s.desired;
      return `<tr><td>${esc(s.name)}</td><td class="${ok ? 'ok' : 'bad'}">${s.running}/${s.desired}</td>` +
        `<td class="dim">${esc(dur(s.uptime_seconds || 0))}</td><td class="dim">${esc(s.image)}</td></tr>`;
    }).join('');

  const hist = snap.cpu_hist || {};
  document.getElementById('tasks').innerHTML =
    '<tr><th>task</th><th>cpu</th><th>cpu hist</th><th>mem</th><th>mem%</th><th>net rx/tx</th><th>pids</th></tr>' +
    (snap.tasks || []).map(t =>
      `<tr><td>${esc(short(t.name))}</td><td>${t.cpu_perc.toFixed(2)}%</td><td>${spark(hist[t.name])}</td>` +
      `<td>${esc(mib(t.mem_bytes))}</td><td>${t.mem_perc.toFixed(1)}%</td>` +
      `<td class="dim">${esc(netio(t.net_rx, t.net_tx))}</td><td>${t.pids}</td></tr>`).join('');

  const cards = [];
  for (const b of Object.keys(snap.requests || {}).sort()) {
    const r = snap.requests[b];
    cards.push(`<div class="card"><div class="k">${b} requests</div><div class="v">${r.requests}</div>` +
      `<div class="k">2xx ${r.s2xx} / 4xx ${r.s4xx} / 5xx ${r.s5xx}</div></div>`);
  }
  document.getElementById('stats').innerHTML = cards.join('');

  document.getElementById('deploys').innerHTML =
    '<tr><th>time</th><th>sha</th><th>result</th><th>s</th></tr>' +
    (snap.deploys || []).map(d => `<tr><td class="dim">${esc(d.ts)}</td><td>${esc(String(d.sha).slice(0, 12))}</td>` +
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
