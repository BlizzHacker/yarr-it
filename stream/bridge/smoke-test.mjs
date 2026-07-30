// Live smoke test for mw-bridge, run against the public endpoint.
// Node 22+ has a global WebSocket, so this needs no dependencies.
const BASE = process.env.BRIDGE || 'wss://yarrit.com/bridge/socket';

function dial(hello, { expectOpen = true, timeoutMs = 12000 } = {}) {
  return new Promise((resolve) => {
    const ws = new WebSocket(BASE);
    ws.binaryType = 'arraybuffer';
    const result = { ok: false, closeCode: null, first: null, bytes: 0 };
    const timer = setTimeout(() => { try { ws.close(); } catch {} resolve(result); }, timeoutMs);

    ws.onopen = () => ws.send(JSON.stringify(hello));
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        result.first = ev.data;
        if (ev.data.includes('"ok":true')) {
          result.ok = true;
          if (!expectOpen) { clearTimeout(timer); ws.close(); resolve(result); return; }
          // Send an HTTP request so a real server replies with real bytes.
          ws.send(new TextEncoder().encode('HEAD / HTTP/1.0\r\nHost: one.one.one.one\r\n\r\n'));
        }
      } else {
        result.bytes += ev.data.byteLength ?? ev.data.length ?? 0;
        clearTimeout(timer);
        try { ws.close(); } catch {}
        resolve(result);
      }
    };
    ws.onclose = (ev) => { result.closeCode = ev.code; clearTimeout(timer); resolve(result); };
    ws.onerror = () => {};
  });
}

const cases = [
  { name: 'relays to a public host (1.1.1.1:80)', hello: { proto: 'tcp', host: '1.1.1.1', port: 80 },
    check: (r) => r.ok && r.bytes > 0, want: 'handshake ok + bytes relayed back' },
  { name: 'REFUSES the WireGuard peer (10.10.10.1:25)', hello: { proto: 'tcp', host: '10.10.10.1', port: 25 },
    expectOpen: false, check: (r) => !r.ok, want: 'refused' },
  { name: 'REFUSES Prowlarr on the LAN (192.168.0.115:9696)', hello: { proto: 'tcp', host: '192.168.0.115', port: 9696 },
    expectOpen: false, check: (r) => !r.ok, want: 'refused' },
  { name: 'REFUSES loopback (127.0.0.1:8801)', hello: { proto: 'tcp', host: '127.0.0.1', port: 8801 },
    expectOpen: false, check: (r) => !r.ok, want: 'refused' },
  { name: 'REFUSES a hostname instead of an IP', hello: { proto: 'tcp', host: 'localhost', port: 25 },
    expectOpen: false, check: (r) => !r.ok, want: 'refused' },
];

let failed = 0;
for (const c of cases) {
  const r = await dial(c.hello, { expectOpen: c.expectOpen });
  const pass = c.check(r);
  if (!pass) failed++;
  console.log(
    `${pass ? 'PASS' : 'FAIL'}  ${c.name}\n      want=${c.want} got=ok:${r.ok} bytes:${r.bytes} close:${r.closeCode}`,
  );
}
console.log(failed === 0 ? '\nall bridge smoke tests passed' : `\n${failed} FAILED`);
process.exit(failed === 0 ? 0 : 1);
