# Linux app: prefer X11 (XWayland) over native Wayland

Builds on [[plans/api-gateway-plan]] (ADR-A18, Flutter app) and [[app-review-fixes]].

## What changed

`app/linux/runner/main.cc` unsets `GDK_BACKEND` and calls `gdk_set_allowed_backends("x11,wayland")`
before GTK starts, unless `AGENTD_APP_WAYLAND=1`. `GDK_BACKEND` has to be dropped because desktop launchers
export it (COSMIC's panel and app library set `GDK_BACKEND=wayland,x11`) and it wins over the allowed list:
a first version that honoured it worked from a terminal but still hung when started from the menu.

## Why

With Flutter 3.50 on the COSMIC Wayland compositor, the GTK embedder deadlocks at start: the platform
(GTK main) thread blocks on a futex waiting for the raster thread, which spins without its frame ever being
presented. The window shows but takes no input and cannot be closed. Reproduced with a stock
`flutter create` app, so it is not caused by agentd_app plugins. Disabling Impeller and running the UI
isolate on a separate thread did not help; under XWayland (`GDK_BACKEND=x11`) the main loop polls normally
and the app works.

## Verification

Release bundle launched on COSMIC Wayland: main thread in `poll` (not `futex`), X11 window created, the
maintainer confirmed the app is clickable. `.deb` regenerated (`dist/agentd-app_0.3.0~dev_amd64.deb`).
