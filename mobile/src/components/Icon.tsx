// The web app's icon set (24px line icons), drawn with react-native-svg.
import { SvgXml } from "react-native-svg";

const P: Record<string, string> = {
  home: '<path d="M3 10.5 12 3l9 7.5"/><path d="M5 9v12h14V9"/><path d="M10 21v-6h4v6"/>',
  compass: '<circle cx="12" cy="12" r="9"/><path d="m15.6 8.4-2.1 5.1-5.1 2.1 2.1-5.1z"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.6-3.6"/>',
  lists: '<path d="M9 6h12M9 12h12M9 18h12"/><path d="M4 6h.01M4 12h.01M4 18h.01" stroke-width="3"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M4 21c0-4.2 3.6-7 8-7s8 2.8 8 7"/>',
  play: '<path d="M7 4.6v14.8L19.5 12z" fill="currentColor" stroke="none"/>',
  download: '<path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 20h14"/>',
  check: '<path d="m4.5 12.5 5 5 10-11"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3.2 2"/>',
  calendar: '<rect x="3" y="5" width="18" height="16" rx="2.5"/><path d="M3 10h18M8 3v4M16 3v4"/>',
  bookmark: '<path d="M6 3h12v18l-6-4.2L6 21z"/>',
  bookmarkFill: '<path d="M6 3h12v18l-6-4.2L6 21z" fill="currentColor"/>',
  star: '<path d="m12 2.8 2.8 5.8 6.3.9-4.6 4.4 1.1 6.3L12 17.2l-5.6 3 1.1-6.3-4.6-4.4 6.3-.9z"/>',
  starFill: '<path d="m12 2.8 2.8 5.8 6.3.9-4.6 4.4 1.1 6.3L12 17.2l-5.6 3 1.1-6.3-4.6-4.4 6.3-.9z" fill="currentColor"/>',
  heart: '<path d="M12 20.3s-7.6-4.6-9.6-9.2C.9 7.6 3.1 4 6.7 4c2.2 0 3.6 1.2 5.3 3.1C13.7 5.2 15.1 4 17.3 4c3.6 0 5.8 3.6 4.3 7.1-2 4.6-9.6 9.2-9.6 9.2z"/>',
  heartFill: '<path d="M12 20.3s-7.6-4.6-9.6-9.2C.9 7.6 3.1 4 6.7 4c2.2 0 3.6 1.2 5.3 3.1C13.7 5.2 15.1 4 17.3 4c3.6 0 5.8 3.6 4.3 7.1-2 4.6-9.6 9.2-9.6 9.2z" fill="currentColor"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  listPlus: '<path d="M3 6h12M3 12h12M3 18h7"/><path d="M18 13v8M14 17h8"/>',
  right: '<path d="m9 5 7 7-7 7"/>',
  left: '<path d="m15 5-7 7 7 7"/>',
  down: '<path d="m5 9 7 7 7-7"/>',
  x: '<path d="M6 6l12 12M18 6 6 18"/>',
  sliders: '<path d="M4 7h9M17 7h3M4 12h3M11 12h9M4 17h11M19 17h1"/><circle cx="15" cy="7" r="2"/><circle cx="9" cy="12" r="2"/><circle cx="17" cy="17" r="2"/>',
  refresh: '<path d="M20 11.5A8 8 0 1 1 17.7 6"/><path d="M20 4v6h-6"/>',
  external: '<path d="M14 4h6v6"/><path d="M20 4 11 13"/><path d="M18 14v6H4V6h6"/>',
  film: '<rect x="3" y="4" width="18" height="16" rx="2.5"/><path d="M7.5 4v16M16.5 4v16M3 9h4.5M3 15h4.5M16.5 9H21M16.5 15H21"/>',
  tv: '<rect x="3" y="6.5" width="18" height="12.5" rx="2.5"/><path d="m8.5 2.5 3.5 4 3.5-4"/>',
  sparkles: '<path d="M11 3.5 12.9 8l4.6 1.9-4.6 1.9L11 16.4l-1.9-4.6L4.5 9.9 9.1 8z"/><path d="M18.5 14.5l.9 2.1 2.1.9-2.1.9-.9 2.1-.9-2.1-2.1-.9 2.1-.9z"/>',
  trash: '<path d="M4 7h16M9.5 7V4h5v3M6.5 7l1 13h9l1-13"/>',
  more: '<circle cx="5" cy="12" r="1.7" fill="currentColor" stroke="none"/><circle cx="12" cy="12" r="1.7" fill="currentColor" stroke="none"/><circle cx="19" cy="12" r="1.7" fill="currentColor" stroke="none"/>',
  eye: '<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7.5v.01"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/><path d="m13.5 6.5 4 4"/>',
  tick: '<circle cx="12" cy="12" r="9.5"/><path d="m7.5 12.3 3 3 6-6.6"/>',
  tickFill: '<circle cx="12" cy="12" r="10" fill="currentColor" stroke="none"/><path d="m7.5 12.3 3 3 6-6.6" stroke="#0d0f14" stroke-width="2.4"/>',
  plug: '<path d="M9 3v5M15 3v5"/><path d="M6 8h12v3a6 6 0 0 1-12 0z"/><path d="M12 17v4"/>',
  sort: '<path d="M7 4v16M3.5 16.5 7 20l3.5-3.5"/><path d="M13 6h8M13 11h6M13 16h4"/>',
  copy: '<rect x="8" y="8" width="12" height="12" rx="2.5"/><path d="M16 8V5.5A1.5 1.5 0 0 0 14.5 4h-9A1.5 1.5 0 0 0 4 5.5v9A1.5 1.5 0 0 0 5.5 16H8"/>',
  subtitles: '<rect x="3" y="5" width="18" height="14" rx="2.5"/><path d="M7 15h4M13 15h4M7 11h2M11 11h6"/>',
  phone: '<rect x="6.5" y="2.5" width="11" height="19" rx="2.5"/><path d="M11 18h2"/>',
  key: '<circle cx="8" cy="15" r="4.5"/><path d="m11.2 11.8 8.8-8.8M17 6l2.5 2.5M14.5 8.5 16.5 10.5"/>',
  mail: '<rect x="3" y="5" width="18" height="14" rx="2.5"/><path d="m3.5 6.5 8.5 7 8.5-7"/>',
};

export type IconName = keyof typeof P;

export function Icon({ name, size = 24, color = "#f3f4f7", strokeWidth = 2 }: { name: IconName; size?: number; color?: string; strokeWidth?: number }) {
  const xml = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="${strokeWidth}" stroke-linecap="round" stroke-linejoin="round">${P[name] || ""}</svg>`;
  return <SvgXml xml={xml} width={size} height={size} color={color} />;
}
