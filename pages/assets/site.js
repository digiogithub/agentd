// agentd website behaviour: copy buttons, scroll reveal, OS detection and the download links of
// the latest GitHub release. Everything degrades gracefully: without JavaScript, or when the
// GitHub API is unreachable or rate limited, every download link points at the releases page.
(() => {
  "use strict";

  const REPO = "digiogithub/agentd";
  const APP_REPO = "digiogithub/agentd-app"; // Android app releases live in their own repo
  const lang = (document.documentElement.lang || "en").slice(0, 2);
  const T = {
    en: { copy: "copy", copied: "copied!", released: "released" },
    es: { copy: "copiar", copied: "¡copiado!", released: "publicada el" },
  }[lang] || { copy: "copy", copied: "copied!", released: "released" };

  document.documentElement.classList.add("js");

  // Copy buttons copy the text of the element named in data-copy (prompt markers excluded).
  for (const btn of document.querySelectorAll("[data-copy]")) {
    btn.textContent = T.copy;
    btn.addEventListener("click", async () => {
      const src = document.getElementById(btn.dataset.copy);
      if (!src) return;
      const clone = src.cloneNode(true);
      clone.querySelectorAll(".p, .c, .copy").forEach((n) => n.remove());
      try {
        await navigator.clipboard.writeText(clone.textContent.replace(/\n{2,}/g, "\n").trim());
        btn.textContent = T.copied;
        btn.classList.add("done");
        setTimeout(() => {
          btn.textContent = T.copy;
          btn.classList.remove("done");
        }, 1600);
      } catch {
        /* clipboard blocked: the text stays selectable */
      }
    });
  }

  // Reveal sections as they scroll into view.
  const reveals = document.querySelectorAll(".reveal");
  if ("IntersectionObserver" in window) {
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) {
            e.target.classList.add("in");
            io.unobserve(e.target);
          }
        }
      },
      { rootMargin: "0px 0px -8% 0px" },
    );
    reveals.forEach((el) => io.observe(el));
  } else {
    reveals.forEach((el) => el.classList.add("in"));
  }

  // Highlight the visitor's operating system.
  const ua = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.userAgent || "";
  const os = /android/i.test(ua) ? "android" : /mac|iphone|ipad/i.test(ua) ? "macos" : /linux|x11/i.test(ua) ? "linux" : "";
  if (os) document.querySelectorAll(`.parcel[data-os="${os}"]`).forEach((el) => el.classList.add("detected"));

  // Fill the download links from the latest release. Asset names carry the version, so each link
  // names a pattern (data-asset) instead of a file. The CLI and the desktop app come from REPO, the
  // Android APKs from APP_REPO (a second fetch; each falls back to its releases page on its own).
  const PATTERNS = {
    "deb-amd64": /^agentd_[^_]+_amd64\.deb$/,
    "deb-arm64": /^agentd_[^_]+_arm64\.deb$/,
    "tar-amd64": /^agentd_[^_]+_linux_amd64\.tar\.gz$/,
    "tar-arm64": /^agentd_[^_]+_linux_arm64\.tar\.gz$/,
    pkg: /^agentd_[^_]+_darwin_universal\.pkg$/,
    zip: /^agentd_[^_]+_darwin_universal\.zip$/,
    sums: /^SHA256SUMS$/,
    // desktop app
    "full-amd64": /^agentd-full_[^_]+_amd64\.deb$/,
    "full-arm64": /^agentd-full_[^_]+_arm64\.deb$/,
    "desk-tar-amd64": /^agentd-desktop_[^_]+_linux_amd64\.tar\.gz$/,
    "desk-tar-arm64": /^agentd-desktop_[^_]+_linux_arm64\.tar\.gz$/,
    "desk-zip": /^agentd-desktop_[^_]+_darwin_universal\.zip$/,
  };
  const APP_PATTERNS = {
    "apk-arm64": /^agentd-app-[^-]+-arm64-v8a\.apk$/,
    "apk-armv7": /^agentd-app-[^-]+-armeabi-v7a\.apk$/,
    "apk-x86_64": /^agentd-app-[^-]+-x86_64\.apk$/,
    "app-sums": /^SHA256SUMS$/,
  };
  const size = (b) => (b < 1 << 10 ? `${b} B` : b < 1 << 20 ? `${Math.round(b / 1024)} KB` : `${(b / (1 << 20)).toFixed(1)} MB`);

  // attrs names the data attributes of one release: tag ("version"), bare version ("ver"), date, notes.
  function fill(repo, patterns, attrs) {
    return fetch(`https://api.github.com/repos/${repo}/releases/latest`, { headers: { Accept: "application/vnd.github+json" } })
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((rel) => {
        const version = String(rel.tag_name || "").replace(/^v/, "");
        if (!version) return;
        for (const el of document.querySelectorAll(`[${attrs.tag}]`)) el.textContent = `v${version}`;
        for (const el of document.querySelectorAll(`[${attrs.ver}]`)) el.textContent = version;
        const date = rel.published_at ? new Date(rel.published_at) : null;
        if (date && !isNaN(date)) {
          const fmt = date.toLocaleDateString(lang, { year: "numeric", month: "short", day: "numeric" });
          for (const el of document.querySelectorAll(`[${attrs.date}]`)) el.textContent = `${T.released} ${fmt}`;
        }
        if (rel.html_url) for (const el of document.querySelectorAll(`[${attrs.notes}]`)) el.href = rel.html_url;
        for (const link of document.querySelectorAll("[data-asset]")) {
          const re = patterns[link.dataset.asset];
          const asset = re && (rel.assets || []).find((a) => re.test(a.name));
          if (!asset) continue;
          link.href = asset.browser_download_url;
          link.title = asset.name;
          const out = link.querySelector("[data-size]");
          if (out) out.textContent = size(asset.size);
        }
      })
      .catch(() => {
        /* rate limited or offline: links keep pointing at the releases page */
      });
  }

  fill(REPO, PATTERNS, { tag: "data-version", ver: "data-ver", date: "data-release-date", notes: "data-release-notes" });
  fill(APP_REPO, APP_PATTERNS, { tag: "data-app-version", ver: "data-app-ver", date: "data-app-release-date", notes: "data-app-release-notes" });
})();
