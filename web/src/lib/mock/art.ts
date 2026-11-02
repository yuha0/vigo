const palettes = [
  ["#efe6d8", "#d9a066", "#b5553c", "#3d405b", "#81a094"],
  ["#f2efe9", "#2b2d42", "#8d99ae", "#d90429", "#edf2f4"],
  ["#f4f1de", "#e07a5f", "#3d405b", "#81b29a", "#f2cc8f"],
  ["#1b1b1e", "#373f51", "#a9bcd0", "#d8dbe2", "#58a4b0"],
  ["#f6f0e6", "#c2a878", "#6c584c", "#a98467", "#283618"],
  ["#e9edc9", "#ccd5ae", "#d4a373", "#faedcd", "#5f6f52"],
  ["#0d1b2a", "#1b263b", "#415a77", "#778da9", "#e0e1dd"],
  ["#fefae0", "#bc6c25", "#dda15e", "#606c38", "#283618"],
  ["#f1e9da", "#2e294e", "#541388", "#ffd400", "#d90368"],
  ["#eae2b7", "#fcbf49", "#f77f00", "#d62828", "#003049"],
  ["#e8e4dc", "#9a8c98", "#4a4e69", "#22223b", "#c9ada7"],
  ["#f8f4ec", "#a3b18a", "#588157", "#3a5a40", "#344e41"],
];

function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return h >>> 0;
}

function rng(seed: number) {
  let a = seed;
  return () => {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const W = 320;
const H = 180;

function colorField(r: () => number, p: string[]) {
  const bands = 2 + Math.floor(r() * 2);
  let y = 14;
  const gap = 10;
  const h = (H - 28 - gap * (bands - 1)) / bands;
  let shapes = "";
  for (let i = 0; i < bands; i++) {
    const c = p[1 + Math.floor(r() * 4)];
    shapes += `<rect x="${30 + r() * 10}" y="${y}" width="${W - 70 - r() * 10}" height="${h * (0.85 + r() * 0.3)}" rx="4" fill="${c}" opacity="0.92" filter="url(#soft)"/>`;
    y += h + gap;
  }
  return `<rect width="${W}" height="${H}" fill="${p[3]}"/>${shapes}`;
}

function landscape(r: () => number, p: string[]) {
  const sunX = 60 + r() * 200;
  const sunY = 40 + r() * 40;
  let hills = "";
  for (let i = 0; i < 3; i++) {
    const base = 100 + i * 26 + r() * 10;
    const a = 8 + r() * 18;
    const ph = r() * Math.PI * 2;
    let d = `M0 ${H} L0 ${base}`;
    for (let x = 0; x <= W; x += 20) d += ` L${x} ${(base + Math.sin(x / (40 + i * 20) + ph) * a).toFixed(1)}`;
    d += ` L${W} ${H} Z`;
    hills += `<path d="${d}" fill="${p[(i + 2) % 5]}"/>`;
  }
  return `<defs><linearGradient id="sky" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${p[0]}"/><stop offset="1" stop-color="${p[4]}"/></linearGradient></defs>
<rect width="${W}" height="${H}" fill="url(#sky)"/><circle cx="${sunX}" cy="${sunY}" r="${14 + r() * 16}" fill="${p[1]}"/>${hills}`;
}

function bauhaus(r: () => number, p: string[]) {
  let shapes = "";
  const n = 4 + Math.floor(r() * 4);
  for (let i = 0; i < n; i++) {
    const c = p[1 + Math.floor(r() * 4)];
    const x = r() * W;
    const y = r() * H;
    const s = 20 + r() * 60;
    const k = r();
    if (k < 0.4) shapes += `<circle cx="${x}" cy="${y}" r="${s / 2}" fill="${c}"/>`;
    else if (k < 0.7) shapes += `<rect x="${x - s / 2}" y="${y - s / 2}" width="${s}" height="${s * (0.4 + r())}" fill="${c}" transform="rotate(${Math.floor(r() * 4) * 45} ${x} ${y})"/>`;
    else shapes += `<path d="M${x - s / 2} ${y} A${s / 2} ${s / 2} 0 0 1 ${x + s / 2} ${y} Z" fill="${c}"/>`;
  }
  return `<rect width="${W}" height="${H}" fill="${p[0]}"/>${shapes}`;
}

function arches(r: () => number, p: string[]) {
  const cx = 60 + r() * 200;
  let rings = "";
  for (let i = 6; i > 0; i--) rings += `<circle cx="${cx}" cy="${H + 10}" r="${i * 26}" fill="${p[i % 5]}"/>`;
  return `<rect width="${W}" height="${H}" fill="${p[0]}"/>${rings}`;
}

const styles = [colorField, landscape, bauhaus, arches];

const cache = new Map<string, string>();

/** A deterministic abstract "artwork" standing in for a thumbnail, at the given width/height ratio. */
export function generatedArtUrl(id: string, aspect = W / H): string {
  const key = `${id}@${aspect.toFixed(3)}`;
  const hit = cache.get(key);
  if (hit) return hit;
  const r = rng(hash(id));
  const p = palettes[Math.floor(r() * palettes.length)];
  const body = styles[Math.floor(r() * styles.length)](r, p);
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${W} ${H}" width="${Math.round(640 * Math.min(1, aspect))}" height="${Math.round(640 / Math.max(1, aspect))}" preserveAspectRatio="xMidYMid slice"><defs><filter id="soft"><feGaussianBlur stdDeviation="2.5"/></filter><filter id="grain"><feTurbulence type="fractalNoise" baseFrequency="0.9" numOctaves="2" stitchTiles="stitch"/><feColorMatrix values="0 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 0.07 0"/></filter></defs>${body}<rect width="${W}" height="${H}" filter="url(#grain)"/></svg>`;
  const url = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
  cache.set(key, url);
  return url;
}
