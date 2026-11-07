# agentd website

Static marketing site published with GitHub Pages by `.github/workflows/pages.yml` (on push to
`main` touching `pages/**`, or manually with `workflow_dispatch`). No build step: the workflow
copies this folder as-is.

```
pages/
  index.html        English
  es/index.html     Spanish (same markup; keep both in sync when editing)
  assets/site.css   hand-drawn postcard / wax-crayon look (SVG noise + #wobble filter, no images)
  assets/site.js    copy buttons, scroll reveal, OS detection, latest-release download links
  assets/fonts/     Caveat, Patrick Hand, Courier Prime (OFL-1.1, latin subset, woff2)
  assets/favicon.svg
```

Download links are filled at run time from the GitHub API
(`repos/digiogithub/agentd/releases/latest`), matched by asset name pattern (`.deb` amd64/arm64,
Linux `tar.gz`, macOS `.pkg`/`.zip`, `SHA256SUMS`). Without JavaScript, or when the API is rate
limited, every link points at the releases page, so a new release needs no redeploy.

Preview locally:

```sh
python3 -m http.server -d pages 8000
```
