/* NetClone web UI - no framework, no external dependencies. */
(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  const S = {
    state: null,
    disks: [],
    mounts: [],
    parts: [],
    images: [],
    dir: "",
    disk: null,
    image: null,
    tab: "capture",
    autoSwitched: false,
    lastCapture: "",
    lastSession: "",
    lastLogs: 0,
  };

  const PHASE_TR = {
    starting: "hazırlanıyor",
    waiting: "istemci bekleniyor",
    sending: "gönderiliyor",
    done: "tamamlandı",
    aborted: "iptal edildi",
    failed: "hata",
    running: "çalışıyor",
    canceled: "iptal edildi",
  };
  const CLIENT_TR = {
    waiting: "bekliyor",
    writing: "yazılıyor",
    done: "tamamlandı",
    failed: "hata",
    late: "geç kaldı",
  };

  // ---------------------------------------------------------------- helpers
  function fmtBytes(n) {
    if (n === null || n === undefined || isNaN(n)) return "—";
    if (n < 1024) return `${n} B`;
    const units = ["KiB", "MiB", "GiB", "TiB", "PiB"];
    let v = n / 1024, i = 0;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`;
  }
  function fmtSpeed(n) {
    if (!n) return "—";
    return `${fmtBytes(n)}/s`;
  }
  function fmtDur(sec) {
    if (sec === null || sec === undefined || isNaN(sec) || sec < 0) return "—";
    sec = Math.round(sec);
    const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = sec % 60;
    if (h) return `${h}s ${m}dk`;
    if (m) return `${m}dk ${s}sn`;
    return `${s}sn`;
  }
  function el(html) {
    const t = document.createElement("template");
    t.innerHTML = html.trim();
    return t.content.firstElementChild;
  }
  function escapeHtml(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }
  function ipv4ToInt(ip) {
    const p = String(ip).split(".").map(Number);
    if (p.length !== 4 || p.some((x) => isNaN(x) || x < 0 || x > 255)) return null;
    return ((p[0] << 24) | (p[1] << 16) | (p[2] << 8) | p[3]) >>> 0;
  }
  function intToIpv4(n) {
    return [(n >>> 24) & 255, (n >>> 16) & 255, (n >>> 8) & 255, n & 255].join(".");
  }
  function subnetOf(ip, mask) {
    const a = ipv4ToInt(ip), m = ipv4ToInt(mask);
    if (a === null || m === null) return "—";
    const ones = m.toString(2).split("").filter((c) => c === "1").length;
    return `${intToIpv4((a & m) >>> 0)}/${ones}`;
  }

  let toastTimer = null;
  function toast(msg, kind) {
    const t = $("#toast");
    t.textContent = msg;
    t.className = "toast show" + (kind ? " " + kind : "");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { t.className = "toast"; }, 4200);
  }

  async function api(path, opts) {
    const res = await fetch(path, Object.assign({
      headers: { "Content-Type": "application/json" },
    }, opts || {}));
    let body = {};
    try { body = await res.json(); } catch (_) { /* empty */ }
    if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
    return body;
  }

  // ------------------------------------------------------------------- tabs
  function switchTab(name) {
    S.tab = name;
    $$(".tabs button").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
    $$(".tab").forEach((sec) => sec.classList.toggle("active", sec.id === `tab-${name}`));
    if (name === "deploy" && !S.images.length && S.dir) refreshImages(S.dir);
    if (name === "capture" && !S.disks.length) refreshDisks();
    if (name === "monitor") scrollLogs(true);
  }

  function bindUI() {
    $$(".tabs button").forEach((b) => b.addEventListener("click", () => switchTab(b.dataset.tab)));

    $("#refreshDisks").addEventListener("click", refreshDisks);
    $("#refreshMounts").addEventListener("click", refreshMounts);
    $("#scanImages").addEventListener("click", () => refreshImages($("#imageDir").value.trim()));
    $("#destDir").addEventListener("input", updateFreeSpace);
    $("#expectedMinus").addEventListener("click", () => bumpExpected(-1));
    $("#expectedPlus").addEventListener("click", () => bumpExpected(1));
    $("#startCapture").addEventListener("click", startCapture);
    $("#cancelCapture").addEventListener("click", cancelCapture);
    $("#startSession").addEventListener("click", startSession);
    $("#abortSession").addEventListener("click", abortSession);
    $("#forceStart").addEventListener("click", forceStart);
    $("#closeSession").addEventListener("click", closeSession);
    $("#imageDir").addEventListener("keydown", (e) => {
      if (e.key === "Enter") refreshImages($("#imageDir").value.trim());
    });
    $("#destDir").addEventListener("keydown", (e) => {
      if (e.key === "Enter") updateFreeSpace();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "F5") e.preventDefault();
    });
  }

  function bumpExpected(delta) {
    const input = $("#expected");
    let v = parseInt(input.value || "1", 10);
    if (isNaN(v)) v = 1;
    v = Math.min(254, Math.max(1, v + delta));
    input.value = v;
  }

  // -------------------------------------------------------------- discovery
  async function refreshDisks() {
    try {
      const r = await api("/api/disks");
      S.disks = r.disks || [];
      renderDisks();
    } catch (e) { toast(`Diskler okunamadı: ${e.message}`, "err"); }
  }

  function renderDisks() {
    const box = $("#diskList");
    box.innerHTML = "";
    if (!S.disks.length) {
      box.appendChild(el(`<div class="empty">Disk bulunamadı.</div>`));
      return;
    }
    S.disks.forEach((d) => {
      const disabled = d.bootMedia || d.readonly;
      const node = el(`
        <div class="item ${disabled ? "disabled" : ""} ${S.disk && S.disk.path === d.path ? "selected" : ""}">
          <div class="icon">${d.transport === "nvme" ? "▦" : d.removable ? "⌁" : "▤"}</div>
          <div class="meta">
            <b>${escapeHtml(d.path)} ${d.model ? "· " + escapeHtml(d.model) : ""}</b>
            <span>${escapeHtml(d.scheme || "?")} · ${escapeHtml(d.transport || "disk")} · ${escapeHtml(d.sector)} B sektör
              ${d.bootMedia ? '<span class="badge warn">sistem</span>' : ""}
              ${d.readonly ? '<span class="badge warn">salt okunur</span>' : ""}
              ${d.mounts && d.mounts.length ? '<span class="badge">bağlı</span>' : ""}
            </span>
          </div>
          <div class="size">${fmtBytes(d.size)}</div>
        </div>`);
      if (!disabled) {
        node.addEventListener("click", () => {
          S.disk = d;
          $$("#diskList .item").forEach((n) => n.classList.remove("selected"));
          node.classList.add("selected");
          if (!$("#imageName").value.trim()) {
            const stamp = new Date().toISOString().slice(0, 10).replace(/-/g, "");
            $("#imageName").value = `imaj-${d.name}-${stamp}`;
          }
        });
      }
      box.appendChild(node);
    });
    if (!S.disk) {
      const first = S.disks.find((d) => !d.bootMedia && !d.readonly);
      if (first) {
        S.disk = first;
        const node = $$("#diskList .item")[S.disks.indexOf(first)];
        if (node) node.classList.add("selected");
      }
    }
  }

  async function refreshMounts() {
    try {
      const [mr, pr] = await Promise.all([api("/api/mounts"), api("/api/partitions")]);
      S.mounts = mr.mounts || [];
      const skip = ["swap", "crypto_LUKS", "LVM2_member", "linux_raid_member", "iso9660", ""];
      S.parts = (pr.devices || []).filter((d) => (d.type === "part" || d.type === "disk") && !d.mountpoint && d.fstype && !skip.includes(d.fstype));
      renderMounts();
      renderParts();
      const current = $("#destDir").value.trim();
      if (!current) {
        const best = S.mounts.filter((m) => !m.path.startsWith("/run") && !m.path.startsWith("/var/lib/netclone"))
          .sort((a, b) => b.free - a.free)[0];
        if (best) {
          $("#destDir").value = best.path;
          updateFreeSpace();
        }
      }
    } catch (e) { toast(`Konumlar okunamadı: ${e.message}`, "err"); }
  }

  function renderMounts() {
    const box = $("#mountList");
    box.innerHTML = "";
    if (!S.mounts.length) {
      box.appendChild(el(`<div class="empty">Bağlı disk yok. Aşağıdan bir disk bağlayın.</div>`));
      return;
    }
    S.mounts.forEach((m) => {
      const node = el(`
        <div class="item">
          <div class="icon">▣</div>
          <div class="meta">
            <b>${escapeHtml(m.path)}</b>
            <span>${escapeHtml(m.dev)} · ${escapeHtml(m.fstype)}</span>
          </div>
          <div class="size">${fmtBytes(m.free)} boş</div>
        </div>`);
      node.addEventListener("click", () => {
        $("#destDir").value = m.path;
        updateFreeSpace();
      });
      if (m.path.startsWith("/media/")) {
        const btn = el(`<button class="ghost small" title="Bağlantıyı kes">Ayır</button>`);
        btn.addEventListener("click", async (ev) => {
          ev.stopPropagation();
          try {
            await api("/api/unmount", { method: "POST", body: JSON.stringify({ path: m.path }) });
            toast(`${m.path} ayrıldı.`, "ok");
            await refreshMounts();
          } catch (err) { toast(err.message, "err"); }
        });
        node.appendChild(btn);
      }
      box.appendChild(node);
    });
  }

  function renderParts() {
    const box = $("#partList");
    box.innerHTML = "";
    if (!S.parts.length) {
      box.appendChild(el(`<div class="empty">Bağlanacak disk yok.</div>`));
      return;
    }
    S.parts.forEach((p) => {
      const node = el(`
        <div class="item">
          <div class="icon">＋</div>
          <div class="meta">
            <b>${escapeHtml(p.path)} ${p.label ? "· " + escapeHtml(p.label) : ""}</b>
            <span>${escapeHtml(p.fstype)} · ${escapeHtml(p.tran || "disk")} ${p.model ? "· " + escapeHtml(p.model) : ""}</span>
          </div>
          <div class="size">${fmtBytes(p.size)}</div>
        </div>`);
      const btn = el(`<button class="ghost small">Bağla</button>`);
      btn.addEventListener("click", async (ev) => {
        ev.stopPropagation();
        btn.disabled = true;
        try {
          const r = await api("/api/mount", { method: "POST", body: JSON.stringify({ device: p.path }) });
          toast(`${p.path} → ${r.path}`, "ok");
          $("#destDir").value = r.path;
          await refreshMounts();
          updateFreeSpace();
        } catch (err) {
          toast(err.message, "err");
          btn.disabled = false;
        }
      });
      node.appendChild(btn);
      box.appendChild(node);
    });
  }

  function freeSpaceFor(path) {
    if (!path) return null;
    let best = null;
    S.mounts.forEach((m) => {
      if (path === m.path || path.startsWith(m.path.replace(/\/$/, "") + "/")) {
        if (!best || m.path.length > best.path.length) best = m;
      }
    });
    return best;
  }

  function updateFreeSpace() {
    const path = $("#destDir").value.trim();
    const m = freeSpaceFor(path);
    $("#freeSpace").textContent = m ? `${fmtBytes(m.free)} / ${fmtBytes(m.total)}` : "bilinmiyor";
  }

  async function refreshImages(dir) {
    if (!dir) { toast("Önce imaj klasörünü girin.", "err"); return; }
    try {
      const r = await api(`/api/images?dir=${encodeURIComponent(dir)}`);
      S.dir = r.dir;
      S.images = r.images || [];
      $("#imageDir").value = r.dir;
      renderImages();
    } catch (e) { toast(`İmajlar okunamadı: ${e.message}`, "err"); }
  }

  function renderImages() {
    const box = $("#imageList");
    box.innerHTML = "";
    if (!S.images.length) {
      box.appendChild(el(`<div class="empty">Bu klasörde imaj yok. Önce “İmaj Al” adımını tamamlayın.</div>`));
      return;
    }
    S.images.forEach((img) => {
      const node = el(`
        <div class="item ${S.image && S.image.path === img.path ? "selected" : ""}">
          <div class="icon">◫</div>
          <div class="meta">
            <b>${escapeHtml(img.name)}</b>
            <span>${escapeHtml(img.source || "?")} · ${escapeHtml(img.scheme || "?")} · ${new Date(img.created || img.modified).toLocaleString("tr-TR")}
              <span class="badge ok">${fmtBytes(img.size)}</span>
            </span>
          </div>
          <div class="size">${fmtBytes(img.diskSize)}</div>
        </div>`);
      node.addEventListener("click", () => {
        S.image = img;
        $$("#imageList .item").forEach((n) => n.classList.remove("selected"));
        node.classList.add("selected");
      });
      box.appendChild(node);
    });
    if (!S.image) {
      const first = S.images[0];
      if (first) {
        S.image = first;
        const node = $$("#imageList .item")[0];
        if (node) node.classList.add("selected");
      }
    }
  }

  // ---------------------------------------------------------------- capture
  async function startCapture() {
    if (!S.disk) { toast("Kaynak disk seçin.", "err"); return; }
    const destDir = $("#destDir").value.trim();
    if (!destDir) { toast("Hedef klasör girin.", "err"); return; }
    const name = $("#imageName").value.trim();
    const level = parseInt($("#level").value, 10) || 1;
    const ok = confirm(
      `Kaynak disk: ${S.disk.path} (${fmtBytes(S.disk.size)})\n` +
      `Hedef: ${destDir}\n\nİmaj alınırken disk okunur, hiçbir veri değiştirilmez. Devam edilsin mi?`);
    if (!ok) return;
    try {
      $("#startCapture").disabled = true;
      await api("/api/capture", {
        method: "POST",
        body: JSON.stringify({ source: S.disk.path, destDir, name, level }),
      });
      toast("İmaj alma işlemi başladı.", "ok");
    } catch (e) {
      toast(`Başlatılamadı: ${e.message}`, "err");
    } finally {
      $("#startCapture").disabled = false;
    }
  }

  async function cancelCapture() {
    try {
      await api("/api/capture/cancel", { method: "POST", body: "{}" });
      toast("İptal isteği gönderildi.", "ok");
    } catch (e) { toast(e.message, "err"); }
  }

  function applyCapture(c) {
    const card = $("#captureProgress");
    if (!c) {
      card.hidden = true;
      return;
    }
    card.hidden = false;
    $("#capTitle").textContent = c.name || "İmaj";
    const pill = $("#capPhase");
    pill.textContent = PHASE_TR[c.phase] || c.phase;
    pill.className = "pill " + (c.phase === "done" ? "ok" : c.phase === "failed" || c.phase === "canceled" ? "err" : "warn");

    const pct = c.sourceSize > 0 ? Math.min(100, (c.bytesRead / c.sourceSize) * 100) : 0;
    const bar = $("#capBar");
    bar.style.width = `${pct.toFixed(1)}%`;
    bar.className = c.phase === "failed" || c.phase === "canceled" ? "fail" : "";

    $("#capRead").textContent = `${fmtBytes(c.bytesRead)} / ${fmtBytes(c.sourceSize)} (${pct.toFixed(1)}%)`;
    $("#capOut").textContent = fmtBytes(c.outBytes);
    $("#capSpeed").textContent = fmtSpeed(c.speed);
    const end = c.finishedAt ? new Date(c.finishedAt).getTime() : Date.now();
    $("#capTime").textContent = fmtDur((end - new Date(c.startedAt).getTime()) / 1000);
    $("#cancelCapture").disabled = c.phase !== "running";

    if (c.phase !== S.lastCapture) {
      if (c.phase === "done") {
        toast("İmaj hazır.", "ok");
        refreshImages(c.destDir).then(() => {
          const hit = S.images.find((i) => i.path === c.imagePath);
          if (hit) S.image = hit;
          renderImages();
          switchTab("deploy");
        });
      } else if (c.phase === "failed") {
        toast(`İmaj alınamadı: ${c.error || "bilinmeyen hata"}`, "err");
      } else if (c.phase === "canceled") {
        toast("İmaj alma iptal edildi.", "err");
      }
      S.lastCapture = c.phase;
    }
  }

  // ---------------------------------------------------------------- session
  async function startSession() {
    if (!S.image) { toast("Bir imaj seçin.", "err"); return; }
    const expected = parseInt($("#expected").value, 10);
    const iface = $("#iface").value;
    const dhcpMode = $("#dhcpMode").value;
    const policy = $("#policy").value;
    if (!(expected >= 1 && expected <= 254)) { toast("Beklenen bilgisayar sayısı 1-254 arası olmalı.", "err"); return; }
    if (!iface) { toast("Ağ arayüzü seçin.", "err"); return; }

    const ok = confirm(
      `İmaj: ${S.image.name}\n` +
      `Beklenen bilgisayar: ${expected}\n` +
      `Ağ: ${iface}\n` +
      `DHCP: ${dhcpMode === "server" ? "bu bilgisayar sunucu" : "proxyDHCP"}\n\n` +
      `Bağlanan ${expected} bilgisayarın diski silinecek. Devam edilsin mi?`);
    if (!ok) return;

    try {
      await api("/api/session", {
        method: "POST",
        body: JSON.stringify({ imagePath: S.image.path, expected, iface, dhcpMode, policy, portBase: 9100 }),
      });
      S.autoSwitched = true;
      switchTab("monitor");
      toast("Oturum açıldı. İstemcileri PXE ile boot edin.", "ok");
    } catch (e) {
      toast(`Oturum açılamadı: ${e.message}`, "err");
    }
  }

  async function abortSession() {
    if (!confirm("Dağıtım iptal edilsin mi? Bağlı istemciler duracak.")) return;
    try {
      await api("/api/session/abort", { method: "POST", body: "{}" });
      toast("Oturum iptal edildi.", "ok");
    } catch (e) { toast(e.message, "err"); }
  }

  async function forceStart() {
    try {
      await api("/api/session/start", { method: "POST", body: "{}" });
      toast("Multicast hemen başlatılıyor.", "ok");
    } catch (e) { toast(e.message, "err"); }
  }

  async function closeSession() {
    try {
      await api("/api/session/close", { method: "POST", body: "{}" });
      S.autoSwitched = false;
      toast("Oturum kapatıldı.", "ok");
    } catch (e) { toast(e.message, "err"); }
  }

  function applySession(s) {
    const count = $("#monCount");
    const phase = $("#monPhase");
    const bar = $("#monBar");
    const grid = $("#clientGrid");

    if (!s) {
      count.textContent = "0 / 0";
      phase.textContent = "oturum yok";
      phase.className = "pill";
      $("#monImage").textContent = "—";
      $("#monElapsed").textContent = "";
      bar.style.width = "0%";
      grid.innerHTML = `<div class="empty">Henüz bağlı bilgisayar yok. İstemcileri PXE ile boot edin.</div>`;
      $("#forceStart").disabled = true;
      $("#abortSession").disabled = true;
      $("#closeSession").disabled = true;
      S.lastSession = "";
      return;
    }

    count.textContent = `${s.connected} / ${s.expected}`;
    phase.textContent = PHASE_TR[s.phase] || s.phase;
    phase.className = "pill " + (s.phase === "done" ? "ok" : s.phase === "sending" || s.phase === "waiting" ? "warn" : s.phase === "failed" ? "err" : "");
    $("#monImage").textContent = `${s.imageName} · ${fmtBytes(s.imageSize)} · ${s.policy}`;
    const start = new Date(s.startedAt || s.createdAt).getTime();
    const end = s.finishedAt ? new Date(s.finishedAt).getTime() : Date.now();
    $("#monElapsed").textContent = `${fmtDur((end - start) / 1000)} · ${s.serverIp} · ${s.iface}`;
    bar.style.width = `${Math.max(0, Math.min(100, s.percent || 0)).toFixed(1)}%`;
    bar.className = s.phase === "failed" ? "fail" : "";

    $("#forceStart").disabled = !(s.phase === "waiting" && s.connected > 0);
    $("#abortSession").disabled = !(s.phase === "waiting" || s.phase === "sending" || s.phase === "starting");
    $("#closeSession").disabled = !(s.phase === "done" || s.phase === "aborted" || s.phase === "failed");

    if (s.clients.length) {
      grid.innerHTML = "";
      s.clients.forEach((c) => {
        const pct = c.phase === "done" ? 100 : Math.max(0, Math.min(100, c.percent || 0));
        const cls = c.phase === "failed" ? "fail" : "";
        const node = el(`
          <div class="client">
            <div class="client-top">
              <b>${escapeHtml(c.hostname || c.mac)}</b>
              <span>${escapeHtml(CLIENT_TR[c.phase] || c.phase)}</span>
            </div>
            <div class="client-sub">${escapeHtml(c.mac)} · ${escapeHtml(c.disk || "?")} · ${fmtBytes(c.diskSize)}</div>
            <div class="bar"><div class="${cls}" style="width:${pct.toFixed(1)}%"></div></div>
            <div class="client-foot">
              <span>${pct.toFixed(1)}%</span>
              <span>${c.phase === "failed" && c.error ? escapeHtml(c.error) : c.phase === "done" ? fmtDur(c.seconds) : fmtSpeed(c.speed)}</span>
            </div>
          </div>`);
        grid.appendChild(node);
      });
    } else {
      grid.innerHTML = `<div class="empty">Henüz bağlı bilgisayar yok. İstemcileri PXE ile boot edin.</div>`;
    }

    if (s.phase !== S.lastSession) {
      if (s.phase === "sending" && S.lastSession === "waiting") toast("Multicast başladı.", "ok");
      if (s.phase === "done") toast("Dağıtım tamamlandı.", "ok");
      if (s.phase === "failed") toast(`Dağıtım hatası: ${s.error || ""}`, "err");
      S.lastSession = s.phase;
    }
  }

  function applyInterfaces(ifaces) {
    const sel = $("#iface");
    const wanted = ifaces.map((i) => i.name).join(",");
    if (sel.dataset.list === wanted) return;
    sel.dataset.list = wanted;
    const prev = sel.value;
    sel.innerHTML = "";
    ifaces.forEach((i) => {
      const opt = document.createElement("option");
      opt.value = i.name;
      opt.textContent = `${i.name} — ${i.address}/${i.mask}${i.default ? " (varsayılan)" : ""}`;
      sel.appendChild(opt);
    });
    if (prev && ifaces.some((i) => i.name === prev)) sel.value = prev;
    else {
      const def = ifaces.find((i) => i.default) || ifaces[0];
      if (def) sel.value = def.name;
    }
    updatePxeInfo();
  }

  function updatePxeInfo() {
    const sel = $("#iface");
    const ifaces = (S.state && S.state.interfaces) || [];
    const cur = ifaces.find((i) => i.name === sel.value) || ifaces[0];
    $("#pxeIp").textContent = cur ? cur.address : "—";
    $("#pxeIface").textContent = cur ? `${cur.name} (${subnetOf(cur.address, cur.mask)})` : "—";
    $("#pxePool").textContent = cur ? `${subnetOf(cur.address, cur.mask)} · otomatik` : "otomatik";
  }

  // ----------------------------------------------------------------- logging
  function scrollLogs(force) {
    const pre = $("#logView");
    const nearBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 40;
    if (force || nearBottom) pre.scrollTop = pre.scrollHeight;
  }

  function applyLogs(lines) {
    if (!lines) return;
    const pre = $("#logView");
    const text = lines.join("\n");
    if (pre.textContent !== text) {
      const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 60;
      pre.textContent = text;
      if (atBottom) pre.scrollTop = pre.scrollHeight;
    }
  }

  // ------------------------------------------------------------------- state
  function applyState(st) {
    S.state = st;
    $("#hostName").textContent = st.hostname || "—";
    const ifaces = st.interfaces || [];
    const def = ifaces.find((i) => i.default) || ifaces[0];
    $("#hostIp").textContent = def ? `${def.name} · ${def.address}` : "ağ yok";

    applyInterfaces(ifaces);
    applyCapture(st.capture || null);
    applySession(st.session || null);
    applyLogs(st.logs);

    if (!S.dir && st.imagesDir) {
      S.dir = st.imagesDir;
      $("#imageDir").value = st.imagesDir;
    }
    if (!S.dir && st.workDir) {
      $("#imageDir").value = st.workDir;
    }

    const ses = st.session;
    if (ses && ["waiting", "sending", "starting"].includes(ses.phase) && !S.autoSwitched && S.tab !== "monitor") {
      S.autoSwitched = true;
      switchTab("monitor");
    }
  }

  async function poll() {
    try {
      const st = await api("/api/state");
      applyState(st);
    } catch (_) {
      $("#hostIp").textContent = "bağlantı yok";
    }
  }

  async function boot() {
    bindUI();
    try {
      const st = await api("/api/state");
      applyState(st);
    } catch (e) {
      toast(`Sunucuya ulaşılamadı: ${e.message}`, "err");
    }
    await refreshDisks();
    await refreshMounts();
    updateFreeSpace();
    poll();
    setInterval(poll, 1200);
  }

  boot();
})();
