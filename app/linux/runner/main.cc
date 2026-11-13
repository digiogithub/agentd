#include "my_application.h"

int main(int argc, char** argv) {
  // Flutter's GTK embedder deadlocks on some native Wayland compositors (seen
  // with Flutter 3.50 on COSMIC: the platform thread waits on the raster
  // thread, which never gets its frame presented, so the window takes no
  // input and cannot be closed). Prefer X11 (XWayland) and fall back to
  // Wayland. Desktop launchers may export GDK_BACKEND=wayland,x11 (COSMIC
  // does), which would win over the allowed list, so it is dropped here;
  // child processes then get GTK's default again. AGENTD_APP_WAYLAND=1 keeps
  // the environment untouched (native Wayland when available).
  if (g_strcmp0(g_getenv("AGENTD_APP_WAYLAND"), "1") != 0) {
    g_unsetenv("GDK_BACKEND");
    gdk_set_allowed_backends("x11,wayland");
  }
  g_autoptr(MyApplication) app = my_application_new();
  return g_application_run(G_APPLICATION(app), argc, argv);
}
