// GoLens shared UI helpers — included by the dashboard and the alerts page.
// Server-rendered values arrive via window.GoLensConfig (inline bootstrap).
'use strict';

// --- theme -------------------------------------------------------------------

function applyTheme(t) {
  var c = document.documentElement.classList;
  c.toggle('light', t === 'light');
  c.toggle('dark', t !== 'light');
  try { localStorage.setItem('golens.theme', t); } catch (e) {}
  var btn = document.getElementById('theme-toggle');
  if (btn) btn.textContent = (t === 'light') ? '☀️' : '🌙';
}

function toggleTheme() {
  applyTheme(document.documentElement.classList.contains('light') ? 'dark' : 'light');
}

// Sync the toggle button's icon once the DOM is ready.
document.addEventListener('DOMContentLoaded', function () {
  var t = 'dark';
  try { t = localStorage.getItem('golens.theme') || 'dark'; } catch (e) {}
  applyTheme(t);
});

// --- shared fetch wrapper ------------------------------------------------------

// fetchJSON fetches a URL and resolves the parsed JSON body, rejecting with an
// Error carrying the server's message when the response is not ok.
function fetchJSON(url, options) {
  return fetch(url, options).then(function (r) {
    if (!r.ok) {
      return r.json().catch(function () { return null; }).then(function (body) {
        throw new Error((body && body.error) || r.statusText || ('request failed: ' + r.status));
      });
    }
    return r.json();
  });
}

// postJSON sends a JSON request body and returns the parsed JSON response.
function postJSON(url, body) {
  return fetchJSON(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  });
}

// --- toast ---------------------------------------------------------------------

var _toastTimer = null;
function showToast(msg, type) {
  var el = document.getElementById('app-toast');
  if (!el) return;
  el.textContent = msg;
  // The toast base rule keeps it hidden; a variant class makes it visible.
  el.className = 'toast ' + (type === 'success' ? 'toast-success' : 'toast-error');
  if (_toastTimer) clearTimeout(_toastTimer);
  _toastTimer = setTimeout(function () { el.className = 'toast'; }, 4000);
}

// Number formatting shared by chart code (kept out of per-page scripts).
function fmtBytes(b) {
  if (b < 1024) return b.toFixed(0) + ' B';
  if (b < 1024 * 1024) return (b / 1024).toFixed(1) + ' KB';
  if (b < 1024 * 1024 * 1024) return (b / (1024 * 1024)).toFixed(1) + ' MB';
  return (b / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
}

function fmtCount(n) {
  if (n < 1000) return n.toString();
  if (n < 1e6) return (n / 1e3).toFixed(1) + 'K';
  return (n / 1e6).toFixed(1) + 'M';
}
