// Exits 0 when a run recorded something that reads as a flicker: a value
// that came back within 150 ms, ink that dropped and returned, or a frame
// whose pixels changed over more than a few rows.
import fs from 'node:fs';
const s = JSON.parse(fs.readFileSync(process.argv[2], 'utf8')).summary;
const n = s.transients.length + s.inkDips.length + s.sseDips.length + s.pixBig.length;
process.exit(n > 0 ? 0 : 1);
