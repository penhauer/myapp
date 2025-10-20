/* eslint-env browser */
(() => {
  const WS_URL = 'ws://localhost:8080/ws'; // Go peer acts as WS signaling server + offerer
  let pc = null, ws = null, videoEl = null;

  const logEl = document.getElementById('log');
  const metricsEl = document.getElementById('metrics');
  const btn = document.getElementById('connectBtn');
  videoEl = document.getElementById('remote');

  const log = (m) => (logEl.textContent = m);

  btn.onclick = async () => {
    btn.disabled = true;
    try { await start(); log('connecting…'); } catch (e) { log('error: ' + e); btn.disabled = false; }
  };

  async function start() {
    // 1. WS connect to Go server
    ws = new WebSocket(WS_URL);
    ws.onopen = () => log('ws: open');
    ws.onclose = () => log('ws: closed');
    ws.onerror = (e) => log('ws: error');

    // 2. Create RTCPeerConnection (receiver; auto-answerer)
    pc = new RTCPeerConnection({
      iceServers: [
        { urls: 'stun:stun.l.google.com:19302' }
        // add TURN if needed
      ]
    });

    pc.onconnectionstatechange = () => log(`pc: ${pc.connectionState}`);
    pc.onicecandidate = (e) => { if (e.candidate) sendWS('ice', { candidate: e.candidate }); };
    pc.ontrack = (e) => { if (!videoEl.srcObject) { videoEl.srcObject = e.streams[0]; wireMetrics(videoEl); } };

    // 3. Handle signaling from server (offer + ice). Reply with answer + ice.
    ws.onmessage = async (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.type === 'offer') {
        await pc.setRemoteDescription(msg.sdp);
        const answer = await pc.createAnswer();
        await pc.setLocalDescription(answer);
        sendWS('answer', { sdp: pc.localDescription });
      } else if (msg.type === 'ice') {
        try { await pc.addIceCandidate(msg.candidate); } catch {}
      }
    };
  }

  function sendWS(type, payload) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type, ...payload }));
  }

  // -------- Receiver-side metrics --------
  let lastRtp = null, lastRender = null, startT = performance.now();
  let stallCount = 0, stallMs = 0, stallOpenAt = null;

  function wireMetrics(video) {
    const fmt = (x) => (x==null ? 'n/a' : (typeof x === 'number' ? (Number.isInteger(x) ? x : x.toFixed(2)) : String(x)));

    const updatePanel = (o) => {
      metricsEl.textContent =
`VIDEO (receiver)
 decoded FPS:   ${fmt(o.fpsDecoded)}
 rendered FPS:  ${fmt(o.fpsRendered)}
 bitrate:       ${fmt(o.bitrateMbps)} Mbps
 pkt loss:      ${fmt(o.lossRatePct)}%  (Δlost=${o.deltaLost}|Δrecv=${o.deltaRecv})
 jitter:        ${fmt(o.jitterMs)} ms
 dropped ratio: ${fmt(o.dropRatio)}  (dropped=${o.dropped}|total=${o.total})
 stalls:        ${o.stallCount} (time=${fmt(o.stallMs/1000)}s, ratio=${fmt(o.stallRatio)})
 decode avg:    ${fmt(o.decodeMsPerFrame)} ms/frame
`;
    };

    const onWaiting = () => { stallCount++; stallOpenAt = performance.now(); };
    const onPlaying = () => { if (stallOpenAt != null) { stallMs += performance.now() - stallOpenAt; stallOpenAt = null; } };
    video.addEventListener('waiting', onWaiting);
    video.addEventListener('playing', onPlaying);
    video.addEventListener('pause', onWaiting);
    video.addEventListener('play', onPlaying);

    setInterval(async () => {
      const out = { stallCount, stallMs: stallMs + (stallOpenAt ? (performance.now() - stallOpenAt) : 0) };

      // inbound-rtp stats
      try {
        const stats = await pc.getStats();
        stats.forEach(r => {
          if (r.type === 'inbound-rtp' && r.kind === 'video') {
            if (lastRtp) {
              const dt = (r.timestamp - lastRtp.timestamp) / 1000;
              if (dt > 0) {
                const dBytes = (r.bytesReceived - lastRtp.bytesReceived) >>> 0;
                const dRecv  = (r.packetsReceived - lastRtp.packetsReceived) >>> 0;
                const dLost  = (r.packetsLost    - lastRtp.packetsLost)    >>> 0;
                const fpsDec = (r.framesDecoded!=null && lastRtp.framesDecoded!=null)
                                ? (r.framesDecoded - lastRtp.framesDecoded) / dt
                                : (r.framesPerSecond ?? null);

                out.bitrateMbps = (dBytes * 8) / dt / 1e6;
                out.deltaRecv   = dRecv;
                out.deltaLost   = dLost;
                out.lossRatePct = (dRecv + dLost) > 0 ? (dLost * 100) / (dRecv + dLost) : 0;
                out.fpsDecoded  = fpsDec ?? null;
                out.jitterMs    = (r.jitter != null) ? (r.jitter * 1000) : null;

                if (r.totalDecodeTime != null && r.framesDecoded > 0) {
                  const dFrames = r.framesDecoded - (lastRtp.framesDecoded ?? 0);
                  const dDecode = r.totalDecodeTime - (lastRtp.totalDecodeTime ?? 0);
                  out.decodeMsPerFrame = (dFrames > 0 && dDecode >= 0) ? (dDecode / dFrames) * 1000 : null;
                }
              }
            }
            lastRtp = r;
          }
        });
      } catch {}

      // render metrics
      if (video.getVideoPlaybackQuality) {
        const q = video.getVideoPlaybackQuality();
        if (lastRender) {
          const dt = (performance.now() - lastRender.t) / 1000;
          const dRendered = q.totalVideoFrames - lastRender.total;
          out.fpsRendered = dt > 0 ? dRendered / dt : null;
        }
        out.total     = q.totalVideoFrames;
        out.dropped   = q.droppedVideoFrames;
        out.dropRatio = q.totalVideoFrames > 0 ? q.droppedVideoFrames / q.totalVideoFrames : 0;
        lastRender = { t: performance.now(), total: q.totalVideoFrames };
      }

      const elapsed = performance.now() - startT;
      out.stallRatio = elapsed > 0 ? (out.stallMs / elapsed) : 0;

      updatePanel(out);
    }, 1000);
  }
})();
