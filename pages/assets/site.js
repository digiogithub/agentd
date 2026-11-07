// agentd website behaviour: copy buttons, scroll reveal, OS detection and the download links of
// the latest GitHub release. Everything degrades gracefully: without JavaScript, or when the
// GitHub API is unreachable or rate limited, every download link points at the releases page.
(() => {
  "use strict";

  const REPO = "digiogithub/agentd";
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
  const os = /mac|iphone|ipad/i.test(ua) ? "macos" : /linux|x11/i.test(ua) && !/android/i.test(ua) ? "linux" : "";
  if (os) document.querySelector(`.parcel[data-os="${os}"]`)?.classList.add("detected");

  // Fill the download links from the latest release. Asset names carry the version, so each link
  // names a pattern (data-asset) instead of a file.
  const PATTERNS = {
    "deb-amd64": /^agentd_[^_]+_amd64\.deb$/,
    "deb-arm64": /^agentd_[^_]+_arm64\.deb$/,
    "tar-amd64": /^agentd_[^_]+_linux_amd64\.tar\.gz$/,
    "tar-arm64": /^agentd_[^_]+_linux_arm64\.tar\.gz$/,
    pkg: /^agentd_[^_]+_darwin_universal\.pkg$/,
    zip: /^agentd_[^_]+_darwin_universal\.zip$/,
    sums: /^SHA256SUMS$/,
  };
  const size = (b) => (b < 1 << 10 ? `${b} B` : b < 1 << 20 ? `${Math.round(b / 1024)} KB` : `${(b / (1 << 20)).toFixed(1)} MB`);

  fetch(`https://api.github.com/repos/${REPO}/releases/latest`, { headers: { Accept: "application/vnd.github+json" } })
    .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
    .then((rel) => {
      const version = String(rel.tag_name || "").replace(/^v/, "");
      if (!version) return;
      for (const el of document.querySelectorAll("[data-version]")) el.textContent = `v${version}`;
      for (const el of document.querySelectorAll("[data-ver]")) el.textContent = version;
      const date = rel.published_at ? new Date(rel.published_at) : null;
      if (date && !isNaN(date)) {
        const fmt = date.toLocaleDateString(lang, { year: "numeric", month: "short", day: "numeric" });
        for (const el of document.querySelectorAll("[data-release-date]")) el.textContent = `${T.released} ${fmt}`;
      }
      if (rel.html_url) for (const el of document.querySelectorAll("[data-release-notes]")) el.href = rel.html_url;
      for (const link of document.querySelectorAll("[data-asset]")) {
        const re = PATTERNS[link.dataset.asset];
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
})();
