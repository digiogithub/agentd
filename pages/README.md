# agentd website

Static marketing site published with GitHub Pages by `.github/workflows/pages.yml` (on push to
`main` touching `pages/**`, or manually with `workflow_dispatch`). No build step: the workflow
copies this folder as-is.

```
pages/
  index.html        English
  es/index.html     Spanish (same markup; keep both in sync when editing)
  assets/site.css   hand-drawn postcard / wax-crayon look (SVG noise + #wobble filter; the only raster
                    images are the screenshots below)
  assets/site.js    copy buttons, scroll reveal, OS detection, latest-release download links
  assets/fonts/     Caveat, Patrick Hand, Courier Prime (OFL-1.1, latin subset, woff2)
  assets/favicon.svg         the app icon (same as packaging/agentd-desktop.svg)
  assets/app-icon.svg        the app icon, shown in the Apps section
  assets/apple-touch-icon.png  180x180 render of the app icon
  assets/shots/     work dashboard screenshots (webp), shown in the `#dashboard` section as taped
                    photographs (`.shots` / `.shot`): panel-desktop-agents, panel-desktop-board (1600x1012),
                    panel-mobile, panel-mobile-answer, panel-mobile-agent (780x1688)
```

The screenshots are renders of the Panel design artboards (Spanish UI, sample data), exported at 2x
and saved as webp (quality 84, under 250 KB each). Both languages use the same files; every `<img>`
carries `width`/`height` and an `alt` in the page's language.

Download links are filled at run time from the GitHub API
(`repos/digiogithub/agentd/releases/latest`), matched by asset name pattern (`.deb` amd64/arm64,
Linux `tar.gz`, macOS `.pkg`/`.zip`, `SHA256SUMS`). Without JavaScript, or when the API is rate
limited, every link points at the releases page, so a new release needs no redeploy. The desktop app
assets (`agentd-full_*.deb`, `agentd-desktop_*_linux_*.tar.gz`, `agentd-desktop_*_darwin_universal.zip`) come
from the same release. The Android APKs (`agentd-app-<v>-<abi>.apk`, `SHA256SUMS`) come from a second fetch of
`repos/digiogithub/agentd-app/releases/latest`, with their own version tag (`data-app-*` attributes) and fallback.

Preview locally:

```sh
python3 -m http.server -d pages 8000
```
