// A scripted OpenAI-compatible model for the PowerShell end-to-end run.
// Each request is answered by how many tool results the conversation
// already has: the model opens a PowerShell terminal, runs four commands
// in it, and then answers. Every tool result it is sent is written to
// <out>/tool-results.jsonl, which is what the run is judged by.
//
//   node fakemodel.mjs <port> <out dir>

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';

const port = Number(process.argv[2] || 9123);
const out = process.argv[3] || '.';
fs.mkdirSync(out, { recursive: true });
const log = path.join(out, 'tool-results.jsonl');

const steps = [
  { action: 'open' },
  { action: 'run', command: "Get-Location; Write-Output ('pwsh-major-' + $PSVersionTable.PSVersion.Major)" },
  { action: 'run', command: 'cmd /c exit 7' },
  { action: 'run', command: 'Get-Item C:\\no-such-file-here' },
  { action: 'run', command: '1..120 | ForEach-Object { "line $_" }' },
  { action: 'run', command: "Write-Output ('x' * 300)" },
];

function sse(res, chunks) {
  res.writeHead(200, { 'Content-Type': 'text/event-stream' });
  for (const c of chunks) res.write(`data: ${JSON.stringify(c)}\n\n`);
  res.end('data: [DONE]\n\n');
}

http.createServer((req, res) => {
  let body = '';
  req.on('data', (d) => { body += d; });
  req.on('end', () => {
    if (!req.url.endsWith('/chat/completions')) {
      res.writeHead(404);
      res.end();
      return;
    }
    let msg;
    try { msg = JSON.parse(body); } catch { res.writeHead(400); res.end(); return; }
    const tools = (msg.messages || []).filter((m) => m.role === 'tool');
    const offered = (msg.tools || []).map((t) => t.function && t.function.name);
    if (tools.length) {
      const last = tools[tools.length - 1];
      fs.appendFileSync(log, JSON.stringify({ step: tools.length, content: last.content }) + '\n');
    } else {
      fs.appendFileSync(log, JSON.stringify({ step: 0, offered }) + '\n');
    }
    const usage = { prompt_tokens: 10, completion_tokens: 2 };
    if (tools.length >= steps.length) {
      sse(res, [
        { choices: [{ delta: { content: 'done' } }] },
        { choices: [{ delta: {}, finish_reason: 'stop' }], usage },
      ]);
      return;
    }
    const args = JSON.stringify(steps[tools.length]);
    sse(res, [
      { choices: [{ delta: { tool_calls: [{ index: 0, id: `call_${tools.length + 1}`, function: { name: 'powershell', arguments: args } }] } }] },
      { choices: [{ delta: {}, finish_reason: 'tool_calls' }], usage },
    ]);
  });
}).listen(port, '127.0.0.1', () => console.log(`fake model on ${port}`));
