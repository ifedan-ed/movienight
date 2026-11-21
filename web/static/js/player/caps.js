// What this browser can decode, measured rather than guessed from its name:
// the <video> element's canPlayType for the file itself, and for HLS the
// MediaSource that hls.js feeds (or Safari's own HLS). The server turns this
// into Jellyfin's device profile, so Jellyfin sends the file as it is when
// it can and remuxes or transcodes only what the browser can't play.

const VIDEO_MP4 = {
  h264: 'video/mp4; codecs="avc1.640028"',
  hevc: 'video/mp4; codecs="hvc1.1.6.L120.90"',
  av1: 'video/mp4; codecs="av01.0.08M.08"',
  vp9: 'video/mp4; codecs="vp09.00.40.08"',
};
const VIDEO_WEBM = { vp9: 'video/webm; codecs="vp9"', vp8: 'video/webm; codecs="vp8"', av1: 'video/webm; codecs="av01.0.08M.08"' };
const AUDIO = {
  aac: 'audio/mp4; codecs="mp4a.40.2"',
  mp3: "audio/mpeg",
  opus: 'audio/mp4; codecs="opus"',
  flac: 'audio/mp4; codecs="flac"',
  ac3: 'audio/mp4; codecs="ac-3"',
  eac3: 'audio/mp4; codecs="ec-3"',
  alac: 'audio/mp4; codecs="alac"',
  vorbis: 'audio/webm; codecs="vorbis"',
};

// Safari (and only Safari) plays HLS itself, and better than a script can
// there (AirPlay, battery, iPhone fullscreen). Chrome now answers "maybe" to
// HLS too, but hls.js on MediaSource is the steadier path there.
export function nativeHls(video) {
  return !!video.canPlayType("application/vnd.apple.mpegurl") && /Apple/.test(navigator.vendor || "");
}

export function mediaSource() {
  return window.ManagedMediaSource || window.MediaSource || null;
}

export function caps() {
  const v = document.createElement("video");
  const plays = (t) => v.canPlayType(t) !== "";
  const MS = mediaSource();
  const hlsPlays = nativeHls(v) ? plays : (t) => !!MS && MS.isTypeSupported(t);
  const pick = (table, test) => Object.keys(table).filter((k) => test(table[k]));
  const directVideo = new Set([...pick(VIDEO_MP4, plays), ...pick(VIDEO_WEBM, plays)]);
  return {
    direct_containers: ["mp4", "webm"].filter((c) => plays(`video/${c}`)),
    direct_video: [...directVideo],
    direct_audio: pick(AUDIO, plays),
    hls_video: pick(VIDEO_MP4, hlsPlays),
    hls_audio: pick(AUDIO, hlsPlays),
  };
}
